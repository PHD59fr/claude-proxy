package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/config"
	"github.com/claude-code-opencode/claude-proxy/internal/log"
	"github.com/claude-code-opencode/claude-proxy/internal/models"
	"github.com/claude-code-opencode/claude-proxy/internal/upstream"
)

func newMockUpstream(handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(handler))
}

func newTestRouter(upstreamURL, apiKey string) *upstream.Router {
	return upstream.NewRouter([]config.UpstreamConfig{{
		Name:    config.DefaultUpstreamName,
		BaseURL: upstreamURL,
		APIKey:  apiKey,
	}}, nil, 30*time.Second)
}

func defaultConfig(upstreamURL string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.UpstreamBaseURL = upstreamURL
	cfg.UpstreamAPIKey = "test-key"
	cfg.InboundAPIKey = ""
	cfg.DefaultModel = "big-pickle"
	return cfg
}

func newTestHandler(upstreamURL string) (*Handler, *config.Config) {
	cfg := defaultConfig(upstreamURL)
	logger := log.New("debug", "text")
	router := newTestRouter(upstreamURL, cfg.UpstreamAPIKey)
	catalog := models.NewCatalog(upstreamURL, cfg.UpstreamAPIKey, 5*time.Minute)
	return NewHandler(cfg, catalog, router, logger), cfg
}

func TestHandleMessages_NonStreaming(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		// Verify auth header
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			t.Errorf("auth = %q, want 'Bearer test-key'", auth)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-test",
			"model": "big-pickle",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "PROXY OK"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
		}`))
	})
	defer ts.Close()

	handler, _ := newTestHandler(ts.URL)

	body := `{
		"model": "big-pickle",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": "Reply with exactly: PROXY OK"}]
	}`

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var resp struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	if resp.Type != "message" {
		t.Errorf("type = %q, want message", resp.Type)
	}
	if resp.Role != "assistant" {
		t.Errorf("role = %q, want assistant", resp.Role)
	}
	if resp.Model != "big-pickle" {
		t.Errorf("model = %q, want big-pickle", resp.Model)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "PROXY OK" {
		t.Errorf("content = %v, want [{text: PROXY OK}]", resp.Content)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want end_turn", resp.StopReason)
	}
	if resp.Usage.InputTokens != 10 {
		t.Errorf("input_tokens = %d, want 10", resp.Usage.InputTokens)
	}
}

func TestHandleMessages_BetaParam(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-beta",
			"model": "big-pickle",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "beta works"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 3}
		}`))
	})
	defer ts.Close()

	handler, _ := newTestHandler(ts.URL)

	body := `{"model":"big-pickle","max_tokens":50,"messages":[{"role":"user","content":"test"}]}`

	req := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	if !strings.Contains(w.Body.String(), "beta works") {
		t.Error("response missing expected content")
	}
}

