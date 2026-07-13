package proxy

import (
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

	router := upstream.NewRouter([]config.UpstreamConfig{
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
	handler.router = upstream.NewRouter([]config.UpstreamConfig{{Name: config.DefaultUpstreamName, BaseURL: url, APIKey: "test-key"}}, nil, time.Second)

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
