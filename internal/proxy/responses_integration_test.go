package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/claude-code-opencode/claude-proxy/internal/config"
	"github.com/claude-code-opencode/claude-proxy/internal/models"
)

func TestMuseResponsesNonStreaming(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %q, want /responses", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("missing Zen authorization")
		}
		var body struct {
			Model           string `json:"model"`
			Stream          bool   `json:"stream"`
			MaxOutputTokens int    `json:"max_output_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != config.OpenCodeMuseSparkContributorFree || body.Stream || body.MaxOutputTokens != 50 {
			t.Fatalf("unexpected request: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	handler, cfg := newTestHandler(upstream.URL)
	cfg.Models = []config.ModelSpec{{Name: config.OpenCodeMuseSparkContributorFree, Upstream: config.DefaultUpstreamName}}
	cfg.Precompute()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"custom","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`))
	w := httptest.NewRecorder()
	handler.HandleMessages(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"text":"ok"`) {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}

func TestDiscoveredZenModelsInitializeDefaultRouting(t *testing.T) {
	handler, _ := newTestHandler("http://example.invalid")
	cfg := config.DefaultConfig()
	cfg.ZenAPIKey = "key"
	handler.UpdateConfig(cfg)
	handler.ApplyDiscoveredOpenCodeModels([]models.ModelEntry{
		{ID: "claude-future"},
		{ID: config.OpenCodeMuseSparkContributorFree},
		{ID: "future-contributor-free"},
		{ID: "future-free"},
		{ID: "big-pickle"},
	})
	effective := handler.GetConfig().EffectiveModels()
	if len(effective) != 2 || effective[0].Name != "future-free" || effective[1].Name != "big-pickle" {
		t.Fatalf("discovered routing = %+v", effective)
	}
}

func TestMuseResponsesStreaming(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %q, want /responses", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\n" +
			`data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"hello"}` + "\n\n" +
			"event: response.completed\n" +
			`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":2,"output_tokens":1}}}` + "\n\n"))
	}))
	defer upstream.Close()

	handler, cfg := newTestHandler(upstream.URL)
	cfg.Models = []config.ModelSpec{{Name: config.OpenCodeMuseSparkContributorFree, Upstream: config.DefaultUpstreamName}}
	cfg.Precompute()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"custom","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	w := httptest.NewRecorder()
	handler.HandleMessages(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"text":"hello"`) || !strings.Contains(w.Body.String(), `"output_tokens":1`) {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}