func TestHandleMessages_Streaming(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		chunks := []string{
			`{"id":"chatcmpl-stream","model":"big-pickle","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			`{"id":"chatcmpl-stream","model":"big-pickle","choices":[{"index":0,"delta":{"content":"STREAM"}}]}`,
			`{"id":"chatcmpl-stream","model":"big-pickle","choices":[{"index":0,"delta":{"content":" OK"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		}

		for _, chunk := range chunks {
			if chunk == "[DONE]" {
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			} else {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			}
			w.(http.Flusher).Flush()
		}
	})
	defer ts.Close()

	handler, _ := newTestHandler(ts.URL)

	body := `{"model":"big-pickle","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"test"}]}`

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	bodyStr := w.Body.String()
	if !strings.Contains(bodyStr, "message_start") {
		t.Error("missing message_start event")
	}
	if !strings.Contains(bodyStr, "content_block_start") {
		t.Error("missing content_block_start event")
	}
	if !strings.Contains(bodyStr, `"text":"STREAM"`) {
		t.Error("missing STREAM text delta")
	}
	if !strings.Contains(bodyStr, `"text":" OK"`) {
		t.Error("missing ' OK' text delta")
	}
	if !strings.Contains(bodyStr, "content_block_stop") {
		t.Error("missing content_block_stop event")
	}
	if !strings.Contains(bodyStr, "message_delta") {
		t.Error("missing message_delta event")
	}
	if !strings.Contains(bodyStr, "message_stop") {
		t.Error("missing message_stop event")
	}
}

func TestHandleModels(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"big-pickle","object":"model"},{"id":"other-model","object":"model"}]}`))
	})
	defer ts.Close()

	handler, _ := newTestHandler(ts.URL)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()

	handler.HandleModels(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	if resp.Object != "list" {
		t.Errorf("object = %q, want list", resp.Object)
	}

	// Should only contain big-pickle (filtered)
	found := false
	for _, m := range resp.Data {
		if m.ID == "big-pickle" {
			found = true
		}
		if m.ID == "other-model" {
			t.Error("other-model should be filtered out")
		}
	}
	if !found {
		t.Error("big-pickle not found in models list")
	}
}

func TestHandleHealthz(t *testing.T) {
	handler, _ := newTestHandler("http://localhost:1")

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()

	handler.HandleHealthz(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "ok") {
		t.Error("response should contain ok")
	}
}

func TestHandleVersion(t *testing.T) {
	handler, _ := newTestHandler("http://localhost:1")
	SetVersion("1.2.3")

	req := httptest.NewRequest("GET", "/version", nil)
	w := httptest.NewRecorder()

	handler.HandleVersion(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Version != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", resp.Version)
	}
}

func TestHandleMessages_MethodNotAllowed(t *testing.T) {
	handler, _ := newTestHandler("http://localhost:1")

	req := httptest.NewRequest("GET", "/v1/messages", nil)
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleMessages_InvalidJSON(t *testing.T) {
	handler, _ := newTestHandler("http://localhost:1")

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var errResp struct {
		Type  string `json:"type"`
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &errResp)
	if errResp.Error.Type != "invalid_request_error" {
		t.Errorf("error type = %q, want invalid_request_error", errResp.Error.Type)
	}
}

func TestHandleMessages_UpstreamError(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	})
	defer ts.Close()

	handler, _ := newTestHandler(ts.URL)

	body := `{"model":"big-pickle","max_tokens":100,"messages":[{"role":"user","content":"test"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}

	var errResp struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &errResp)
	if errResp.Error.Type != "rate_limit_error" {
		t.Errorf("error type = %q, want rate_limit_error", errResp.Error.Type)
	}
}

func TestAuth_NoKeyConfigured(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	})
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.InboundAPIKey = "" // No auth required
	logger := log.New("debug", "text")
	router := newTestRouter(ts.URL, cfg.UpstreamAPIKey)
	catalog := models.NewCatalog(ts.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, logger)

	body := `{"model":"big-pickle","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// No auth header
	handler.HandleMessages(w, req)

	// With no inbound key configured, this should pass through
	// (auth middleware allows unauthenticated when no key is configured)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (no auth required): %s", w.Code, w.Body.String())
	}
}

func TestServer_GracefulShutdown(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"big-pickle","object":"model"}]}`))
	})
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.ListenAddr = "127.0.0.1:0" // Random port
	logger := log.New("info", "text")

	srv := NewServer(cfg, logger)
	// Setup before starting the goroutine to avoid race
	srv.Setup()

	// Start in background
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start()
	}()

	// Wait for server to start listening
	time.Sleep(200 * time.Millisecond)

	// Shutdown
	ctx := context.Background()
	if err := srv.Shutdown(ctx); err != nil {
		t.Errorf("shutdown error: %v", err)
	}
}

func TestHandleMessages_ToolCallResponse(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-tools",
			"model": "big-pickle",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": null,
					"tool_calls": [{
						"id": "call_xyz",
						"type": "function",
						"function": {
							"name": "get_weather",
							"arguments": "{\"location\":\"Paris\"}"
						}
					}]
				},
				"finish_reason": "tool_calls"
			}],
			"usage": {"prompt_tokens": 20, "completion_tokens": 15}
		}`))
	})
	defer ts.Close()

	handler, _ := newTestHandler(ts.URL)

	body := `{"model":"big-pickle","max_tokens":100,"messages":[{"role":"user","content":"weather?"}],"tools":[{"name":"get_weather","description":"Get weather","input_schema":{"type":"object"}}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var resp struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", resp.StopReason)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "tool_use" {
		t.Errorf("content = %v, want tool_use block", resp.Content)
	}
}

