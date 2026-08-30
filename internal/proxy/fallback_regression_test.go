package proxy

import (
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
	"github.com/claude-code-opencode/claude-proxy/internal/providers"
)

func TestHandleMessages_TransportErrorFallsBack(t *testing.T) {
	unavailable := httptest.NewServer(http.NotFoundHandler())
	unavailableURL := unavailable.URL
	unavailable.Close()

	fallback := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fallback","model":"fallback","choices":[{"index":0,"message":{"role":"assistant","content":"transport fallback"},"finish_reason":"stop"}]}`))
	})
	defer fallback.Close()

	cfg := defaultConfig(fallback.URL)
	cfg.Models = []config.ModelSpec{
		{Name: "unavailable", Upstream: "primary"},
		{Name: "fallback", Upstream: config.DefaultUpstreamName},
	}
	cfg.AllowUnlisted = true
	cfg.Precompute()

	router := providers.NewRegistry([]config.UpstreamConfig{
		{Name: "primary", BaseURL: unavailableURL, APIKey: "test-key"},
		{Name: config.DefaultUpstreamName, BaseURL: fallback.URL, APIKey: "test-key"},
	}, nil, time.Second)
	handler := NewHandler(cfg, models.NewCatalog(fallback.URL, "test-key", time.Minute), router, log.New("error", "text"))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"custom","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`))
	w := httptest.NewRecorder()
	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "transport fallback") {
		t.Errorf("expected fallback response, got %s", w.Body.String())
	}
}

func TestHandleMessages_AllTransportFailuresReturnError(t *testing.T) {
	unavailable := httptest.NewServer(http.NotFoundHandler())
	url := unavailable.URL
	unavailable.Close()

	cfg := defaultConfig(url)
	cfg.Models = []config.ModelSpec{{Name: "unavailable", Upstream: config.DefaultUpstreamName}}
	cfg.AllowUnlisted = true
	cfg.Precompute()
	handler, _ := newTestHandler(url)
	handler.cfg.Store(cfg)
	handler.replaceRouter(providers.NewRegistry([]config.UpstreamConfig{{Name: config.DefaultUpstreamName, BaseURL: url, APIKey: "test-key"}}, nil, time.Second))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"custom","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`))
	w := httptest.NewRecorder()
	handler.HandleMessages(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "api_error") {
		t.Errorf("expected Anthropic api_error, got %s", w.Body.String())
	}
}

func TestHandleMessages_CodexNamedRequestUsesConfiguredPriority(t *testing.T) {
	calls := 0
	primary := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"primary","model":"big-pickle","choices":[{"index":0,"message":{"role":"assistant","content":"configured priority"},"finish_reason":"stop"}]}`))
	})
	defer primary.Close()

	cfg := defaultConfig(primary.URL)
	cfg.Models = []config.ModelSpec{
		{Name: "big-pickle", Upstream: config.DefaultUpstreamName},
		{Name: "gpt-5.6-terra", Upstream: config.CodexUpstreamName},
	}
	cfg.AllowUnlisted = true
	cfg.Precompute()
	handler, _ := newTestHandler(primary.URL)
	handler.cfg.Store(cfg)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.6-terra","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`))
	w := httptest.NewRecorder()
	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if calls != 1 || !strings.Contains(w.Body.String(), "configured priority") {
		t.Errorf("expected configured primary only, calls=%d body=%s", calls, w.Body.String())
	}
}

