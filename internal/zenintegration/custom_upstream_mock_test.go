package zenintegration

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
	"github.com/claude-code-opencode/claude-proxy/internal/proxy"
)

// TestCustomUpstream_MockOpenAICompatible teste la suite complète contre un mock
// qui simule un upstream OpenAI-compatible standard. Ce test ne nécessite AUCUNE
// clé API externe et valide que le framework de test fonctionne correctement.
func TestCustomUpstream_MockOpenAICompatible(t *testing.T) {
	var receivedRequests []*http.Request

	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedRequests = append(receivedRequests, r)

		switch r.URL.Path {
		case "/chat/completions":
			var reqBody map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			_ = r.Body.Close()

			model := "unknown"
			if m, ok := reqBody["model"].(string); ok {
				model = m
			}
			stream := false
			if s, ok := reqBody["stream"].(bool); ok {
				stream = s
			}

			if stream {
				handleStreamingResponse(w, model)
			} else {
				handleNonStreamingResponse(w, model)
			}

		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []interface{}{
					map[string]string{"id": "mock-model-1", "object": "model"},
					map[string]string{"id": "mock-model-2", "object": "model"},
				},
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockUpstream.Close()

	// Configuration du test
	tc := CustomUpstreamTestCase{
		Name:    "mock-openai",
		BaseURL: mockUpstream.URL,
		APIKey:  "mock-key",
		Models: []config.ModelSpec{
			{Name: "mock-model-1", Upstream: "mock-openai"},
			{Name: "mock-model-2", Upstream: "mock-openai"},
		},
		SupportsStream: true,
		SupportsTools:  true,
		SkipIfNoKey:    false,
	}

	cfg := buildTestConfig(tc, mockUpstream.URL)
	cfg.Models = tc.Models
	cfg.Precompute()

	logger := log.New("debug", "text")
	router := providers.NewRegistry([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: mockUpstream.URL, APIKey: "test-key"},
		{Name: "mock-openai", BaseURL: mockUpstream.URL, APIKey: tc.APIKey},
	}, nil, 30*time.Second)
	catalog := models.NewCatalog(mockUpstream.URL, "test-key", 5*time.Minute)
	handler := proxy.NewHandler(cfg, catalog, router, logger)

	t.Run("NonStreaming", func(t *testing.T) {
		body := `{"model":"custom","max_tokens":100,"messages":[{"role":"user","content":"Hello"}]}`
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("anthropic-version", "2023-06-01")
		w := httptest.NewRecorder()

		handler.HandleMessages(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "Mock response") {
			t.Errorf("unexpected response: %s", w.Body.String())
		}

		// Vérifier auth header
		if len(receivedRequests) > 0 {
			lastReq := receivedRequests[len(receivedRequests)-1]
			auth := lastReq.Header.Get("Authorization")
			if auth != "Bearer mock-key" {
				t.Errorf("Authorization = %q, want Bearer mock-key", auth)
			}
		}
	})

	t.Run("Streaming", func(t *testing.T) {
		body := `{"model":"custom","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"Stream test"}]}`
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handler.HandleMessages(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
			t.Errorf("Content-Type = %q, want text/event-stream", w.Header().Get("Content-Type"))
		}
		bodyStr := w.Body.String()
		for _, event := range []string{"message_start", "content_block_delta", "message_stop"} {
			if !strings.Contains(bodyStr, event) {
				t.Errorf("missing event %q", event)
			}
		}
	})

	t.Run("Tools", func(t *testing.T) {
		body := `{
			"model": "custom",
			"max_tokens": 100,
			"messages": [{"role": "user", "content": "Use a tool"}],
			"tools": [{"name": "test_tool", "description": "A test tool", "input_schema": {"type": "object"}}]
		}`
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handler.HandleMessages(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		// Le mock ne renvoie pas de tool_use, mais la requête doit passer
	})

	t.Run("Fallback", func(t *testing.T) {
		// Reset mock to fail on first model, succeed on second
		callCount := 0
		mockUpstream2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			var reqBody map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			model := reqBody["model"].(string)

			if model == "mock-model-1" && callCount == 1 {
				w.WriteHeader(http.StatusBadGateway)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]string{"message": "upstream error", "type": "server_error"},
				})
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "chatcmpl-fb", "model": model,
				"choices": []interface{}{map[string]interface{}{
					"index": 0, "message": map[string]interface{}{
						"role": "assistant", "content": "fallback worked"},
					"finish_reason": "stop",
				}},
				"usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 10},
			})
		}))
		defer mockUpstream2.Close()

		cfg2 := buildTestConfig(tc, mockUpstream2.URL)
		cfg2.Models = tc.Models
		cfg2.Precompute()
		router2 := providers.NewRegistry([]config.UpstreamConfig{
			{Name: "mock-openai", BaseURL: mockUpstream2.URL, APIKey: tc.APIKey},
		}, nil, 30*time.Second)
		catalog2 := models.NewCatalog(mockUpstream2.URL, "test-key", 5*time.Minute)
		handler2 := proxy.NewHandler(cfg2, catalog2, router2, logger)

		body := `{"model":"custom","max_tokens":100,"messages":[{"role":"user","content":"test"}]}`
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handler2.HandleMessages(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "fallback worked") {
			t.Errorf("expected fallback: %s", w.Body.String())
		}
	})
}

// handleNonStreamingResponse renvoie une réponse OpenAI chat completions standard.
func handleNonStreamingResponse(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]interface{}{
		"id":    "chatcmpl-mock",
		"model": model,
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "Mock response for " + model,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// handleStreamingResponse renvoie une réponse SSE OpenAI standard.
func handleStreamingResponse(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)

	chunks := []string{
		fmt.Sprintf(`data: {"id":"chatcmpl-stream","model":"%s","choices":[{"index":0,"delta":{"role":"assistant"}}]}`, model),
		fmt.Sprintf(`data: {"id":"chatcmpl-stream","model":"%s","choices":[{"index":0,"delta":{"content":"Streaming "}}]}`, model),
		fmt.Sprintf(`data: {"id":"chatcmpl-stream","model":"%s","choices":[{"index":0,"delta":{"content":"works"},"finish_reason":"stop"}]}`, model),
		`data: [DONE]`,
	}
	for _, chunk := range chunks {
		_, _ = fmt.Fprintf(w, "%s\n\n", chunk)
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(5 * time.Millisecond)
	}
}