func TestHandleMessages_AllowUnlistedModels(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-x",
			"model": "custom-model",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}]
		}`))
	})
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.AllowUnlisted = true
	logger := log.New("debug", "text")
	router := newTestRouter(ts.URL, cfg.UpstreamAPIKey)
	catalog := models.NewCatalog(ts.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, logger)

	body := `{"model":"custom-model","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (unlisted allowed): %s", w.Code, w.Body.String())
	}
}

func TestRequestID_Middleware(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	})
	defer ts.Close()

	handler, _ := newTestHandler(ts.URL)

	body := `{"model":"big-pickle","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// Apply requestID middleware - wrap handler in http.HandlerFunc
	h := requestID(http.HandlerFunc(handler.HandleMessages))
	h.ServeHTTP(w, req)

	reqID := w.Header().Get("X-Request-ID")
	if reqID == "" {
		t.Error("X-Request-ID header not set")
	}
	if !strings.HasPrefix(reqID, "req_") {
		t.Errorf("X-Request-ID = %q, want req_ prefix", reqID)
	}
}

func TestHandleMessages_FallbackOn502(t *testing.T) {
	// Mock upstream that returns 502 for primary model, 200 for fallback model
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		// Read body to determine model
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		if body.Model == "primary-model" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"bad gateway","type":"server_error"}}`))
			return
		}

		// Fallback model succeeds
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-fb",
			"model": "fallback-model",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "fallback ok"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 3}
		}`))
	})
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.DefaultModel = "primary-model"
	cfg.PrecomputedFallbacks = []config.ModelSpec{
		{Name: "primary-model", Upstream: config.DefaultUpstreamName},
		{Name: "fallback-model", Upstream: config.DefaultUpstreamName},
	}
	cfg.Precompute()
	logger := log.New("debug", "text")
	router := newTestRouter(ts.URL, cfg.UpstreamAPIKey)
	catalog := models.NewCatalog(ts.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, logger)

	body := `{"model":"primary-model","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Model   string `json:"model"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if len(resp.Content) != 1 || resp.Content[0].Text != "fallback ok" {
		t.Errorf("content = %v, want [{text: fallback ok}]", resp.Content)
	}
}

func TestHandleMessages_FallbackOn503(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		if body.Model == "primary-model" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"service unavailable","type":"server_error"}}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-fb2",
			"model": "fallback-model",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "recovered"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 3}
		}`))
	})
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.DefaultModel = "primary-model"
	cfg.PrecomputedFallbacks = []config.ModelSpec{
		{Name: "primary-model", Upstream: config.DefaultUpstreamName},
		{Name: "fallback-model", Upstream: config.DefaultUpstreamName},
	}
	cfg.Precompute()
	logger := log.New("debug", "text")
	router := newTestRouter(ts.URL, cfg.UpstreamAPIKey)
	catalog := models.NewCatalog(ts.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, logger)

	body := `{"model":"primary-model","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if len(resp.Content) != 1 || resp.Content[0].Text != "recovered" {
		t.Errorf("content = %v, want [{text: recovered}]", resp.Content)
	}
}

func TestHandleMessages_FallbackAllModelsExhausted502(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		// All models return 502
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"bad gateway","type":"server_error"}}`))
	})
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.DefaultModel = "model-a"
	cfg.PrecomputedFallbacks = []config.ModelSpec{
		{Name: "model-a", Upstream: config.DefaultUpstreamName},
		{Name: "model-b", Upstream: config.DefaultUpstreamName},
	}
	logger := log.New("debug", "text")
	router := newTestRouter(ts.URL, cfg.UpstreamAPIKey)
	catalog := models.NewCatalog(ts.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, logger)

	body := `{"model":"model-a","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	// Should not return 200 — all models exhausted
	if w.Code == http.StatusOK {
		t.Fatalf("status = 200, want non-200 (all models exhausted)")
	}

	// Should be a 5xx error from the last failed upstream
	var errResp struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &errResp)
	if errResp.Error.Type == "" {
		t.Error("expected error type in response")
	}
}