func TestHandleMessages_ClientErrorFallsBack(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		if body.Model == "broken" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"server_error","message":"Upstream request failed: Model is unavailable."}}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"working","model":"working","choices":[{"index":0,"message":{"role":"assistant","content":"works despite 400"},"finish_reason":"stop"}]}`))
	})
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.Models = []config.ModelSpec{
		{Name: "broken", Upstream: "primary"},
		{Name: "working", Upstream: config.DefaultUpstreamName},
	}
	cfg.AllowUnlisted = true
	cfg.Precompute()

	router := providers.NewRegistry([]config.UpstreamConfig{
		{Name: "primary", BaseURL: ts.URL, APIKey: "test-key"},
		{Name: config.DefaultUpstreamName, BaseURL: ts.URL, APIKey: "test-key"},
	}, nil, time.Second)
	handler := NewHandler(cfg, models.NewCatalog(ts.URL, "test-key", time.Minute), router, log.New("error", "text"))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"custom","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`))
	w := httptest.NewRecorder()
	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "works despite 400") {
		t.Errorf("expected fallback response, got %s", w.Body.String())
	}
}

func TestHandleMessages_StreamingClientErrorFallsBack(t *testing.T) {
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		if body.Model == "broken" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"server_error","message":"Upstream request failed: Model is unavailable."}}`))
			return
		}

		// Fallback model returns streaming SSE
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"id":"chatcmpl-fb","model":"working","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			`{"id":"chatcmpl-fb","model":"working","choices":[{"index":0,"delta":{"content":"works despite 400"}}]}`,
			`{"id":"chatcmpl-fb","model":"working","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
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
	cfg.Models = []config.ModelSpec{
		{Name: "broken", Upstream: "primary"},
		{Name: "working", Upstream: config.DefaultUpstreamName},
	}
	cfg.AllowUnlisted = true
	cfg.Precompute()

	router := providers.NewRegistry([]config.UpstreamConfig{
		{Name: "primary", BaseURL: ts.URL, APIKey: "test-key"},
		{Name: config.DefaultUpstreamName, BaseURL: ts.URL, APIKey: "test-key"},
	}, nil, time.Second)
	handler := NewHandler(cfg, models.NewCatalog(ts.URL, "test-key", time.Minute), router, log.New("debug", "text"))

	body := `{"model":"custom","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	w := httptest.NewRecorder()
	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "message_stop") {
		t.Errorf("expected streamed fallback response, got %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"text":"works despite 400"`) {
		t.Errorf("missing fallback text delta, got %s", w.Body.String())
	}
}

// TestHandleMessages_UnavailableModelSkippedAndHidden verifies that a model
// the upstream no longer serves (400 "Model is unavailable") is skipped on
// subsequent requests and hidden from the GET /v1/models listing.
func TestHandleMessages_UnavailableModelSkippedAndHidden(t *testing.T) {
	workingCalls := 0
	brokenCalls := 0
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		if body.Model == "broken" {
			brokenCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"server_error","message":"Upstream request failed: Model is unavailable."}}`))
			return
		}

		workingCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"working","model":"working","choices":[{"index":0,"message":{"role":"assistant","content":"works despite unavailable"},"finish_reason":"stop"}]}`))
	})
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.Models = []config.ModelSpec{
		{Name: "broken", Upstream: "primary"},
		{Name: "working", Upstream: config.DefaultUpstreamName},
	}
	cfg.AllowUnlisted = true
	cfg.Precompute()

	router := providers.NewRegistry([]config.UpstreamConfig{
		{Name: "primary", BaseURL: ts.URL, APIKey: "test-key"},
		{Name: config.DefaultUpstreamName, BaseURL: ts.URL, APIKey: "test-key"},
	}, nil, time.Second)
	handler := NewHandler(cfg, models.NewCatalog(ts.URL, "test-key", time.Minute), router, log.New("error", "text"))

	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"custom","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`))
		w := httptest.NewRecorder()
		handler.HandleMessages(w, req)
		return w
	}

	// First request: broken → 400 unavailable → falls back to working.
	w := send()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "works despite unavailable") {
		t.Errorf("expected fallback response, got %s", w.Body.String())
	}
	if brokenCalls != 1 || workingCalls != 1 {
		t.Fatalf("first request: broken=%d working=%d, want 1/1", brokenCalls, workingCalls)
	}
	if !handler.IsModelUpstreamUnavailable("primary", "broken") {
		t.Fatal("broken should be marked unavailable after the 400")
	}

	// Second request: broken is skipped entirely.
	w = send()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if brokenCalls != 1 || workingCalls != 2 {
		t.Errorf("second request: broken=%d working=%d, want 1/2", brokenCalls, workingCalls)
	}

	// The model listing hides the unavailable model.
	modelsReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	mw := httptest.NewRecorder()
	handler.HandleModels(mw, modelsReq)
	if mw.Code != http.StatusOK {
		t.Fatalf("models status = %d", mw.Code)
	}
	bodyOut := mw.Body.String()
	if strings.Contains(bodyOut, "broken") {
		t.Errorf("models listing should hide unavailable model, got: %s", bodyOut)
	}
	if !strings.Contains(bodyOut, "working") {
		t.Errorf("models listing should still show working model, got: %s", bodyOut)
	}
}

