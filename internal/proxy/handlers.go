package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/anthropic"
	"github.com/claude-code-opencode/claude-proxy/internal/config"
	"github.com/claude-code-opencode/claude-proxy/internal/log"
	"github.com/claude-code-opencode/claude-proxy/internal/models"
	"github.com/claude-code-opencode/claude-proxy/internal/providers"
	"github.com/claude-code-opencode/claude-proxy/internal/providers/codex"
	"github.com/claude-code-opencode/claude-proxy/internal/providers/errs"
)

func isThinkingRequested(req *anthropic.MessageRequest) bool {
	if req == nil || req.Thinking == nil {
		return false
	}
	return req.Thinking.Type == "enabled" || req.Thinking.Type == "adaptive"
}

type Handler struct {
	cfg     atomic.Pointer[config.Config]
	catalog *models.Catalog
	router  atomic.Pointer[providers.Registry]
	logger  *log.Logger

	// disabledUntil maps a model key (upstream/model) to when it can be retried.
	// The reason field tracks whether the disable was from a rate limit (429)
	// or a transport/server error, so writePreferenceExhausted can return the
	// correct HTTP status. Protected by disabledMu.
	disabledMu       sync.Mutex
	disabledUntil    map[string]disableInfo
	rateLimitRetries map[string]int // model key -> retry count for per-model 429
	probeResults     map[string]modelProbeResult

	discoveredMu    sync.RWMutex
	discoveredCodex []codex.ModelInfo
}

type disableInfo struct {
	until  time.Time
	reason string // "rate_limit", "error", or "unavailable"
}

type modelProbeResult struct {
	OK        bool
	CheckedAt time.Time
	LastError string
}

func NewHandler(cfg *config.Config, catalog *models.Catalog, router *providers.Registry, logger *log.Logger) *Handler {
	h := &Handler{
		catalog:          catalog,
		logger:           logger,
		disabledUntil:    make(map[string]disableInfo),
		rateLimitRetries: make(map[string]int),
		probeResults:     make(map[string]modelProbeResult),
	}
	h.cfg.Store(cfg)
	h.router.Store(router)
	return h
}

// loadConfig returns a snapshot of the current configuration.
// Safe for concurrent use — the pointer is read atomically.
func (h *Handler) loadConfig() *config.Config {
	return h.cfg.Load()
}

func (h *Handler) loadRouter() *providers.Registry {
	return h.router.Load()
}

func (h *Handler) replaceRouter(router *providers.Registry) {
	h.router.Store(router)
}

// providerFor returns the provider that serves the given model spec, or nil
// when it is not configured (e.g. a codex route without tokens).
func (h *Handler) providerFor(spec config.ModelSpec) providers.Provider {
	reg := h.loadRouter()
	if reg == nil {
		return nil
	}
	if spec.Upstream == config.CodexUpstreamName {
		return reg.Codex()
	}
	return reg.Provider(spec.Upstream)
}

// circuitBreakerCooldown is how long a model stays disabled after a
// transport error (5xx, connection failure) or after hitting the rate-limit
// retry threshold (2nd 429). 1 hour to let free-tier quotas fully reset.
const circuitBreakerCooldown = 60 * time.Minute

// rateLimitCooldown is how long a model stays disabled after a single 429.
// Free-tier quotas are bursty (per-window), so keep the cooldown short so a
// once-limited model is retried quickly before the full 1-hour blacklist.
const rateLimitCooldown = time.Minute

// ─── Public methods for the web interface ───────────────────────────────────