func TestHandleReadyz_UpstreamOK(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path: %s, want /models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	defer ts.Close()

	handler, _ := newTestHandler(ts.URL)

	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()

	handler.HandleReadyz(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != `{"status":"ready"}` {
		t.Errorf("body = %q, want {\"status\":\"ready\"}", w.Body.String())
	}
}

func TestHandleReadyz_UpstreamDown(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	ts.Close() // Close immediately so upstream is unreachable

	handler, _ := newTestHandler(ts.URL)

	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()

	handler.HandleReadyz(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not_ready") {
		t.Errorf("body = %q, want to contain not_ready", w.Body.String())
	}
}

func TestHandleMessages_FallbackToDefaultRegardlessOfName(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-alias",
			"model": "big-pickle",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "alias worked"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 3}
		}`))
	})
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.DefaultModel = "big-pickle"
	cfg.AllowUnlisted = false
	logger := log.New("debug", "text")
	router := newTestRouter(ts.URL, cfg.UpstreamAPIKey)
	catalog := models.NewCatalog(ts.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, logger)

	// Requesting any non-default model name resolves to the configured
	// default model (the proxy ignores the client-requested name).
	body := `{"model":"my-custom-name","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if len(resp.Content) != 1 || resp.Content[0].Text != "alias worked" {
		t.Errorf("content = %v, want [{text: alias worked}]", resp.Content)
	}
}

