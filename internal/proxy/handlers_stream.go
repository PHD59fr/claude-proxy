package proxy

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/anthropic"
	"github.com/claude-code-opencode/claude-proxy/internal/config"
	"github.com/claude-code-opencode/claude-proxy/internal/convert"
	"github.com/claude-code-opencode/claude-proxy/internal/providers/errs"
)

func (h *Handler) handleStream(w http.ResponseWriter, r *http.Request, msgReq *anthropic.MessageRequest, originalModel string, fallbackModels []config.ModelSpec, reqID string, start time.Time, authOverride ...string) {
	ctx := r.Context()
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
		resp, err := provider.Invoke(ctx, msgReq, spec.Name, h.loadConfig().DefaultModel, true, authOverride...)
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
			h.DisableModelUpstream(spec.Upstream, spec.Name, "error")
			h.logger.Warn("upstream transport error, trying next model",
				"request_id", reqID, "model", spec.Name, "upstream", spec.Upstream,
				"has_more_fallbacks", i < len(fallbackModels)-1, "error", err.Error())
			continue
		}
		upLatency := time.Since(upStart)

		// Validate that the upstream actually returned a streaming response.
		// Some models/upstreams may return a regular JSON response even when
		// streaming was requested. Detect this and handle as non-streaming.
		// Only do this for 200 OK responses; errors should be handled by
		// the existing status code logic below.
		contentType := resp.Header.Get("Content-Type")
		isSSE := strings.Contains(contentType, "text/event-stream")

		if resp.StatusCode == http.StatusOK && !isSSE {
			// Upstream returned 200 OK but not SSE — likely a regular JSON response.
			// Read body and try to parse as regular response.
			respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, h.loadConfig().MaxBodySize+1))
			_ = resp.Body.Close()
			if readErr != nil {
				h.logger.Warn("failed to read non-streaming upstream response", "request_id", reqID, "error", readErr.Error())
				h.DisableModelUpstream(spec.Upstream, spec.Name, "error")
				continue
			}

			// Try to parse as regular chat completion response
			provider := h.providerFor(spec)
			if provider != nil {
				anthBody, parseErr := provider.ParseResponse(spec.Name, respBody, originalModel)
				if parseErr == nil {
					// Successfully parsed as non-streaming response — return it
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(anthBody)
					h.clearRateLimitRetry(spec.Upstream + "/" + spec.Name)
					latency := time.Since(start)
					h.logger.Info("request completed (non-streaming fallback)",
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
				h.logger.Debug("non-streaming fallback parse failed, trying next model",
					"request_id", reqID, "model", spec.Name, "error", parseErr.Error())
			}

			// If we couldn't parse it, treat as upstream error and try next model
			h.DisableModelUpstream(spec.Upstream, spec.Name, "error")
			continue
		}

		if resp.StatusCode == 429 {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
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
			if retryAfter != nil {
				if maxRetryAfter == nil || *retryAfter > *maxRetryAfter {
					maxRetryAfter = retryAfter
				}
			}
			if i < len(fallbackModels)-1 {
				continue
			}
			// All models exhausted
			anthBody, anthStatus := anthropic.NewRateLimitResponse(429, "Rate limit exceeded on all models", maxRetryAfter)
			w.Header().Set("Content-Type", "application/json")
			if maxRetryAfter != nil {
				w.Header().Set("Retry-After", strconv.Itoa(*maxRetryAfter))
			}
			w.WriteHeader(anthStatus)
			_, _ = w.Write(anthBody)
			h.logger.Info("all models in preference list exhausted",
				"request_id", reqID,
				"tried_models", modelNames(fallbackModels),
			)
			return
		}

		if resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504 {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			h.logger.Warn("upstream transient error, trying next model",
				"request_id", reqID,
				"model", spec.Name,
				"status", resp.StatusCode,
				"has_more_fallbacks", i < len(fallbackModels)-1,
			)
			h.DisableModelUpstream(spec.Upstream, spec.Name, "error")
			if i < len(fallbackModels)-1 {
				continue
			}
			anthBody, anthStatus := anthropic.UpstreamErrorToAnthropic(resp.StatusCode, string(respBody))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(anthStatus)
			_, _ = w.Write(anthBody)
			return
		}

		if resp.StatusCode >= 400 {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
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
				if i < len(fallbackModels)-1 {
					continue
				}
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
			h.logger.Info("upstream client error, trying next model",
				"request_id", reqID,
				"upstream_status", resp.StatusCode,
				"model", spec.Name,
				"upstream_latency", upLatency.String(),
				"has_more_fallbacks", i < len(fallbackModels)-1,
			)
			if i < len(fallbackModels)-1 {
				continue
			}
			// All models exhausted — return the last 4xx error.
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

		// Success — stream the response.
		h.clearRateLimitRetry(spec.Upstream + "/" + spec.Name)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		chunks := provider.StreamChunks(ctx, spec.Name, resp.Body, originalModel, h.loadConfig().RequestTimeout)
		// Close the body once the stream has been fully consumed (or abandoned on
		// a conversion error) so the upstream connection is always returned to the
		// pool. StreamChunks reads resp.Body in a goroutine that finishes when its
		// source channel closes, which happens before handleStream returns.
		defer func() { _ = resp.Body.Close() }()
		sc := convert.NewStreamConverter(w, originalModel, "msg_"+reqID)

		if err := sc.Convert(chunks); err != nil {
			h.logger.Debug("stream conversion ended",
				"request_id", reqID,
				"error", err.Error(),
			)
			return
		}

		latency := time.Since(start)
		h.logger.Info("stream completed",
			"request_id", reqID,
			"status", http.StatusOK,
			"upstream_latency", upLatency.String(),
			"latency", latency.String(),
			"requested_model", originalModel,
			"used_model", spec.Name,
			"preference_step", i,
			"stream", true,
		)
		return
	}

	h.writePreferenceExhausted(w, reqID, fallbackModels)
}
