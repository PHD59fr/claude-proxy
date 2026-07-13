package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/anthropic"
	"github.com/claude-code-opencode/claude-proxy/internal/codex"
	"github.com/claude-code-opencode/claude-proxy/internal/config"
	"github.com/claude-code-opencode/claude-proxy/internal/convert"
	"github.com/claude-code-opencode/claude-proxy/internal/log"
	"github.com/claude-code-opencode/claude-proxy/internal/models"
	"github.com/claude-code-opencode/claude-proxy/internal/openai"
	"github.com/claude-code-opencode/claude-proxy/internal/upstream"
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
	router  *upstream.Router
	logger  *log.Logger

	// disabledUntil maps a model key (upstream/model) to when it can be retried.
	// The reason field tracks whether the disable was from a rate limit (429)
	// or a transport/server error, so writePreferenceExhausted can return the
	// correct HTTP status. Protected by disabledMu.
	disabledMu    sync.Mutex
	disabledUntil map[string]disableInfo
	probeResults  map[string]modelProbeResult
}

type disableInfo struct {
	until  time.Time
	reason string // "rate_limit" or "error"
}

type modelProbeResult struct {
	OK        bool
	CheckedAt time.Time
	LastError string
}

func NewHandler(cfg *config.Config, catalog *models.Catalog, router *upstream.Router, logger *log.Logger) *Handler {
	h := &Handler{
		catalog:       catalog,
		router:        router,
		logger:        logger,
		disabledUntil: make(map[string]disableInfo),
		probeResults:  make(map[string]modelProbeResult),
	}
	h.cfg.Store(cfg)
	return h
}

// loadConfig returns a snapshot of the current configuration.
// Safe for concurrent use — the pointer is read atomically.
func (h *Handler) loadConfig() *config.Config {
	return h.cfg.Load()
}

// circuitBreakerCooldown is how long a model stays disabled after a failure
// before it is tried again.
const circuitBreakerCooldown = 15 * time.Minute

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
	if probe, ok := h.probeResults[name]; ok {
		status.Tested = true
		status.OK = probe.OK
		status.LastError = probe.LastError
		checkedAt := probe.CheckedAt
		status.CheckedAt = &checkedAt
	}
	if info, disabled := h.disabledUntil[name]; disabled && time.Now().Before(info.until) {
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
		statuses = append(statuses, h.webModelStatus(spec.Name, spec.Upstream, i+1, true))
	}
	for _, m := range models.DefaultModels {
		if !seen[m.ID] {
			seen[m.ID] = true
			statuses = append(statuses, h.webModelStatus(m.ID, "", len(statuses)+1, false))
		}
	}
	for _, name := range []string{
		"gpt-5.6", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
		"gpt-5.4", "gpt-5.4-mini",
	} {
		if !seen[name] {
			seen[name] = true
			statuses = append(statuses, h.webModelStatus(name, "codex", len(statuses)+1, false))
		}
	}
	return statuses
}

// TestModel probes a single model and returns nil on success.
func (h *Handler) TestModel(ctx context.Context, name string) error {
	var err error
	for _, spec := range h.loadConfig().EffectiveModels() {
		if spec.Name != name {
			continue
		}
		if spec.Upstream == config.CodexUpstreamName {
			if h.router.Codex() == nil {
				err = fmt.Errorf("codex not configured")
			} else {
				err = h.router.Codex().CheckModel(ctx, name)
			}
			h.RecordModelProbe(name, err)
			return err
		}
		client, clientErr := h.router.ClientForModel(name, spec.Upstream)
		if clientErr != nil {
			err = clientErr
		} else {
			err = client.CheckChatCompletion(ctx, name)
		}
		h.RecordModelProbe(name, err)
		return err
	}
	// Try as free model on default upstream.
	client := h.router.DefaultClient()
	if client == nil {
		err = fmt.Errorf("no upstream for model %q", name)
	} else {
		err = client.CheckChatCompletion(ctx, name)
	}
	h.RecordModelProbe(name, err)
	return err
}