func TestHandleMessages_StreamingFallbackOn502(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		if body.Model == "primary-model" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"bad gateway","type":"server_error"}}`))
			return
		}

		// Fallback model returns streaming SSE
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"id":"chatcmpl-fb","model":"fallback-stream","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			`{"id":"chatcmpl-fb","model":"fallback-stream","choices":[{"index":0,"delta":{"content":"STREAMED"}}]}`,
			`{"id":"chatcmpl-fb","model":"fallback-stream","choices":[{"index":0,"delta":{"content":" OK"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		}
		for _, chunk := range chunks {
			if chunk == "[DONE]" {
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			} else {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			}
			w.(http.Flusher).Flush()
		}
	})
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.DefaultModel = "primary-model"
	cfg.PrecomputedFallbacks = []config.ModelSpec{
		{Name: "primary-model", Upstream: config.DefaultUpstreamName},
		{Name: "fallback-stream", Upstream: config.DefaultUpstreamName},
	}
	cfg.Precompute()
	logger := log.New("debug", "text")
	router := newTestRouter(ts.URL, cfg.UpstreamAPIKey)
	catalog := models.NewCatalog(ts.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, logger)

	body := `{"model":"primary-model","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"test"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	bodyStr := w.Body.String()
	if !strings.Contains(bodyStr, "message_start") {
		t.Error("missing message_start event")
	}
	if !strings.Contains(bodyStr, `"text":"STREAMED"`) {
		t.Error("missing STREAMED text delta")
	}
	if !strings.Contains(bodyStr, `"text":" OK"`) {
		t.Error("missing ' OK' text delta")
	}
	if !strings.Contains(bodyStr, "message_stop") {
		t.Error("missing message_stop event")
	}
}

func TestHandleMessages_OrderedModelsIgnoreClientName(t *testing.T) {
	// The proxy should try models in the configured priority order
	// regardless of the model name written in Claude Code.
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		// Record which model the proxy sent to upstream.
		if body.Model != "big-pickle" {
			t.Errorf("upstream received model = %q, want big-pickle (1st in configured order)", body.Model)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-ordered",
			"model": "big-pickle",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "ordered ok"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 3}
		}`))
	})
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	// Client names a different model ("whatever-i-typed"), but the proxy must
	// walk the configured order starting with big-pickle.
	cfg.Models = []config.ModelSpec{
		{Name: "big-pickle", Upstream: config.DefaultUpstreamName},
		{Name: "gpt-5.6-terra", Upstream: config.CodexUpstreamName},
		{Name: "deepseek-v4-flash-free", Upstream: config.DefaultUpstreamName},
	}
	cfg.Precompute()
	cfg.AllowUnlisted = true
	logger := log.New("debug", "text")
	router := newTestRouter(ts.URL, cfg.UpstreamAPIKey)
	catalog := models.NewCatalog(ts.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, logger)

	body := `{"model":"whatever-i-typed","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Content) != 1 || resp.Content[0].Text != "ordered ok" {
		t.Errorf("content = %v, want [{text: ordered ok}]", resp.Content)
	}
}

func TestHandleMessages_ReasoningModelRouting(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		if body.Model != "reasoning-model" {
			t.Errorf("upstream received model = %q, want reasoning-model", body.Model)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-reason",
			"model": "reasoning-model",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "reasoned"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5}
		}`))
	})
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.ReasoningModel = "reasoning-model"
	cfg.AllowUnlisted = true
	logger := log.New("debug", "text")
	router := newTestRouter(ts.URL, cfg.UpstreamAPIKey)
	catalog := models.NewCatalog(ts.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, logger)

	body := `{"model":"big-pickle","max_tokens":100,"thinking":{"type":"enabled","budget_tokens":10000},"messages":[{"role":"user","content":"think"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if len(resp.Content) != 1 || resp.Content[0].Text != "reasoned" {
		t.Errorf("content = %v, want [{text: reasoned}]", resp.Content)
	}
}

func TestHandleMessages_CompletionModelRouting(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		if body.Model != "completion-model" {
			t.Errorf("upstream received model = %q, want completion-model", body.Model)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-complete",
			"model": "completion-model",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "completed"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5}
		}`))
	})
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.CompletionModel = "completion-model"
	cfg.AllowUnlisted = true
	logger := log.New("debug", "text")
	router := newTestRouter(ts.URL, cfg.UpstreamAPIKey)
	catalog := models.NewCatalog(ts.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, logger)

	body := `{"model":"big-pickle","max_tokens":100,"messages":[{"role":"user","content":"complete this"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if len(resp.Content) != 1 || resp.Content[0].Text != "completed" {
		t.Errorf("content = %v, want [{text: completed}]", resp.Content)
	}
}

func TestHandleMessages_PassthroughKey(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		// Verify passthrough key is forwarded as Bearer token
		auth := r.Header.Get("Authorization")
		if auth != "Bearer sk-user-key-12345" {
			t.Errorf("auth = %q, want 'Bearer sk-user-key-12345'", auth)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-pt",
			"model": "big-pickle",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "passthrough ok"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 3}
		}`))
	})
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.PassthroughAPIKey = true
	logger := log.New("debug", "text")
	router := newTestRouter(ts.URL, cfg.UpstreamAPIKey)
	catalog := models.NewCatalog(ts.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, logger)

	body := `{"model":"big-pickle","max_tokens":100,"messages":[{"role":"user","content":"test"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", "sk-user-key-12345")
	w := httptest.NewRecorder()

	// Apply auth middleware to simulate the full pipeline (sets passthrough key in context)
	h := auth("", cfg.PassthroughAPIKey)(http.HandlerFunc(handler.HandleMessages))
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if len(resp.Content) != 1 || resp.Content[0].Text != "passthrough ok" {
		t.Errorf("content = %v, want [{text: passthrough ok}]", resp.Content)
	}
}

func TestBuildFallbackList_DropsCodexWhenNotConfigured(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models = []config.ModelSpec{
		{Name: "big-pickle", Upstream: config.DefaultUpstreamName},
		{Name: "gpt-5.6-terra", Upstream: config.CodexUpstreamName},
		{Name: "deepseek-v4-flash-free", Upstream: config.DefaultUpstreamName},
	}
	cfg.Precompute()

	// Codex not configured (handler.codex == nil) → gpt-5.6-terra must be dropped.
	h := NewHandler(cfg, nil, nil, nil)
	list := h.buildPreferenceList(&config.ModelSpec{Name: "big-pickle", Upstream: config.DefaultUpstreamName})

	for _, m := range list {
		if m.Name == "gpt-5.6-terra" {
			t.Errorf("gpt-5.6-terra should be excluded when Codex is not configured, got %v", list)
		}
	}
	if len(list) != 2 {
		t.Errorf("list length = %d, want 2 (big-pickle, deepseek-v4-flash-free)", len(list))
	}
	if list[0].Name != "big-pickle" {
		t.Errorf("list[0] = %q, want big-pickle", list[0].Name)
	}
}

func TestBuildFallbackList_KeepsCodexWhenConfigured(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models = []config.ModelSpec{
		{Name: "big-pickle", Upstream: config.DefaultUpstreamName},
		{Name: "gpt-5.6-terra", Upstream: config.CodexUpstreamName},
		{Name: "deepseek-v4-flash-free", Upstream: config.DefaultUpstreamName},
	}
	cfg.Precompute()

	// Codex configured → gpt-5.6-terra stays in the list, in order.
	router := newTestRouter("http://example.invalid", "public")
	router.SetCodex(upstream.NewCodexClient("https://chatgpt.com/backend-api", "tok", "acct", 30*time.Second))
	h := NewHandler(cfg, nil, router, nil)
	list := h.buildPreferenceList(&config.ModelSpec{Name: "big-pickle", Upstream: config.DefaultUpstreamName})

	if len(list) != 3 {
		t.Fatalf("list length = %d, want 3", len(list))
	}
	if list[1].Name != "gpt-5.6-terra" {
		t.Errorf("list[1] = %q, want gpt-5.6-terra", list[1].Name)
	}
}

// TestHandleMessages_RoutesToCorrectUpstream verifies that a model is sent to
// the upstream named in its ModelSpec, not always the default one.
func TestHandleMessages_RoutesToCorrectUpstream(t *testing.T) {
	var opencodeGot, customGot int
	opencode := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		opencodeGot++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","model":"big-pickle","choices":[{"index":0,"message":{"role":"assistant","content":"opencode"}}]}`))
	})
	defer opencode.Close()

	custom := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		customGot++
		// Verify the custom API key is forwarded.
		if r.Header.Get("Authorization") != "Bearer custom-key" {
			t.Errorf("custom upstream auth = %q, want Bearer custom-key", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","model":"my-model","choices":[{"index":0,"message":{"role":"assistant","content":"custom"}}]}`))
	})
	defer custom.Close()

	cfg := defaultConfig(opencode.URL)
	cfg.Models = []config.ModelSpec{
		{Name: "my-model", Upstream: "custom"},
		{Name: "big-pickle", Upstream: config.DefaultUpstreamName},
	}
	cfg.Precompute()
	cfg.AllowUnlisted = true

	router := upstream.NewRouter([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: opencode.URL, APIKey: cfg.UpstreamAPIKey},
		{Name: "custom", BaseURL: custom.URL, APIKey: "custom-key"},
	}, nil, 30*time.Second)
	catalog := models.NewCatalog(opencode.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, log.New("debug", "text"))

	body := `{"model":"my-model","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if customGot != 1 || opencodeGot != 0 {
		t.Errorf("expected request routed to custom upstream only (custom=%d, opencode=%d)", customGot, opencodeGot)
	}
}

// TestHandleMessages_FallbackAcrossUpstreams verifies that when the primary
// model's upstream fails, the proxy tries the next model on a different upstream.
func TestHandleMessages_FallbackAcrossUpstreams(t *testing.T) {
	opencode := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","model":"big-pickle","choices":[{"index":0,"message":{"role":"assistant","content":"fallback-ok"}}]}`))
	})
	defer opencode.Close()

	custom := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"bad gateway"}}`))
	})
	defer custom.Close()

	cfg := defaultConfig(opencode.URL)
	cfg.Models = []config.ModelSpec{
		{Name: "primary-model", Upstream: "custom"},
		{Name: "big-pickle", Upstream: config.DefaultUpstreamName},
	}
	cfg.Precompute()
	cfg.AllowUnlisted = true

	router := upstream.NewRouter([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: opencode.URL, APIKey: cfg.UpstreamAPIKey},
		{Name: "custom", BaseURL: custom.URL, APIKey: "custom-key"},
	}, nil, 30*time.Second)
	catalog := models.NewCatalog(opencode.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, log.New("debug", "text"))

	body := `{"model":"primary-model","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "fallback-ok") {
		t.Errorf("expected fallback response, got: %s", w.Body.String())
	}
}
