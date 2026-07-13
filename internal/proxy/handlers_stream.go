package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/anthropic"
	"github.com/claude-code-opencode/claude-proxy/internal/codex"
	"github.com/claude-code-opencode/claude-proxy/internal/config"
	"github.com/claude-code-opencode/claude-proxy/internal/convert"
	"github.com/claude-code-opencode/claude-proxy/internal/openai"
)

func (h *Handler) handleStream(w http.ResponseWriter, r *http.Request, msgReq *anthropic.MessageRequest, originalModel string, fallbackModels []config.ModelSpec, reqID string, start time.Time, authOverride ...string) {
	ctx := r.Context()
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
				h.disableModelUpstream(spec.Upstream, spec.Name, "error")
				h.logger.Warn("codex upstream error, trying next model",
					"request_id", reqID, "model", spec.Name, "error", err.Error())
				continue
			}
			upLatency := time.Since(upStart)

			if resp.StatusCode == 429 {
				_ = resp.Body.Close()
				retryAfter := anthropic.ParseRetryAfter(resp.Header)
				h.disableModelUpstream(spec.Upstream, spec.Name, "rate_limit")
				h.logger.Warn("rate limited on model",
					"request_id", reqID, "used_model", spec.Name,
					"retry_after", retryAfter,
					"has_more_fallbacks", i < len(fallbackModels)-1)
				if retryAfter != nil {
					if maxRetryAfter == nil || *retryAfter > *maxRetryAfter {
						maxRetryAfter = retryAfter
					}
				}
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
				h.logger.Info("all models in preference list exhausted",
					"request_id", reqID, "tried_models", modelNames(fallbackModels))
				return
			}

			if resp.StatusCode >= 400 {
				respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				_ = resp.Body.Close()
				h.disableModelUpstream(spec.Upstream, spec.Name, "error")
				h.logger.Warn("codex upstream error (stream)",
					"request_id", reqID, "model", spec.Name,
					"status", resp.StatusCode,

					"has_more_fallbacks", i < len(fallbackModels)-1)
				if i < len(fallbackModels)-1 {
					continue
				}
				anthBody, anthStatus := anthropic.UpstreamErrorToAnthropic(resp.StatusCode, string(respBody))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(anthStatus)
				_, _ = w.Write(anthBody)
				return
			}

			// Success: stream Codex response
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)

			sc := convert.NewStreamConverter(w, originalModel, "msg_"+reqID)
			chunks := codex.ParseCodexStreamWithTimeout(r.Context(), resp.Body, h.loadConfig().RequestTimeout)
			oaiCh := make(chan openai.StreamChunk, 16)
			go func() {
				defer close(oaiCh)
				for chunk := range chunks {
					if chunk.Err != nil {
						oaiCh <- openai.StreamChunk{Err: chunk.Err}
						return
					}
					// Emit text deltas before Done so text in the same chunk isn't lost
					if chunk.TextDelta != "" {
						oaiCh <- openai.StreamChunk{
							Chunk: openai.ChatCompletionChunk{
								Model: originalModel,
								Choices: []openai.ChunkChoice{{
									Index: 0,
									Delta: openai.ChatDelta{Content: chunk.TextDelta},
								}},
							},
						}
					}
					if chunk.ToolCallStart != nil {
						oaiCh <- openai.StreamChunk{
							Chunk: openai.ChatCompletionChunk{
								Model: originalModel,
								Choices: []openai.ChunkChoice{{
									Index: 0,
									Delta: openai.ChatDelta{
										ToolCalls: []openai.ToolCallDelta{{
											Index:    chunk.ToolCallIndex,
											ID:       chunk.ToolCallStart.ID,
											Type:     "function",
											Function: openai.FuncDelta{Name: chunk.ToolCallStart.Name},
										}},
									},
								}},
							},
						}
					}
					if chunk.ToolCallDelta != nil {
						oaiCh <- openai.StreamChunk{
							Chunk: openai.ChatCompletionChunk{
								Model: originalModel,
								Choices: []openai.ChunkChoice{{
									Index: 0,
									Delta: openai.ChatDelta{
										ToolCalls: []openai.ToolCallDelta{{
											Index:    chunk.ToolCallDelta.Index,
											Function: openai.FuncDelta{Arguments: chunk.ToolCallDelta.Arguments},
										}},
									},
								}},
							},
						}
					}
					if chunk.Done != nil {
						finishReason := "stop"
						oaiCh <- openai.StreamChunk{
							Chunk: openai.ChatCompletionChunk{
								Model: originalModel,
								Choices: []openai.ChunkChoice{{
									Index:        0,
									FinishReason: &finishReason,
								}},
							},
						}
						oaiCh <- openai.StreamChunk{Done: true}
						// Do NOT return — continue reading in case the Codex backend
						// sends subsequent responses (e.g. text after tool calls).
					}
				}
			}()

			if err := sc.Convert(oaiCh); err != nil {
				h.logger.Debug("codex stream conversion ended",
					"request_id", reqID, "error", err.Error())
				return
			}
			latency := time.Since(start)
			h.logger.Info("stream completed",
				"request_id", reqID, "status", http.StatusOK,
				"upstream_latency", upLatency.String(), "latency", latency.String(),
				"requested_model", originalModel, "used_model", spec.Name,
				"preference_step", i, "stream", true)
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
		resp, err := client.Stream(ctx, "POST", "/chat/completions", bytes.NewReader(reqBody), authOverride...)
		if err != nil {
			h.logger.Warn("upstream transport error, trying next model",
				"request_id", reqID, "model", spec.Name, "upstream", spec.Upstream,
				"has_more_fallbacks", i < len(fallbackModels)-1, "error", err.Error())
			h.disableModelUpstream(spec.Upstream, spec.Name, "error")
			continue
		}
		upLatency := time.Since(upStart)

		if resp.StatusCode == 429 {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			retryAfter := anthropic.ParseRetryAfter(resp.Header)
			h.logger.Warn("rate limited on model",
				"request_id", reqID,
				"used_model", spec.Name,
				"retry_after", retryAfter,
				"has_more_fallbacks", i < len(fallbackModels)-1,
			)
			h.disableModelUpstream(spec.Upstream, spec.Name, "rate_limit")
			if retryAfter != nil {
				if maxRetryAfter == nil || *retryAfter > *maxRetryAfter {
					maxRetryAfter = retryAfter
				}
			}
			_ = respBody // consumed for logging
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

		if resp.StatusCode >= 400 {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
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
			return
		}

		// Success — stream the response
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		chunks := openai.ParseStreamWithTimeout(r.Context(), resp.Body, h.loadConfig().RequestTimeout)
		sc := convert.NewStreamConverter(w, originalModel, "msg_"+reqID)

		if err := sc.Convert(chunks); err != nil {
			h.logger.Debug("stream conversion ended",
				"request_id", reqID,
				"error", err.Error(),
			)
			return
		}

		_ = resp.Body.Close()

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