func TestHandleMessages_StreamingGlobalRateLimitSkipsUpstream(t *testing.T) {
	zenCalls := map[string]int{}
	fallbackCalls := 0
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		if body.Model == "zen-one" || body.Model == "zen-two" {
			zenCalls[body.Model]++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			// usage_limit_reached is the truly global rate limit (works on any upstream)
			_, _ = w.Write([]byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`))
			return
		}

		fallbackCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", `{"id":"fallback","model":"working","choices":[{"index":0,"delta":{"content":"fallback"},"finish_reason":"stop"}]}`)
	})
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.Models = []config.ModelSpec{
		{Name: "zen-one", Upstream: config.DefaultUpstreamName},
		{Name: "zen-two", Upstream: config.DefaultUpstreamName},
		{Name: "working", Upstream: "fallback"},
	}
	cfg.AllowUnlisted = true
	cfg.Precompute()

	router := providers.NewRegistry([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: ts.URL, APIKey: "test-key"},
		{Name: "fallback", BaseURL: ts.URL, APIKey: "test-key"},
	}, nil, time.Second)
	handler := NewHandler(cfg, models.NewCatalog(ts.URL, "test-key", time.Minute), router, log.New("error", "text"))

	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"custom","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
		w := httptest.NewRecorder()
		handler.HandleMessages(w, req)
		return w
	}

	w := send()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if zenCalls["zen-one"] != 1 || zenCalls["zen-two"] != 0 || fallbackCalls != 1 {
		t.Fatalf("calls after first request: zen-one=%d zen-two=%d fallback=%d, want 1/0/1", zenCalls["zen-one"], zenCalls["zen-two"], fallbackCalls)
	}
	if !handler.isModelUpstreamDisabled(config.DefaultUpstreamName, "zen-two") {
		t.Fatal("global rate limit should disable the remaining models on that upstream")
	}

	w = send()
	if w.Code != http.StatusOK {
		t.Fatalf("second status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if zenCalls["zen-one"] != 1 || zenCalls["zen-two"] != 0 || fallbackCalls != 2 {
		t.Fatalf("calls after second request: zen-one=%d zen-two=%d fallback=%d, want 1/0/2", zenCalls["zen-one"], zenCalls["zen-two"], fallbackCalls)
	}
}

// TestHandleMessages_StreamingFirst429DisablesModel verifies that the first
// per-model 429 on the streaming path disables the model for the short cooldown
// (mirroring handleNonStream), so a subsequent request skips it and reaches the
// fallback immediately instead of re-hitting the rate limit.
func TestHandleMessages_StreamingFirst429DisablesModel(t *testing.T) {
	zenCalls := 0
	fallbackCalls := 0
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		if body.Model == "zen-one" {
			zenCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"per-model quota"}}`))
			return
		}

		fallbackCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", `{"id":"fallback","model":"working","choices":[{"index":0,"delta":{"content":"fallback"},"finish_reason":"stop"}]}`)
	})
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.Models = []config.ModelSpec{
		{Name: "zen-one", Upstream: config.DefaultUpstreamName},
		{Name: "working", Upstream: "fallback"},
	}
	cfg.AllowUnlisted = true
	cfg.Precompute()

	router := providers.NewRegistry([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: ts.URL, APIKey: "test-key"},
		{Name: "fallback", BaseURL: ts.URL, APIKey: "test-key"},
	}, nil, time.Second)
	handler := NewHandler(cfg, models.NewCatalog(ts.URL, "test-key", time.Minute), router, log.New("error", "text"))

	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"custom","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
		w := httptest.NewRecorder()
		handler.HandleMessages(w, req)
		return w
	}

	// First request: zen-one 429s (first, per-model) then falls back to working.
	w := send()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if zenCalls != 1 || fallbackCalls != 1 {
		t.Fatalf("first request: zen-one=%d fallback=%d, want 1/1", zenCalls, fallbackCalls)
	}
	// The first 429 must have short-disabled the model for subsequent requests.
	if !handler.isModelUpstreamDisabled(config.DefaultUpstreamName, "zen-one") {
		t.Fatal("expected zen-one to be disabled after the first streaming 429")
	}

	// Second request: zen-one is skipped, fallback serves directly.
	w = send()
	if w.Code != http.StatusOK {
		t.Fatalf("second status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if zenCalls != 1 || fallbackCalls != 2 {
		t.Fatalf("second request: zen-one=%d fallback=%d, want 1/2 (model must be skipped)", zenCalls, fallbackCalls)
	}
}