// ResetCircuitBreakers clears all disabled model states.
func (h *Handler) ResetCircuitBreakers() {
	h.disabledMu.Lock()
	defer h.disabledMu.Unlock()
	h.disabledUntil = make(map[string]disableInfo)
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
	if _, already := h.disabledUntil[name]; !already {
		h.logger.Warn("model disabled by circuit breaker", "model", name, "cooldown", circuitBreakerCooldown, "reason", reason)
	}
	h.disabledUntil[name] = disableInfo{until: time.Now().Add(circuitBreakerCooldown), reason: reason}
}

// disableModelUpstream disables a specific model on a specific upstream.
// The key is "upstream/model" so the same model name on different upstreams
// is tracked independently. reason should be "rate_limit" or "error".
func (h *Handler) disableModelUpstream(upstreamName, modelName, reason string) {
	h.disableModel(upstreamName+"/"+modelName, reason)
}

func (h *Handler) isModelDisabled(name string) bool {
	h.disabledMu.Lock()
	defer h.disabledMu.Unlock()
	info, ok := h.disabledUntil[name]
	if !ok {
		return false
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
	return h.isModelDisabled(upstreamName + "/" + modelName)
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
		if !ok || info.reason != "rate_limit" {
			return false
		}
	}
	return true
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

	// "custom" is the sentinel value set by ANTHROPIC_MODEL=custom in the
	// banner.  It means "use whatever the proxy's default model is".
	// The proxy ignores the client-requested model name and always routes to
	// its own configured default model (the first entry in the ordered
	// models list); the name Claude Code sends is not used. Any non-default
	// name falls back to the default as well.
	resolvedModel := originalModel
	if originalModel == "custom" || originalModel == "" || originalModel != h.loadConfig().DefaultModel {
		if originalModel != "custom" && originalModel != "" {
			h.logger.Warn("model not found, falling back to default",
				"request_id", reqID,
				"requested_model", originalModel,
				"fallback_model", h.loadConfig().DefaultModel,
			)
		}
		resolvedModel = h.loadConfig().DefaultModel
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
	fallbackModels := h.buildPreferenceList(&config.ModelSpec{
		Name:     resolvedModel,
		Upstream: h.loadConfig().UpstreamForModel(resolvedModel),
	})
	// Copy before filtering to avoid mutating the shared PrecomputedFallbacks slice.
	usable := make([]config.ModelSpec, 0, len(fallbackModels))
	for _, spec := range fallbackModels {
		if h.isModelUpstreamDisabled(spec.Upstream, spec.Name) {
			h.logger.Debug("skipping disabled model", "model", spec.Name, "upstream", spec.Upstream)
			continue
		}
		usable = append(usable, spec)
	}
	fallbackModels = usable

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

	for i, spec := range fallbackModels {
		msgReq.Model = spec.Name

		if i > 0 {
			h.logger.Info("trying next model in preference order",
				"request_id", reqID,
				"attempt", i+1,
				"model", spec.Name,
				"upstream", spec.Upstream,
			)
		}

		// Codex model: route through Codex client
		if spec.Upstream == config.CodexUpstreamName {
			if h.router.Codex() == nil {
				h.logger.Warn("codex model but codex backend not configured, skipping",
					"request_id", reqID, "model", spec.Name)
				continue
			}
			codexReq, err := codex.TransformRequest(msgReq)
			if err != nil {
				h.logger.Warn("codex transform failed, skipping model",
					"request_id", reqID, "model", spec.Name, "error", err.Error())
				continue
			}
			reqBody, err := json.Marshal(codexReq)
			if err != nil {
				continue
			}

			upStart := time.Now()
			resp, err := h.router.Codex().Do(ctx, "POST", codex.CodexPath, bytes.NewReader(reqBody))
			if err != nil {
				h.logger.Warn("codex upstream error, trying next model",
					"request_id", reqID, "model", spec.Name, "error", err.Error())
				continue
			}
			upLatency := time.Since(upStart)

			if resp.StatusCode == 429 {
				_ = resp.Body.Close()
				retryAfter := anthropic.ParseRetryAfter(resp.Header)
				h.logger.Warn("rate limited on model",
					"request_id", reqID, "used_model", spec.Name,
					"retry_after", retryAfter,
					"has_more_fallbacks", i < len(fallbackModels)-1)
				if retryAfter != nil {
					if maxRetryAfter == nil || *retryAfter > *maxRetryAfter {
						maxRetryAfter = retryAfter
					}
				}
				h.disableModelUpstream(spec.Upstream, spec.Name, "rate_limit")
				if i < len(fallbackModels)-1 {
					continue
				}
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

			if resp.StatusCode >= 400 {
				respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				_ = resp.Body.Close()
				h.logger.Warn("codex upstream error (non-stream)",
					"request_id", reqID, "model", spec.Name,
					"status", resp.StatusCode,

					"has_more_fallbacks", i < len(fallbackModels)-1)
				h.disableModelUpstream(spec.Upstream, spec.Name, "error")
				if i < len(fallbackModels)-1 {
					continue
				}
				anthBody, anthStatus := anthropic.UpstreamErrorToAnthropic(resp.StatusCode, string(respBody))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(anthStatus)
				_, _ = w.Write(anthBody)
				return
			}

			// Success: parse Codex non-stream response. The Codex backend
			// streams the output as response.output_item.done events and sends
			// an (almost) empty response body in response.done/response.completed
			// — the final body's "output" array is empty. Reassemble the output
			// from the item events so text and tool-calls are not lost.
			respBody, err := io.ReadAll(io.LimitReader(resp.Body, h.loadConfig().MaxBodySize+1))
			_ = resp.Body.Close()
			if err != nil {
				continue
			}
			events := codex.ParseSSEEvents(respBody)
			doneBody := codex.FindDoneBody(events)
			if doneBody == nil || codex.IsFailedStatus(doneBody.Status) {
				status := "none"
				if doneBody != nil {
					status = doneBody.Status
				}
				h.logger.Warn("codex non-stream: no usable response in Codex SSE, trying next model",
					"request_id", reqID, "model", spec.Name, "status", status)
				continue
			}
			// Reassemble output items from response.output_item.done events.
			var outputItems []codex.OutputItem
			for _, evt := range events {
				if evt.Type == "response.output_item.done" && evt.Item != nil {
					outputItems = append(outputItems, *evt.Item)
				}
			}
			if len(outputItems) > 0 {
				doneBody.Output = outputItems
			}
			anthResp := codex.TransformResponse(doneBody, originalModel)
			anthRespBody, err := json.Marshal(anthResp)
			if err != nil {
				continue
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(anthRespBody)
			latency := time.Since(start)
			h.logger.Info("request completed",
				"request_id", reqID, "status", http.StatusOK,
				"upstream_latency", upLatency.String(), "latency", latency.String(),
				"requested_model", originalModel, "used_model", spec.Name,
				"preference_step", i, "stream", false)
			return
		}

		// Regular model: route through the upstream that serves it.
		client, err := h.router.ClientForModel(spec.Name, spec.Upstream)
		if err != nil {
			h.logger.Warn("no upstream for model, skipping",
				"request_id", reqID, "model", spec.Name, "upstream", spec.Upstream, "error", err.Error())
			continue
		}

		// Regular model: OpenAI Chat Completions path
		oaiReq, err := convert.Request(msgReq, h.loadConfig().DefaultModel)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, anthropic.ErrInvalidRequest, "Conversion error: "+err.Error())
			return
		}

		reqBody, err := json.Marshal(oaiReq)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, anthropic.ErrInternalError, "Failed to marshal request")
			return
		}

		upStart := time.Now()
		resp, err := client.Do(ctx, "POST", "/chat/completions", bytes.NewReader(reqBody), authOverride...)
		if err != nil {
			h.logger.Warn("upstream transport error, trying next model",
				"request_id", reqID, "model", spec.Name, "upstream", spec.Upstream,
				"has_more_fallbacks", i < len(fallbackModels)-1, "error", err.Error())
			h.disableModelUpstream(spec.Upstream, spec.Name, "error")
			continue
		}
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, h.loadConfig().MaxBodySize+1))
		upLatency := time.Since(upStart)

		if err != nil {
			_ = resp.Body.Close()
			h.writeError(w, http.StatusBadGateway, anthropic.ErrInternalError, "Failed to read upstream response")
			return
		}

		if resp.StatusCode == 429 {
			retryAfter := anthropic.ParseRetryAfter(resp.Header)
			h.logger.Warn("rate limited on model",
				"request_id", reqID,
				"used_model", spec.Name,
				"retry_after", retryAfter,
				"has_more_fallbacks", i < len(fallbackModels)-1,
			)
			h.disableModelUpstream(spec.Upstream, spec.Name, "rate_limit")
			// Track max Retry-After across all attempts
			if retryAfter != nil {
				if maxRetryAfter == nil || *retryAfter > *maxRetryAfter {
					maxRetryAfter = retryAfter
				}
			}
			// Try next fallback model
			if i < len(fallbackModels)-1 {
				_ = resp.Body.Close()
				continue
			}
			// All models exhausted — return 429 with Retry-After
			anthBody, anthStatus := anthropic.NewRateLimitResponse(429, "Rate limit exceeded on all models", maxRetryAfter)
			w.Header().Set("Content-Type", "application/json")
			if maxRetryAfter != nil {
				w.Header().Set("Retry-After", strconv.Itoa(*maxRetryAfter))
			}
			w.WriteHeader(anthStatus)
			_, _ = w.Write(anthBody)
			tried := modelNames(fallbackModels)
			h.logger.Info("all models in preference list exhausted",
				"request_id", reqID,
				"tried_models", tried,
			)
			_ = resp.Body.Close()
			return
		}

		if resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504 {
			h.logger.Warn("upstream transient error, trying next model",
				"request_id", reqID,
				"model", spec.Name,
				"status", resp.StatusCode,
				"has_more_fallbacks", i < len(fallbackModels)-1,
			)
			h.disableModelUpstream(spec.Upstream, spec.Name, "error")
			_ = resp.Body.Close()
			if i < len(fallbackModels)-1 {
				continue
			}
			// All models exhausted — return the last error
			anthBody, anthStatus := anthropic.UpstreamErrorToAnthropic(resp.StatusCode, string(respBody))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(anthStatus)
			_, _ = w.Write(anthBody)
			return
		}

		if resp.StatusCode >= 400 {
			anthBody, anthStatus := anthropic.UpstreamErrorToAnthropic(resp.StatusCode, string(respBody))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(anthStatus)
			_, _ = w.Write(anthBody)
			h.logger.Info("upstream error",
				"request_id", reqID,
				"upstream_status", resp.StatusCode,
				"model", spec.Name,
				"upstream_latency", upLatency.String(),
			)
			_ = resp.Body.Close()
			return
		}

		// Success — parse and return
		var oaiResp openai.ChatCompletionResponse
		if err := json.Unmarshal(respBody, &oaiResp); err != nil {
			_ = resp.Body.Close()
			h.writeError(w, http.StatusBadGateway, anthropic.ErrInternalError, "Invalid upstream response")
			return
		}

		anthResp := convert.Response(&oaiResp, originalModel)
		anthRespBody, err := json.Marshal(anthResp)
		if err != nil {
			_ = resp.Body.Close()
			h.writeError(w, http.StatusInternalServerError, anthropic.ErrInternalError, "Failed to marshal response")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(anthRespBody)

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
		_ = resp.Body.Close()
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
		if spec.Upstream == config.CodexUpstreamName && h.router.Codex() == nil {
			continue
		}
		if !seen[spec.Name] {
			modelsList = append(modelsList, models.ModelEntry{ID: spec.Name, Object: "model", OwnedBy: spec.Upstream})
			seen[spec.Name] = true
		}
	}
	if h.loadConfig().ExposeAllModels {
		for _, model := range h.catalog.GetModels(false) {
			if !seen[model.ID] {
				modelsList = append(modelsList, model)
				seen[model.ID] = true
			}
		}
	}

	resp := models.ToResponse(modelsList, h.loadConfig().UpstreamBaseURL)

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
	var err error
	if primary.Upstream == config.CodexUpstreamName {
		client := h.router.Codex()
		if client == nil {
			err = errors.New("codex backend not configured")
		} else {
			err = client.CheckModel(ctx, primary.Name)
		}
	} else {
		client, clientErr := h.router.ClientForModel(primary.Name, primary.Upstream)
		if clientErr != nil {
			err = clientErr
		} else {
			err = client.Check(ctx)
		}
	}
	if err != nil {
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

func (h *Handler) codexClient() *upstream.CodexClient {
	if h.router == nil {
		return nil
	}
	return h.router.Codex()
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
	configured := h.loadConfig().PrecomputedFallbacks

	if primary == nil {
		primary = &config.ModelSpec{Name: h.loadConfig().DefaultModel, Upstream: config.DefaultUpstreamName}
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
	return list
}