// WebModelStatus exposes model status for the web dashboard.
type WebModelStatus struct {
	Name          string     `json:"name"`
	Upstream      string     `json:"upstream"`
	OK            bool       `json:"ok"`
	Tested        bool       `json:"tested"`
	CheckedAt     *time.Time `json:"checked_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	DisabledUntil *time.Time `json:"disabled_until,omitempty"`
	Configured    bool       `json:"configured"`
	Order         int        `json:"order"`
}

// RecordModelProbe stores the latest result of a model availability probe.
func (h *Handler) RecordModelProbe(name string, err error) {
	h.disabledMu.Lock()
	defer h.disabledMu.Unlock()
	result := modelProbeResult{OK: err == nil, CheckedAt: time.Now()}
	if err != nil {
		result.LastError = err.Error()
	}
	h.probeResults[name] = result
}

func (h *Handler) webModelStatus(name, upstream string, order int, configured bool) WebModelStatus {
	h.disabledMu.Lock()
	defer h.disabledMu.Unlock()
	status := WebModelStatus{Name: name, Upstream: upstream, Order: order, Configured: configured}
	modelKey := upstream + "/" + name
	if probe, ok := h.probeResults[name]; ok {
		status.Tested = true
		status.OK = probe.OK
		status.LastError = probe.LastError
		checkedAt := probe.CheckedAt
		status.CheckedAt = &checkedAt
	}
	if info, disabled := h.disabledUntil[modelKey]; disabled && time.Now().Before(info.until) {
		status.OK = false
		untilCopy := info.until
		status.DisabledUntil = &untilCopy
	}
	return status
}

// GetModelStatuses returns the last probe status of configured and known models.
func (h *Handler) GetModelStatuses() []WebModelStatus {
	var statuses []WebModelStatus
	seen := make(map[string]bool)
	for i, spec := range h.loadConfig().EffectiveModels() {
		if seen[spec.Name] {
			continue
		}
		seen[spec.Name] = true
		if h.IsModelUpstreamUnavailable(spec.Upstream, spec.Name) {
			// The upstream no longer serves this model — hide it from the
			// dashboard instead of showing it red forever.
			continue
		}
		statuses = append(statuses, h.webModelStatus(spec.Name, spec.Upstream, i+1, true))
	}
	for _, m := range h.catalog.CachedModels() {
		if config.IsOpenCodeModelSupported(m.ID) && !seen[m.ID] {
			seen[m.ID] = true
			statuses = append(statuses, h.webModelStatus(m.ID, config.DefaultUpstreamName, len(statuses)+1, false))
		}
	}
	for _, model := range h.DiscoveredCodexModels() {
		if !seen[model.Slug] {
			seen[model.Slug] = true
			statuses = append(statuses, h.webModelStatus(model.Slug, config.CodexUpstreamName, len(statuses)+1, false))
		}
	}
	return statuses
}

func (h *Handler) SetDiscoveredCodexModels(discovered []codex.ModelInfo) {
	h.discoveredMu.Lock()
	h.discoveredCodex = append([]codex.ModelInfo(nil), discovered...)
	h.discoveredMu.Unlock()
}

func (h *Handler) DiscoveredCodexModels() []codex.ModelInfo {
	h.discoveredMu.RLock()
	defer h.discoveredMu.RUnlock()
	return append([]codex.ModelInfo(nil), h.discoveredCodex...)
}

// ApplyDiscoveredOpenCodeModels initializes a fresh default configuration from
// the current free Zen catalog. Explicit user model lists are never changed.
func (h *Handler) ApplyDiscoveredOpenCodeModels(discovered []models.ModelEntry) {
	current := h.loadConfig()
	if len(current.Models) != 0 {
		return
	}
	var specs []config.ModelSpec
	for _, model := range models.FilteredModels(discovered) {
		if !config.IsOpenCodeModelSupported(model.ID) || config.IsOpenCodeContributorFree(model.ID) {
			continue
		}
		specs = append(specs, config.ModelSpec{Name: model.ID, Upstream: config.DefaultUpstreamName})
	}
	if len(specs) == 0 {
		return
	}
	next := *current
	next.Models = specs
	next.Precompute()
	h.UpdateConfig(&next)
}

// TestModel probes a single model and returns nil on success.
func (h *Handler) TestModel(ctx context.Context, name string) error {
	var err error
	for _, spec := range h.loadConfig().EffectiveModels() {
		if spec.Name != name {
			continue
		}
		p := h.providerFor(spec)
		if p == nil {
			err = fmt.Errorf("no provider configured for upstream %q", spec.Upstream)
		} else {
			err = p.Check(ctx, name)
		}
		if errors.Is(err, errs.ErrModelUnavailable) {
			h.MarkModelUpstreamUnavailable(spec.Upstream, name)
		}
		h.RecordModelProbe(name, err)
		return err
	}
	// Try as a free model on the built-in zen upstream.
	p := h.loadRouter().Default()
	if p == nil {
		err = fmt.Errorf("no upstream for model %q", name)
	} else {
		err = p.Check(ctx, name)
	}
	if errors.Is(err, errs.ErrModelUnavailable) {
		h.MarkModelUpstreamUnavailable(config.DefaultUpstreamName, name)
	}
	h.RecordModelProbe(name, err)
	return err
}

// ResetCircuitBreakers clears all disabled model states.
func (h *Handler) ResetCircuitBreakers() {
	h.disabledMu.Lock()
	defer h.disabledMu.Unlock()
	h.disabledUntil = make(map[string]disableInfo)
	h.rateLimitRetries = make(map[string]int)
}

// clearRateLimitRetry drops the 429 retry count for a model once it has
// answered successfully, so the "N consecutive 429s" threshold only counts
// back-to-back rate limits rather than accumulating for the process lifetime.
func (h *Handler) clearRateLimitRetry(modelKey string) {
	h.disabledMu.Lock()
	delete(h.rateLimitRetries, modelKey)
	h.disabledMu.Unlock()
}

// GetConfig returns the current configuration.
func (h *Handler) GetConfig() *config.Config {
	return h.cfg.Load()
}

// UpdateConfig atomically replaces the configuration. Safe for concurrent use.
func (h *Handler) UpdateConfig(newCfg *config.Config) {
	h.cfg.Store(newCfg)
}

// ReorderModels reorders the configured model list.
func (h *Handler) ReorderModels(names []string) {
	// Build a copy to avoid racing on the shared config pointer.
	current := h.cfg.Load()
	var newModels []config.ModelSpec
	seen := make(map[string]bool)
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		for _, spec := range current.EffectiveModels() {
			if spec.Name == name {
				newModels = append(newModels, spec)
				break
			}
		}
	}
	if len(newModels) == 0 {
		return
	}
	next := *current
	next.Models = newModels
	next.Precompute()
	h.cfg.Store(&next)
}

func (h *Handler) disableModel(name string, reason string) {
	h.disabledMu.Lock()
	defer h.disabledMu.Unlock()

	// Parse upstream/model from the key (format: "upstream/model" or "upstream/*")
	upstreamName, modelName := parseModelKey(name)

	cooldown := circuitBreakerCooldown
	if reason == "rate_limit" {
		// A single 429 is treated as a burst of free-quota exhaustion and must
		// retried fast. Only the escalated blacklist (rate_limit_escalated) uses
		// the long circuitBreakerCooldown.
		cooldown = rateLimitCooldown
	}

	// Discovered models (not in explicit config) get shorter cooldown on transport errors
	// so they can be retried sooner as "best effort" fallbacks.
	isConfigured := h.isConfiguredModel(upstreamName, modelName)
	if !isConfigured && reason == "error" {
		cooldown = 5 * time.Minute
	}

	until := time.Now().Add(cooldown)
	if reason == "unavailable" {
		until = time.Time{}
		cooldown = 0
	}

	if existing, already := h.disabledUntil[name]; already {
		// Never clobber an in-progress disable with a shorter one: a single 429
		// (rate_limit, 1m) must not re-expose a model that is mid-transport-error
		// cooldown (60m) or mid-escalated-blacklist. A permanent "unavailable"
		// disable always wins.
		if existing.reason == "unavailable" {
			return
		}
		if !until.IsZero() && existing.until.After(until) {
			return
		}
		h.disabledUntil[name] = disableInfo{until: until, reason: reason}
		return
	}

	h.logger.Warn("model disabled", "model", name, "cooldown", cooldown, "reason", reason)
	h.disabledUntil[name] = disableInfo{until: until, reason: reason}
}

// parseModelKey splits "upstream/model" or "upstream/*" into components.
func parseModelKey(key string) (upstream, model string) {
	if idx := strings.Index(key, "/"); idx >= 0 {
		return key[:idx], key[idx+1:]
	}
	return key, ""
}

// isConfiguredModel checks if the model is in the explicit preference list (cfg.Models).
func (h *Handler) isConfiguredModel(upstreamName, modelName string) bool {
	if modelName == "*" {
		return true // upstream-wide disable is always configured
	}
	cfg := h.loadConfig()
	for _, m := range cfg.Models {
		if m.Upstream == upstreamName && m.Name == modelName {
			return true
		}
	}
	return false
}

// DisableModelUpstream temporarily disables a specific model on a specific upstream.
// The key is "upstream/model" so the same model name on different upstreams
// is tracked independently. reason should be "rate_limit" or "error".
func (h *Handler) DisableModelUpstream(upstreamName, modelName, reason string) {
	h.disableModelUpstream(upstreamName, modelName, reason)
}

// disableModelUpstream is the internal implementation.
func (h *Handler) disableModelUpstream(upstreamName, modelName, reason string) {
	h.disableModel(upstreamName+"/"+modelName, reason)
}

// DisableUpstream temporarily disables every model on an upstream. This is
// used for provider-wide quota errors where trying another model cannot help.
func (h *Handler) DisableUpstream(upstreamName, reason string) {
	h.disableUpstream(upstreamName, reason)
}

// disableUpstream temporarily disables every model on an upstream. This is
// used for provider-wide quota errors where trying another model cannot help.
func (h *Handler) disableUpstream(upstreamName, reason string) {
	h.disableModel(upstreamName+"/*", reason)
}

// trackRateLimit applies per-model rate-limit backoff for a 429 upstream
// response. A global rate limit disables the whole upstream (trying another
// model there cannot help); otherwise the model's consecutive-429 counter is
// incremented and the model disabled for the short burst cooldown (1st 429) or
// the long blacklist (2nd consecutive 429, "rate_limit_escalated"). It returns
// the backoff scope ("upstream" or "model") and the updated retry count (0 for
// a global limit), so callers can log consistently.
//
// This single method is shared by the streaming and non-streaming fallback
// paths so their rate-limit handling cannot diverge again.
func (h *Handler) trackRateLimit(upstream, model string, respBody []byte) (string, int) {
	if errs.IsGlobalRateLimit(respBody) {
		h.disableUpstream(upstream, "rate_limit")
		return "upstream", 0
	}
	modelKey := upstream + "/" + model
	h.disabledMu.Lock()
	retries := h.rateLimitRetries[modelKey] + 1
	h.rateLimitRetries[modelKey] = retries
	h.disabledMu.Unlock()
	if retries >= 2 {
		// Second consecutive 429: blacklist the model for the long
		// circuitBreakerCooldown rather than the 1-minute burst cooldown.
		// rate_limit_escalated maps to that longer duration in disableModel and
		// is still classified as rate limiting by allModelsDisabledByRateLimit.
		h.DisableModelUpstream(upstream, model, "rate_limit_escalated")
	} else {
		// First 429: disable with the short cooldown so subsequent requests
		// skip the model, while the current request falls through to the next.
		h.DisableModelUpstream(upstream, model, "rate_limit")
	}
	return "model", retries
}

func (h *Handler) isModelDisabled(name string) bool {
	h.disabledMu.Lock()
	defer h.disabledMu.Unlock()
	info, ok := h.disabledUntil[name]
	if !ok {
		return false
	}
	if info.reason == "unavailable" {
		// Permanent: the upstream no longer serves this model.
		return true
	}
	if time.Now().Before(info.until) {
		return true
	}
	// Cooldown expired — re-enable the model.
	delete(h.disabledUntil, name)
	h.logger.Info("model re-enabled after cooldown", "model", name)
	return false
}

// isModelUpstreamDisabled checks if a model on a specific upstream is disabled.
func (h *Handler) isModelUpstreamDisabled(upstreamName, modelName string) bool {
	return h.isModelDisabled(upstreamName+"/*") || h.isModelDisabled(upstreamName+"/"+modelName)
}

// IsModelUpstreamUnavailable reports whether the upstream answered that the
// model no longer exists. Such models are skipped and hidden from listings.
func (h *Handler) IsModelUpstreamUnavailable(upstreamName, modelName string) bool {
	h.disabledMu.Lock()
	defer h.disabledMu.Unlock()
	info, ok := h.disabledUntil[upstreamName+"/"+modelName]
	return ok && info.reason == "unavailable"
}

// MarkModelUpstreamUnavailable permanently disables a model the upstream no
// longer serves, so it is skipped from routing and hidden from listings.
func (h *Handler) MarkModelUpstreamUnavailable(upstreamName, modelName string) {
	h.DisableModelUpstream(upstreamName, modelName, "unavailable")
}

// allModelsDisabledByRateLimit checks if every model in the list was disabled
// specifically due to rate limiting (not transport errors).
func (h *Handler) allModelsDisabledByRateLimit(specs []config.ModelSpec) bool {
	if len(specs) == 0 {
		return false
	}
	h.disabledMu.Lock()
	defer h.disabledMu.Unlock()
	for _, s := range specs {
		info, ok := h.disabledUntil[s.Upstream+"/"+s.Name]
		if !ok {
			info, ok = h.disabledUntil[s.Upstream+"/*"]
		}
		if !ok || (info.reason != "rate_limit" && info.reason != "rate_limit_escalated") {
			return false
		}
	}
	return true
}

// nextDifferentUpstream finds the next index in fallbackModels whose upstream
// differs from the given upstream. Returns -1 if no such model exists.
func (h *Handler) nextDifferentUpstream(models []config.ModelSpec, startIdx int, upstream string) int {
	for j := startIdx + 1; j < len(models); j++ {
		if models[j].Upstream != upstream {
			return j
		}
	}
	return -1
}

// HandleMessages handles POST /v1/messages and POST /v1/messages?beta=true
func (h *Handler) HandleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, anthropic.ErrInvalidRequest, "Method not allowed")
		return
	}

	reqID := GetRequestID(r.Context())
	start := time.Now()

	h.logger.Debug("request received",
		"request_id", reqID,
		"method", r.Method,
		"path", r.URL.Path,
	)

	// Read body (middleware already enforces MaxBodySize via http.MaxBytesReader)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			h.writeError(w, http.StatusRequestEntityTooLarge, anthropic.ErrInvalidRequest, "Request body too large")
			return
		}
		h.writeError(w, http.StatusBadRequest, anthropic.ErrInvalidRequest, "Failed to read request body")
		return
	}

	// Parse request
	var msgReq anthropic.MessageRequest
	if err := json.Unmarshal(body, &msgReq); err != nil {
		h.writeError(w, http.StatusBadRequest, anthropic.ErrInvalidRequest, "Invalid JSON: "+err.Error())
		return
	}

	// Resolve model
	originalModel := msgReq.Model
	if h.loadConfig().DefaultModel == "" {
		h.writeError(w, http.StatusServiceUnavailable, anthropic.ErrInternalError, "Model catalog is not ready")
		return
	}

	// "custom" is the sentinel value set by ANTHROPIC_MODEL=custom in the
	// banner.  It means "use whatever the proxy's default model is".
	// If the client sends a specific model name that exists in our
	// preference list, try it first; otherwise fall back to default.
	resolvedModel := originalModel
	cfg := h.loadConfig()
	if originalModel == "custom" || originalModel == "" {
		resolvedModel = cfg.DefaultModel
	} else if originalModel != cfg.DefaultModel {
		// Check if the requested model is in our effective models list.
		requestedAllowed := false
		for _, m := range cfg.EffectiveModels() {
			if m.Name == originalModel {
				requestedAllowed = true
				break
			}
		}
		if !requestedAllowed && !cfg.AllowUnlisted {
			h.logger.Warn("model not in preference list, falling back to default",
				"request_id", reqID,
				"requested_model", originalModel,
				"fallback_model", cfg.DefaultModel,
			)
			resolvedModel = cfg.DefaultModel
		}
	}

	// Route to reasoning/completion model if configured
	// Only override when the user didn't explicitly choose a different model
	if resolvedModel == h.loadConfig().DefaultModel {
		if isThinkingRequested(&msgReq) && h.loadConfig().ReasoningModel != "" {
			h.logger.Debug("routing to reasoning model",
				"request_id", reqID,
				"from_model", resolvedModel,
				"to_model", h.loadConfig().ReasoningModel,
			)
			resolvedModel = h.loadConfig().ReasoningModel
		} else if !isThinkingRequested(&msgReq) && h.loadConfig().CompletionModel != "" {
			h.logger.Debug("routing to completion model",
				"request_id", reqID,
				"from_model", resolvedModel,
				"to_model", h.loadConfig().CompletionModel,
			)
			resolvedModel = h.loadConfig().CompletionModel
		}
	}

	// Check if model is allowed
	if !h.loadConfig().AllowUnlisted {
		if !h.loadConfig().IsModelAllowed(resolvedModel) {
			h.writeError(w, http.StatusBadRequest, anthropic.ErrInvalidRequest,
				fmt.Sprintf("Model '%s' is not available. Set ALLOW_UNLISTED_MODELS=true to allow any model.", originalModel))
			return
		}
	}

	msgReq.Model = resolvedModel

	h.logger.Info("request processing",
		"request_id", reqID,
		"requested_model", originalModel,
		"resolved_model", resolvedModel,
		"stream", msgReq.Stream,
	)

	// Get passthrough API key if enabled
	var passthroughKey string
	if h.loadConfig().PassthroughAPIKey {
		passthroughKey = GetPassthroughKey(r)
	}

	// Build ordered list of models to try (deduplicated, preference order),
	// skipping models disabled by the circuit breaker.
	preferenceModels := h.buildPreferenceList(&config.ModelSpec{
		Name:     resolvedModel,
		Upstream: h.loadConfig().UpstreamForModel(resolvedModel),
	})
	// Copy before filtering to avoid mutating the shared PrecomputedFallbacks slice.
	usable := make([]config.ModelSpec, 0, len(preferenceModels))
	for _, spec := range preferenceModels {
		if h.isModelUpstreamDisabled(spec.Upstream, spec.Name) {
			h.logger.Debug("skipping disabled model", "model", spec.Name, "upstream", spec.Upstream)
			continue
		}
		usable = append(usable, spec)
	}
	fallbackModels := usable
	if len(fallbackModels) == 0 {
		h.writePreferenceExhausted(w, reqID, preferenceModels)
		return
	}

	// Forward to upstream
	if msgReq.Stream {
		h.handleStream(w, r, &msgReq, originalModel, fallbackModels, reqID, start, passthroughKey)
	} else {
		h.handleNonStream(w, r, &msgReq, originalModel, fallbackModels, reqID, start, passthroughKey)
	}
}

func (h *Handler) handleNonStream(w http.ResponseWriter, r *http.Request, msgReq *anthropic.MessageRequest, originalModel string, fallbackModels []config.ModelSpec, reqID string, start time.Time, authOverride ...string) {
	ctx, cancel := context.WithTimeout(r.Context(), h.loadConfig().RequestTimeout)
	defer cancel()
	var maxRetryAfter *int
	var lastConvertErr error

	// Indexed loop (not `for ... range`): the conversion-error branch below
	// advances `i` directly (i = next - 1) to skip the remaining models on the
	// same upstream, which only works with a manual index.
	for i := 0; i < len(fallbackModels); i++ {
		spec := fallbackModels[i]
		msgReq.Model = spec.Name
		if h.isModelUpstreamDisabled(spec.Upstream, spec.Name) {
			h.logger.Debug("skipping newly disabled model", "model", spec.Name, "upstream", spec.Upstream)
			continue
		}

		if i > 0 {
			h.logger.Info("trying next model in preference order",
				"request_id", reqID,
				"attempt", i+1,
				"model", spec.Name,
				"upstream", spec.Upstream,
			)
		}

		provider := h.providerFor(spec)
		if provider == nil {
			h.logger.Warn("provider not configured, skipping",
				"request_id", reqID, "model", spec.Name, "upstream", spec.Upstream)
			continue
		}

		upStart := time.Now()
		resp, err := provider.Invoke(ctx, msgReq, spec.Name, h.loadConfig().DefaultModel, false, authOverride...)
		if err != nil {
			if errors.Is(err, errs.ErrInvalidRequest) {
				// The provider cannot encode this request. Every model on the
				// same upstream shares the same converter, so further models on
				// this upstream would fail identically — skip straight to the
				// next upstream (e.g. Codex) instead of looping over every Zen
				// fallback. Return the conversion error as a 400 only if no
				// other upstream can be tried.
				lastConvertErr = err
				h.logger.Warn("provider cannot encode request, skipping remaining models on upstream",
					"request_id", reqID, "model", spec.Name, "upstream", spec.Upstream, "error", err.Error())
				next := h.nextDifferentUpstream(fallbackModels, i, spec.Upstream)
				if next >= 0 {
					i = next - 1 // loop increments
					continue
				}
				h.writeError(w, http.StatusBadRequest, anthropic.ErrInvalidRequest, "Conversion error: "+lastConvertErr.Error())
				return
			}
			if errors.Is(err, errs.ErrMarshal) {
				h.writeError(w, http.StatusInternalServerError, anthropic.ErrInternalError, "Failed to marshal request")
				return
			}
			h.logger.Warn("upstream transport error, trying next model",
				"request_id", reqID, "model", spec.Name, "upstream", spec.Upstream,
				"has_more_fallbacks", i < len(fallbackModels)-1, "error", err.Error())
			h.DisableModelUpstream(spec.Upstream, spec.Name, "error")
			continue
		}
		upLatency := time.Since(upStart)

		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, h.loadConfig().MaxBodySize+1))
		if readErr != nil {
			_ = resp.Body.Close()
			h.writeError(w, http.StatusBadGateway, anthropic.ErrInternalError, "Failed to read upstream response")
			return
		}

		if resp.StatusCode == 429 {
			_ = resp.Body.Close()
			retryAfter := anthropic.ParseRetryAfter(resp.Header)
			retryAfterLog := "unknown"
			if retryAfter != nil {
				retryAfterLog = strconv.Itoa(*retryAfter)
			}
			scope, retries := h.trackRateLimit(spec.Upstream, spec.Name, respBody)
			if retries >= 2 {
				h.logger.Warn("rate limit retry limit reached, blacklisting model",
					"request_id", reqID,
					"model", spec.Name,
					"upstream", spec.Upstream,
					"retries", retries,
					"cooldown", circuitBreakerCooldown,
				)
			} else if retries == 1 {
				h.logger.Debug("rate limit hit, disabling model for short cooldown",
					"request_id", reqID,
					"model", spec.Name,
					"upstream", spec.Upstream,
					"retries", retries,
				)
			}
			h.logger.Warn("rate limited on model",
				"request_id", reqID,
				"used_model", spec.Name,
				"retry_after_seconds", retryAfterLog,
				"scope", scope,
				"has_more_fallbacks", i < len(fallbackModels)-1,
			)
			// Track max Retry-After across all attempts.
			if retryAfter != nil {
				if maxRetryAfter == nil || *retryAfter > *maxRetryAfter {
					maxRetryAfter = retryAfter
				}
			}
			if i < len(fallbackModels)-1 {
				continue
			}
			// All models exhausted — return 429 with Retry-After.
			anthBody, anthStatus := anthropic.NewRateLimitResponse(429, "Rate limit exceeded on all models", maxRetryAfter)
			w.Header().Set("Content-Type", "application/json")
			if maxRetryAfter != nil {
				w.Header().Set("Retry-After", strconv.Itoa(*maxRetryAfter))
			}
			w.WriteHeader(anthStatus)
			_, _ = w.Write(anthBody)
			tried := modelNames(fallbackModels)
			h.logger.Info("all models in preference list exhausted",
				"request_id", reqID, "tried_models", tried)
			return
		}

		if resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504 {
			h.logger.Warn("upstream transient error, trying next model",
				"request_id", reqID,
				"model", spec.Name,
				"status", resp.StatusCode,
				"has_more_fallbacks", i < len(fallbackModels)-1,
			)
			h.DisableModelUpstream(spec.Upstream, spec.Name, "error")
			_ = resp.Body.Close()
			if i < len(fallbackModels)-1 {
				continue
			}
			// All models exhausted — return the last error.
			anthBody, anthStatus := anthropic.UpstreamErrorToAnthropic(resp.StatusCode, string(respBody))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(anthStatus)
			_, _ = w.Write(anthBody)
			return
		}

		if resp.StatusCode >= 400 {
			// A 5xx (other than 502/503/504 handled above) is still transient —
			// never mark a model permanently unavailable on a server error.
			if resp.StatusCode < 500 && errs.IsModelUnavailable(respBody) {
				h.MarkModelUpstreamUnavailable(spec.Upstream, spec.Name)
				h.logger.Warn("model no longer available on upstream, skipping (hidden)",
					"request_id", reqID,
					"model", spec.Name,
					"upstream", spec.Upstream,
					"upstream_status", resp.StatusCode,
					"has_more_fallbacks", i < len(fallbackModels)-1,
				)
				_ = resp.Body.Close()
				if i < len(fallbackModels)-1 {
					continue
				}
				// Every model is unavailable — return the last error.
				anthBody, anthStatus := anthropic.UpstreamErrorToAnthropic(resp.StatusCode, string(respBody))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(anthStatus)
				_, _ = w.Write(anthBody)
				h.logger.Info("all models in preference list exhausted",
					"request_id", reqID,
					"tried_models", modelNames(fallbackModels),
				)
				return
			}
			anthBody, anthStatus := anthropic.UpstreamErrorToAnthropic(resp.StatusCode, string(respBody))
			h.logger.Info("upstream client error, trying next model",
				"request_id", reqID,
				"upstream_status", resp.StatusCode,
				"model", spec.Name,
				"upstream_latency", upLatency.String(),
				"has_more_fallbacks", i < len(fallbackModels)-1,
			)
			_ = resp.Body.Close()
			if i < len(fallbackModels)-1 {
				continue
			}
			// All models exhausted — return the last 4xx error.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(anthStatus)
			_, _ = w.Write(anthBody)
			h.logger.Info("all models in preference list exhausted",
				"request_id", reqID,
				"tried_models", modelNames(fallbackModels),
			)
			return
		}

		// Success — decode through the provider.
		h.clearRateLimitRetry(spec.Upstream + "/" + spec.Name)
		anthBody, parseErr := provider.ParseResponse(spec.Name, respBody, originalModel)
		_ = resp.Body.Close()
		if parseErr != nil {
			if errors.Is(parseErr, errs.ErrNoContent) {
				h.logger.Warn("no usable response from provider, trying next model",
					"request_id", reqID, "model", spec.Name, "upstream", spec.Upstream)
				continue
			}
			if errors.Is(parseErr, errs.ErrFailed) {
				h.logger.Warn("provider response reported a failure, trying next model",
					"request_id", reqID, "model", spec.Name, "upstream", spec.Upstream)
				h.DisableModelUpstream(spec.Upstream, spec.Name, "error")
				continue
			}
			h.writeError(w, http.StatusBadGateway, anthropic.ErrInternalError, "Invalid upstream response")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(anthBody)

		latency := time.Since(start)
		h.logger.Info("request completed",
			"request_id", reqID,
			"status", http.StatusOK,
			"upstream_latency", upLatency.String(),
			"latency", latency.String(),
			"requested_model", originalModel,
			"used_model", spec.Name,
			"preference_step", i,
			"stream", false,
		)
		return
	}

	h.writePreferenceExhausted(w, reqID, fallbackModels)
}

func (h *Handler) HandleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, anthropic.ErrInvalidRequest, "Method not allowed")
		return
	}
	// The configured routing list is the proxy contract. Do not block model
	// discovery on a remote catalog refresh, which is only an optional expansion.
	modelsList := make([]models.ModelEntry, 0, len(h.loadConfig().EffectiveModels()))
	seen := make(map[string]bool)
	for _, spec := range h.loadConfig().EffectiveModels() {
		if h.providerFor(spec) == nil {
			continue
		}
		if h.IsModelUpstreamUnavailable(spec.Upstream, spec.Name) {
			// The upstream no longer serves this model — hide it.
			continue
		}
		if !seen[spec.Name] {
			modelsList = append(modelsList, models.ModelEntry{ID: spec.Name, Object: "model", OwnedBy: spec.Upstream})
			seen[spec.Name] = true
		}
	}
	if h.loadConfig().ExposeAllModels {
		for _, model := range h.catalog.GetModels(false) {
			if config.IsOpenCodeModelSupported(model.ID) && !seen[model.ID] {
				modelsList = append(modelsList, model)
				seen[model.ID] = true
			}
		}
		for _, model := range h.DiscoveredCodexModels() {
			if !seen[model.Slug] {
				modelsList = append(modelsList, models.ModelEntry{ID: model.Slug, Object: "model", OwnedBy: config.CodexUpstreamName})
				seen[model.Slug] = true
			}
		}
	}

	resp := models.ToResponse(modelsList, h.loadConfig().ZenBaseURL)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleHealthz handles GET /healthz
func (h *Handler) HandleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, anthropic.ErrInvalidRequest, "Method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// HandleReadyz handles GET /readyz
func (h *Handler) HandleReadyz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, anthropic.ErrInvalidRequest, "Method not allowed")
		return
	}
	probeTimeout := h.loadConfig().RequestTimeout
	if probeTimeout <= 0 || probeTimeout > 5*time.Second {
		probeTimeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()
	effective := h.loadConfig().EffectiveModels()
	if len(effective) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not_ready","error":"no configured model"}`))
		return
	}
	primary := effective[0]
	p := h.providerFor(primary)
	var checkErr error
	if p == nil {
		checkErr = errors.New("no provider configured for upstream " + primary.Upstream)
	} else {
		checkErr = p.Check(ctx, primary.Name, GetPassthroughKey(r))
	}
	if checkErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not_ready","error":"upstream unreachable"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ready"}`))
}

// HandleVersion handles GET /version
func (h *Handler) HandleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, anthropic.ErrInvalidRequest, "Method not allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"version": getVersion(), "binary": "claude-proxy"})
}

var versionStore atomic.Value

func getVersion() string {
	v, _ := versionStore.Load().(string)
	return v
}

// SetVersion updates the version string.
func SetVersion(v string) {
	versionStore.Store(v)
}

func (h *Handler) writeError(w http.ResponseWriter, status int, errType, message string) {
	body, _ := anthropic.NewErrorResponse(status, errType, message)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (h *Handler) codexClient() providers.Provider {
	reg := h.loadRouter()
	if reg == nil {
		return nil
	}
	return reg.Codex()
}

func (h *Handler) writePreferenceExhausted(w http.ResponseWriter, reqID string, specs []config.ModelSpec) {
	// If every model was disabled due to rate limiting, return 429 so the
	// client applies proper backoff. Transport errors → 502.
	if h.allModelsDisabledByRateLimit(specs) {
		h.logger.Warn("all models disabled by circuit breaker (rate limited)",
			"request_id", reqID, "tried_models", modelNames(specs))
		anthBody, anthStatus := anthropic.NewRateLimitResponse(429,
			"All configured models are temporarily disabled by the circuit breaker (rate limit)", nil)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(anthStatus)
		_, _ = w.Write(anthBody)
		return
	}
	h.logger.Warn("all models in preference list exhausted before a response",
		"request_id", reqID, "tried_models", modelNames(specs))
	h.writeError(w, http.StatusBadGateway, anthropic.ErrInternalError,
		"All configured upstream models failed before responding")
}

// modelNames returns the model names of the specs, used for logging.
func modelNames(specs []config.ModelSpec) []string {
	out := make([]string, len(specs))
	for i, m := range specs {
		out[i] = m.Name
	}
	return out
}

// buildPreferenceList returns the ordered list of ModelSpecs to try. The
// configured `models` preference order is walked as-is regardless of what
// model name the client wrote in Claude Code.
//
// The one exception is reasoning/completion routing: when HandleMessages
// resolved the request to a distinct routing model (e.g. a ReasoningModel),
// that model is tried first, followed by the configured list.
func (h *Handler) buildPreferenceList(primary *config.ModelSpec) []config.ModelSpec {
	cfg := h.loadConfig()
	configured := cfg.PrecomputedFallbacks

	if primary == nil {
		primary = &config.ModelSpec{Name: cfg.DefaultModel, Upstream: config.DefaultUpstreamName}
	}

	if len(configured) == 0 {
		return []config.ModelSpec{*primary}
	}

	// Models routed to Codex are only usable when the Codex backend is
	// configured. Drop them from the ordered list otherwise.
	codexAvailable := h.codexClient() != nil
	filtered := make([]config.ModelSpec, 0, len(configured))
	for _, m := range configured {
		if m.Upstream == config.CodexUpstreamName && !codexAvailable {
			continue
		}
		filtered = append(filtered, m)
	}
	if len(filtered) == 0 {
		return []config.ModelSpec{*primary}
	}
	configured = filtered

	// If the resolved model already leads the list, use it as-is.
	if configured[0].Name == primary.Name && configured[0].Upstream == primary.Upstream {
		if !cfg.OnlyPreferredModels {
			return h.appendDiscoveredModels(configured, primary, codexAvailable)
		}
		return configured
	}

	// Reasoning/completion routing: prepend the resolved model, then the list.
	seen := make(map[string]bool, len(configured)+1)
	list := make([]config.ModelSpec, 0, len(configured)+1)
	list = append(list, *primary)
	seen[primary.Name] = true
	for _, m := range configured {
		if !seen[m.Name] {
			list = append(list, m)
			seen[m.Name] = true
		}
	}
	if !cfg.OnlyPreferredModels {
		return h.appendDiscoveredModels(list, primary, codexAvailable)
	}
	return list
}

// appendDiscoveredModels adds discovered Zen and Codex models as additional
// fallbacks when OnlyPreferredModels is false.
func (h *Handler) appendDiscoveredModels(list []config.ModelSpec, primary *config.ModelSpec, codexAvailable bool) []config.ModelSpec {
	seen := make(map[string]bool, len(list))
	for _, m := range list {
		seen[m.Name] = true
	}

	// Add discovered free Zen models from catalog.
	if h.catalog != nil {
		for _, m := range models.FilteredModels(h.catalog.CachedModels()) {
			if config.IsOpenCodeModelSupported(m.ID) && !config.IsOpenCodeContributorFree(m.ID) && !seen[m.ID] {
				list = append(list, config.ModelSpec{Name: m.ID, Upstream: config.DefaultUpstreamName})
				seen[m.ID] = true
			}
		}
	}

	// Add discovered Codex models.
	if codexAvailable {
		for _, model := range h.DiscoveredCodexModels() {
			if !seen[model.Slug] {
				list = append(list, config.ModelSpec{Name: model.Slug, Upstream: config.CodexUpstreamName})
				seen[model.Slug] = true
			}
		}
	}
	return list
}
