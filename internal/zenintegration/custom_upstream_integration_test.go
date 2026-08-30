package zenintegration

import (
	"encoding/json"
	"fmt"
	"io"
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

// CustomUpstreamTestCase définit un cas de test pour un upstream custom.
type CustomUpstreamTestCase struct {
	Name           string
	BaseURL        string
	APIKey         string
	Models         []config.ModelSpec
	RequestHeaders map[string]string // Headers additionnels attendus
	ResponseFormat string            // "openai" | "azure" | "custom"
	SupportsStream bool
	SupportsTools  bool
	SkipIfNoKey    bool
}

// RunCustomUpstreamTests exécute une suite de tests standard contre un upstream custom.
// Utilisation dans un test réel :
//
//	func TestOpenRouterIntegration(t *testing.T) {
//	    if testing.Short() { t.Skip("skipping integration test") }
//	    RunCustomUpstreamTests(t, CustomUpstreamTestCase{
//	        Name:        "openrouter",
//	        BaseURL:     "https://openrouter.ai/api/v1",
//	        APIKey:      os.Getenv("OPENROUTER_API_KEY"),
//	        Models:      []config.ModelSpec{{Name: "anthropic/claude-3.5-sonnet", Upstream: "openrouter"}},
//	        SupportsStream: true,
//	        SupportsTools:  true,
//	        SkipIfNoKey:    true,
//	    })
//	}
func RunCustomUpstreamTests(t *testing.T, tc CustomUpstreamTestCase) {
	if tc.SkipIfNoKey && tc.APIKey == "" {
		t.Skip("API key not set, skipping integration test")
	}

	t.Run("NonStreaming_BasicText", func(t *testing.T) {
		testNonStreaming(t, tc)
	})

	t.Run("Streaming_BasicText", func(t *testing.T) {
		if !tc.SupportsStream {
			t.Skip("upstream does not support streaming")
		}
		testStreaming(t, tc)
	})

	t.Run("Tools_FunctionCalling", func(t *testing.T) {
		if !tc.SupportsTools {
			t.Skip("upstream does not support tools")
		}
		testTools(t, tc)
	})

	t.Run("SystemPrompt", func(t *testing.T) {
		testSystemPrompt(t, tc)
	})

	t.Run("FallbackToNextModel", func(t *testing.T) {
		testFallback(t, tc)
	})

	t.Run("RateLimitHandling", func(t *testing.T) {
		testRateLimit(t, tc)
	})

	t.Run("ModelUnavailableHandling", func(t *testing.T) {
		testModelUnavailable(t, tc)
	})

	t.Run("RequestHeadersForwarded", func(t *testing.T) {
		testHeadersForwarded(t, tc)
	})
}

// testNonStreaming teste une requête non-streaming simple.
func testNonStreaming(t *testing.T, tc CustomUpstreamTestCase) {
	var receivedReq *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedReq = r
		w.Header().Set("Content-Type", "application/json")
		resp := openAIChatCompletionResponse(tc.Models[0].Name, "Hello from "+tc.Name)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer upstream.Close()

	cfg := buildTestConfig(tc, upstream.URL)
	handler := buildTestHandler(tc, cfg, upstream.URL)

	body := `{"model":"custom","max_tokens":100,"messages":[{"role":"user","content":"Say hello"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Hello from "+tc.Name) {
		t.Errorf("response missing expected content: %s", w.Body.String())
	}

	// Vérifier les headers transmis
	if receivedReq != nil {
		auth := receivedReq.Header.Get("Authorization")
		if auth != "Bearer "+tc.APIKey {
			t.Errorf("Authorization header = %q, want Bearer %s", auth, tc.APIKey)
		}
		for k, v := range tc.RequestHeaders {
			if receivedReq.Header.Get(k) != v {
				t.Errorf("Header %s = %q, want %q", k, receivedReq.Header.Get(k), v)
			}
		}
	}
}

// testStreaming teste le streaming SSE.
func testStreaming(t *testing.T, tc CustomUpstreamTestCase) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		chunks := []string{
			`data: {"id":"chatcmpl-stream","model":"` + tc.Models[0].Name + `","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			`data: {"id":"chatcmpl-stream","model":"` + tc.Models[0].Name + `","choices":[{"index":0,"delta":{"content":"Streaming "}}]}`,
			`data: {"id":"chatcmpl-stream","model":"` + tc.Models[0].Name + `","choices":[{"index":0,"delta":{"content":"works"},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "%s\n\n", chunk)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer upstream.Close()

	cfg := buildTestConfig(tc, upstream.URL)
	handler := buildTestHandler(tc, cfg, upstream.URL)

	body := `{"model":"custom","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"test"}]}`
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
	for _, expected := range []string{"message_start", "content_block_delta", "message_stop"} {
		if !strings.Contains(bodyStr, expected) {
			t.Errorf("missing SSE event %q in: %s", expected, bodyStr)
		}
	}
}

// testTools teste l'appel de fonctions.
func testTools(t *testing.T, tc CustomUpstreamTestCase) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)

		// Vérifier que les tools sont envoyés
		if _, ok := body["tools"]; !ok {
			t.Logf("WARNING: upstream request missing 'tools' field: %v", body)
		}

		w.Header().Set("Content-Type", "application/json")
		resp := openAIChatCompletionResponseWithTools(tc.Models[0].Name, "get_weather", `{"location":"Paris"}`)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer upstream.Close()

	cfg := buildTestConfig(tc, upstream.URL)
	handler := buildTestHandler(tc, cfg, upstream.URL)

	body := `{
		"model": "custom",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": "What's the weather?"}],
		"tools": [{"name": "get_weather", "description": "Get weather", "input_schema": {"type": "object"}}]
	}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "tool_use") && !strings.Contains(w.Body.String(), "get_weather") {
		t.Errorf("expected tool_use response, got: %s", w.Body.String())
	}
}

// testSystemPrompt teste la transmission du system prompt.
func testSystemPrompt(t *testing.T, tc CustomUpstreamTestCase) {
	var receivedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		receivedBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		resp := openAIChatCompletionResponse(tc.Models[0].Name, "System prompt received")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer upstream.Close()

	cfg := buildTestConfig(tc, upstream.URL)
	handler := buildTestHandler(tc, cfg, upstream.URL)

	body := `{"model":"custom","max_tokens":100,"system":"You are a test bot","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	// Vérifier que le system prompt a été transmis (dans le premier message user pour OpenAI)
	if receivedBody != "" {
		var reqBody map[string]interface{}
		_ = json.NewDecoder(strings.NewReader(receivedBody)).Decode(&reqBody)
		if msgs, ok := reqBody["messages"].([]interface{}); ok && len(msgs) > 0 {
			firstMsg := msgs[0].(map[string]interface{})
			if content, ok := firstMsg["content"].(string); ok {
				if !strings.Contains(content, "You are a test bot") {
					t.Errorf("system prompt not found in upstream request: %v", firstMsg)
				}
			}
		}
	}
}

// testFallback teste le fallback vers le modèle suivant.
func testFallback(t *testing.T, tc CustomUpstreamTestCase) {
	if len(tc.Models) < 2 {
		t.Skip("need at least 2 models for fallback test")
	}

	primaryCalls := 0
	fallbackCalls := 0

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		model := body["model"].(string)

		if model == tc.Models[0].Name {
			primaryCalls++
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"message": "upstream error", "type": "server_error"},
			})
			return
		}

		fallbackCalls++
		w.Header().Set("Content-Type", "application/json")
		resp := openAIChatCompletionResponse(model, "fallback worked")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer upstream.Close()

	cfg := buildTestConfig(tc, upstream.URL)
	cfg.Models = tc.Models
	cfg.Precompute()
	handler := buildTestHandler(tc, cfg, upstream.URL)

	body := `{"model":"custom","max_tokens":100,"messages":[{"role":"user","content":"test"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "fallback worked") {
		t.Errorf("expected fallback response: %s", w.Body.String())
	}
	if primaryCalls != 1 || fallbackCalls != 1 {
		t.Errorf("primary=%d fallback=%d, want 1/1", primaryCalls, fallbackCalls)
	}
}

// testRateLimit teste la gestion du rate limit (429).
func testRateLimit(t *testing.T, tc CustomUpstreamTestCase) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"message": "rate limited", "type": "rate_limit_error"},
		})
	}))
	defer upstream.Close()

	cfg := buildTestConfig(tc, upstream.URL)
	cfg.Models = []config.ModelSpec{{Name: tc.Models[0].Name, Upstream: tc.Models[0].Upstream}}
	cfg.OnlyPreferredModels = true // Prevent discovered models from being added as fallbacks
	cfg.Precompute()
	handler := buildTestHandler(tc, cfg, upstream.URL)

	body := `{"model":"custom","max_tokens":100,"messages":[{"role":"user","content":"test"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	// Doit retourner 429 avec Retry-After
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") != "1" {
		t.Errorf("Retry-After = %q, want 1", w.Header().Get("Retry-After"))
	}
}

// testModelUnavailable teste la gestion modèle indisponible (400 model_not_available).
func testModelUnavailable(t *testing.T, tc CustomUpstreamTestCase) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"message": "Model is unavailable", "type": "server_error", "code": "model_not_available"},
		})
	}))
	defer upstream.Close()

	cfg := buildTestConfig(tc, upstream.URL)
	cfg.Models = []config.ModelSpec{{Name: tc.Models[0].Name, Upstream: tc.Models[0].Upstream}}
	cfg.OnlyPreferredModels = true // Prevent discovered models from being added as fallbacks
	cfg.Precompute()
	handler := buildTestHandler(tc, cfg, upstream.URL)

	body := `{"model":"custom","max_tokens":100,"messages":[{"role":"user","content":"test"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	// Doit marquer le modèle unavailable et retourner erreur (pas de fallback configuré)
	if w.Code == http.StatusOK {
		t.Errorf("expected error for unavailable model, got 200")
	}
	if !handler.IsModelUpstreamUnavailable(tc.Models[0].Upstream, tc.Models[0].Name) {
		t.Error("model should be marked unavailable")
	}
}

// testHeadersForwarded teste que les headers custom sont transmis.
func testHeadersForwarded(t *testing.T, tc CustomUpstreamTestCase) {
	if len(tc.RequestHeaders) == 0 {
		t.Skip("no custom headers to test")
	}

	var receivedReq *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedReq = r
		w.Header().Set("Content-Type", "application/json")
		resp := openAIChatCompletionResponse(tc.Models[0].Name, "ok")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer upstream.Close()

	cfg := buildTestConfig(tc, upstream.URL)
	handler := buildTestHandler(tc, cfg, upstream.URL)

	body := `{"model":"custom","max_tokens":100,"messages":[{"role":"user","content":"test"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if receivedReq != nil {
		for k, v := range tc.RequestHeaders {
			if receivedReq.Header.Get(k) != v {
				t.Errorf("Custom header %s = %q, want %q", k, receivedReq.Header.Get(k), v)
			}
		}
	}
}

// ─── Helpers ────────────────────────────────────────────────────────────────

func buildTestConfig(tc CustomUpstreamTestCase, upstreamURL string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.ZenBaseURL = upstreamURL
	cfg.ZenAPIKey = "test-key" // Pour le fallback zen
	cfg.AllowUnlisted = true
	cfg.Models = tc.Models
	cfg.Precompute()
	return cfg
}

func buildTestHandler(tc CustomUpstreamTestCase, cfg *config.Config, upstreamURL string) *proxy.Handler {
	logger := log.New("debug", "text")
	router := providers.NewRegistry([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: upstreamURL, APIKey: "test-key"},
		{Name: tc.Models[0].Upstream, BaseURL: upstreamURL, APIKey: tc.APIKey},
	}, nil, 30*time.Second)
	catalog := models.NewCatalog(upstreamURL, "test-key", 5*time.Minute)
	return proxy.NewHandler(cfg, catalog, router, logger)
}

func openAIChatCompletionResponse(model, content string) map[string]interface{} {
	return map[string]interface{}{
		"id":    "chatcmpl-test",
		"model": model,
		"choices": []interface{}{map[string]interface{}{
			"index": 0,
			"message": map[string]interface{}{
				"role":    "assistant",
				"content": content,
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30},
	}
}

func openAIChatCompletionResponseWithTools(model, toolName, toolArgs string) map[string]interface{} {
	return map[string]interface{}{
		"id":    "chatcmpl-tools",
		"model": model,
		"choices": []interface{}{map[string]interface{}{
			"index": 0,
			"message": map[string]interface{}{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []interface{}{map[string]interface{}{
					"id":   "call_123",
					"type": "function",
					"function": map[string]interface{}{
						"name":      toolName,
						"arguments": toolArgs,
					},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]int{"prompt_tokens": 15, "completion_tokens": 25, "total_tokens": 40},
	}
}