// TestHandleMessages_StreamingSecond429EscalatesToLongCooldown verifies that on
// the streaming path a second consecutive 429 escalates the model to the long
// circuit-breaker blacklist (rate_limit_escalated), matching handleNonStream.
// The retry counter is seeded to 1 so this request is deterministically the 2nd
// consecutive 429 without racing the clock between requests.
func TestHandleMessages_StreamingSecond429EscalatesToLongCooldown(t *testing.T) {
	zenCalls := 0
	fallbackCalls := 0
	ts := newMockUpstream(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		if body.Model == "zen-one" {
			zenCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"per-model quota"}}`))
			return
		}

		fallbackCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", `{"id":"fallback","model":"working","choices":[{"index":0,"delta":{"content":"fallback"},"finish_reason":"stop"}]}`)
	})
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.Models = []config.ModelSpec{
		{Name: "zen-one", Upstream: config.DefaultUpstreamName},
		{Name: "working", Upstream: "fallback"},
	}
	cfg.AllowUnlisted = true
	cfg.Precompute()

	router := providers.NewRegistry([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: ts.URL, APIKey: "test-key"},
		{Name: "fallback", BaseURL: ts.URL, APIKey: "test-key"},
	}, nil, time.Second)
	handler := NewHandler(cfg, models.NewCatalog(ts.URL, "test-key", time.Minute), router, log.New("error", "text"))

	// Seed so the model is NOT disabled yet but the counter makes this the 2nd
	// consecutive 429 for the model key.
	modelKey := config.DefaultUpstreamName + "/zen-one"
	handler.disabledMu.Lock()
	handler.rateLimitRetries[modelKey] = 1
	handler.disabledMu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"custom","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	w := httptest.NewRecorder()
	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if zenCalls != 1 || fallbackCalls != 1 {
		t.Fatalf("calls: zen-one=%d fallback=%d, want 1/1", zenCalls, fallbackCalls)
	}

	handler.disabledMu.Lock()
	retries := handler.rateLimitRetries[modelKey]
	info := handler.disabledUntil[modelKey]
	handler.disabledMu.Unlock()
	if retries != 2 {
		t.Errorf("rateLimitRetries = %d, want 2 (2nd consecutive 429)", retries)
	}
	if info.reason != "rate_limit_escalated" {
		t.Errorf("reason = %q, want rate_limit_escalated", info.reason)
	}
	if duration := time.Until(info.until); duration < circuitBreakerCooldown-time.Second {
		t.Errorf("escalated cooldown = %v, want ~ %v (2nd-429 blacklist must back off long)", duration, circuitBreakerCooldown)
	}
}
