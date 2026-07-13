package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/codex"
	"github.com/claude-code-opencode/claude-proxy/internal/config"
	"github.com/claude-code-opencode/claude-proxy/internal/log"
	"github.com/claude-code-opencode/claude-proxy/internal/models"
	"github.com/claude-code-opencode/claude-proxy/internal/upstream"
)

// capturedCodexRequest stores what the mock Codex backend received.
type capturedCodexRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
}

// mockCodexBackend simulates the ChatGPT Codex backend.
type mockCodexBackend struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []capturedCodexRequest
	handler  func(w http.ResponseWriter, r *http.Request, body []byte)
}

func newMockCodexBackend() *mockCodexBackend {
	m := &mockCodexBackend{}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		_ = r.Body.Close()

		m.mu.Lock()
		m.requests = append(m.requests, capturedCodexRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			Headers: r.Header.Clone(),
			Body:    body,
		})
		h := m.handler
		m.mu.Unlock()

		if h != nil {
			h(w, r, body)
			return
		}
		// Default: valid SSE response
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.done\ndata: " + defaultCodexResponseBody() + "\n\n"))
	}))
	return m
}

func (m *mockCodexBackend) close()          { m.server.Close() }
func (m *mockCodexBackend) baseURL() string { return m.server.URL }

func (m *mockCodexBackend) requestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func defaultCodexResponseBody() string {
	return `{"type":"response.done","response":{"id":"resp_test","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":10,"output_tokens":5}}}`
}

// TestCodexViaProxy_FullFlow tests the complete proxy flow:
// Claude Code → Proxy → Mock Codex backend
// This validates the entire request transformation pipeline.
func TestCodexViaProxy_FullFlow(t *testing.T) {
	codexMock := newMockCodexBackend()
	defer codexMock.close()

	// Mock regular upstream (OpenCode) for fallback
	opencodeMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-oc","model":"big-pickle","choices":[{"index":0,"message":{"role":"assistant","content":"fallback ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`))
	}))
	defer opencodeMock.Close()

	cfg := config.DefaultConfig()
	cfg.UpstreamBaseURL = opencodeMock.URL
	cfg.UpstreamAPIKey = "test-key"
	cfg.DefaultModel = "gpt-5.6-terra"
	cfg.AllowUnlisted = true
	cfg.CodexOAuthToken = "test-codex-token"
	cfg.CodexAccountID = "test-acct-id"
	cfg.Models = []config.ModelSpec{
		{Name: "gpt-5.6-terra", Upstream: config.CodexUpstreamName},
		{Name: "big-pickle", Upstream: config.DefaultUpstreamName},
	}
	cfg.Precompute()

	logger := log.New("debug", "text")
	codexClient := upstream.NewCodexClient(codexMock.baseURL(), cfg.CodexOAuthToken, cfg.CodexAccountID, 30*time.Second)
	router := upstream.NewRouter([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: opencodeMock.URL, APIKey: cfg.UpstreamAPIKey},
	}, codexClient, 30*time.Second)
	catalog := models.NewCatalog(opencodeMock.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, logger)

	// Simulate Claude Code request
	body := `{"model":"custom","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"Say hello"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	t.Logf("Proxy response status: %d", w.Code)
	t.Logf("Proxy response body: %.200s", w.Body.String())

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the Codex backend received the request
	if codexMock.requestCount() != 1 {
		t.Errorf("Codex backend received %d requests, want 1", codexMock.requestCount())
	}

	t.Logf("✅ Full proxy flow works: Claude Code → Proxy → Codex backend")
}

// TestCodexViaProxy_FallbackOn400 verifies that when the Codex backend
// returns 400, the proxy falls back to the next model.
func TestCodexViaProxy_FallbackOn400(t *testing.T) {
	codexMock := newMockCodexBackend()
	defer codexMock.close()

	// Codex always returns 400
	codexMock.handler = func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid request: model unavailable","type":"invalid_request_error","code":"model_not_available"}}`))
	}

	// Regular upstream works (streaming SSE, since the proxy request is stream:true)
	opencodeMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		writeSSE := func(s string) {
			_, _ = w.Write([]byte(s))
			if flusher != nil {
				flusher.Flush()
			}
		}
		writeSSE(`data: {"id":"chatcmpl-fb","model":"big-pickle","choices":[{"index":0,"delta":{"role":"assistant","content":"fallback worked"},"finish_reason":null}]}` + "\n\n")
		writeSSE(`data: {"id":"chatcmpl-fb","model":"big-pickle","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n")
		writeSSE("data: [DONE]\n\n")
	}))
	defer opencodeMock.Close()

	cfg := config.DefaultConfig()
	cfg.UpstreamBaseURL = opencodeMock.URL
	cfg.UpstreamAPIKey = "test-key"
	cfg.DefaultModel = "gpt-5.6-terra"
	cfg.AllowUnlisted = true
	cfg.CodexOAuthToken = "test-token"
	cfg.CodexAccountID = "test-acct"
	cfg.Models = []config.ModelSpec{
		{Name: "gpt-5.6-terra", Upstream: config.CodexUpstreamName},
		{Name: "big-pickle", Upstream: config.DefaultUpstreamName},
	}
	cfg.Precompute()

	logger := log.New("debug", "text")
	codexClient := upstream.NewCodexClient(codexMock.baseURL(), "test-token", "test-acct", 30*time.Second)
	router := upstream.NewRouter([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: opencodeMock.URL, APIKey: cfg.UpstreamAPIKey},
	}, codexClient, 30*time.Second)
	catalog := models.NewCatalog(opencodeMock.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, logger)

	body := `{"model":"custom","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"test"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	t.Logf("Proxy response status: %d", w.Code)

	// Should succeed via fallback
	if w.Code != http.StatusOK {
		t.Errorf("Expected 200 (via fallback), got %d: %s", w.Code, w.Body.String())
	}

	if !strings.Contains(w.Body.String(), "fallback worked") {
		t.Errorf("Expected fallback response, got: %s", w.Body.String())
	}

	t.Logf("✅ Fallback works: Codex 400 → falls back to big-pickle")
}

// TestCodexViaProxy_ConcurrentRequests tests multiple concurrent requests
// through the proxy to the Codex backend (simulates Claude Code parallel agents).
func TestCodexViaProxy_ConcurrentRequests(t *testing.T) {
	codexMock := newMockCodexBackend()
	defer codexMock.close()

	var mu sync.Mutex
	concurrentCount := 0
	maxConcurrent := 0

	codexMock.handler = func(w http.ResponseWriter, r *http.Request, body []byte) {
		mu.Lock()
		concurrentCount++
		if concurrentCount > maxConcurrent {
			maxConcurrent = concurrentCount
		}
		mu.Unlock()

		// Simulate 200ms processing time
		time.Sleep(200 * time.Millisecond)

		mu.Lock()
		concurrentCount--
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.done\ndata: " + defaultCodexResponseBody() + "\n\n"))
	}

	opencodeMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","model":"bp","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer opencodeMock.Close()

	cfg := config.DefaultConfig()
	cfg.UpstreamBaseURL = opencodeMock.URL
	cfg.UpstreamAPIKey = "test-key"
	cfg.DefaultModel = "gpt-5.6-terra"
	cfg.AllowUnlisted = true
	cfg.CodexOAuthToken = "tok"
	cfg.CodexAccountID = "acct"
	cfg.Models = []config.ModelSpec{
		{Name: "gpt-5.6-terra", Upstream: config.CodexUpstreamName},
		{Name: "big-pickle", Upstream: config.DefaultUpstreamName},
	}
	cfg.Precompute()

	logger := log.New("debug", "text")
	codexClient := upstream.NewCodexClient(codexMock.baseURL(), "tok", "acct", 30*time.Second)
	router := upstream.NewRouter([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: opencodeMock.URL, APIKey: cfg.UpstreamAPIKey},
	}, codexClient, 30*time.Second)
	catalog := models.NewCatalog(opencodeMock.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, logger)

	// Send 5 concurrent requests (like Claude Code parallel subagents)
	const numRequests = 5
	var wg sync.WaitGroup
	statuses := make([]int, numRequests)

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			body := `{"model":"custom","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"test"}]}`
			req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.HandleMessages(w, req)
			statuses[idx] = w.Code
		}(i)
	}

	wg.Wait()

	t.Logf("Results: %v", statuses)
	t.Logf("Max concurrent Codex requests: %d", maxConcurrent)
	t.Logf("Total Codex requests: %d", codexMock.requestCount())

	allOK := true
	for i, s := range statuses {
		if s != http.StatusOK {
			t.Errorf("Request %d: got %d, want 200", i, s)
			allOK = false
		}
	}

	if allOK {
		t.Logf("✅ All %d concurrent requests succeeded", numRequests)
	}
}

// TestCodexViaProxy_RequestBodyFormat captures the EXACT request body
// sent to the Codex backend through the full proxy flow.
func TestCodexViaProxy_RequestBodyFormat(t *testing.T) {
	codexMock := newMockCodexBackend()
	defer codexMock.close()

	var capturedBody []byte
	codexMock.handler = func(w http.ResponseWriter, r *http.Request, body []byte) {
		capturedBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.done\ndata: " + defaultCodexResponseBody() + "\n\n"))
	}

	opencodeMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","model":"bp","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer opencodeMock.Close()

	cfg := config.DefaultConfig()
	cfg.UpstreamBaseURL = opencodeMock.URL
	cfg.UpstreamAPIKey = "test-key"
	cfg.DefaultModel = "gpt-5.6-terra"
	cfg.AllowUnlisted = true
	cfg.CodexOAuthToken = "tok"
	cfg.CodexAccountID = "acct"
	cfg.Models = []config.ModelSpec{
		{Name: "gpt-5.6-terra", Upstream: config.CodexUpstreamName},
		{Name: "big-pickle", Upstream: config.DefaultUpstreamName},
	}
	cfg.Precompute()

	logger := log.New("debug", "text")
	codexClient := upstream.NewCodexClient(codexMock.baseURL(), "tok", "acct", 30*time.Second)
	router := upstream.NewRouter([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: opencodeMock.URL, APIKey: cfg.UpstreamAPIKey},
	}, codexClient, 30*time.Second)
	catalog := models.NewCatalog(opencodeMock.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, logger)

	// Send with tools and thinking (like Claude Code does)
	body := `{
		"model": "custom",
		"max_tokens": 10000,
		"stream": true,
		"system": "You are a helpful assistant.",
		"messages": [{"role": "user", "content": "Write a function"}],
		"tools": [{"name": "write_file", "description": "Write a file", "input_schema": {"type": "object"}}],
		"thinking": {"type": "enabled", "budget_tokens": 10000}
	}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	t.Logf("=== EXACT request body sent to Codex backend ===")
	var pretty map[string]interface{}
	_ = json.Unmarshal(capturedBody, &pretty)
	prettyJSON, _ := json.MarshalIndent(pretty, "", "  ")
	t.Logf("%s", string(prettyJSON))

	// Validate it's proper Codex Responses API format
	var parsed codex.ResponsesRequest
	if err := json.Unmarshal(capturedBody, &parsed); err != nil {
		t.Fatalf("Body is not valid ResponsesRequest: %v", err)
	}

	if parsed.Model != "gpt-5.6-terra" {
		t.Errorf("model = %q, want gpt-5.6-terra", parsed.Model)
	}
	if parsed.Store != false {
		t.Errorf("store = %v, want false", parsed.Store)
	}
	if !parsed.Stream {
		t.Errorf("stream = false, want true")
	}
	if parsed.Instructions != "You are a helpful assistant." {
		t.Errorf("instructions = %q", parsed.Instructions)
	}
	if len(parsed.Input) == 0 {
		t.Error("input is empty")
	}
	if len(parsed.Tools) == 0 {
		t.Error("tools is empty")
	}
	if parsed.Reasoning == nil {
		t.Error("reasoning is nil (thinking enabled but not converted)")
	}

	t.Logf("✅ Request body is valid Codex Responses API format")
}

// TestCodexViaProxy_NonStreamingFallback tests non-streaming Codex fallback.
func TestCodexViaProxy_NonStreamingFallback(t *testing.T) {
	codexMock := newMockCodexBackend()
	defer codexMock.close()

	codexMock.handler = func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request","type":"invalid_request_error"}}`))
	}

	opencodeMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","model":"bp","choices":[{"index":0,"message":{"role":"assistant","content":"non-stream fallback"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`))
	}))
	defer opencodeMock.Close()

	cfg := config.DefaultConfig()
	cfg.UpstreamBaseURL = opencodeMock.URL
	cfg.UpstreamAPIKey = "test-key"
	cfg.DefaultModel = "gpt-5.6-terra"
	cfg.AllowUnlisted = true
	cfg.CodexOAuthToken = "tok"
	cfg.CodexAccountID = "acct"
	cfg.Models = []config.ModelSpec{
		{Name: "gpt-5.6-terra", Upstream: config.CodexUpstreamName},
		{Name: "big-pickle", Upstream: config.DefaultUpstreamName},
	}
	cfg.Precompute()

	logger := log.New("debug", "text")
	codexClient := upstream.NewCodexClient(codexMock.baseURL(), "tok", "acct", 30*time.Second)
	router := upstream.NewRouter([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: opencodeMock.URL, APIKey: cfg.UpstreamAPIKey},
	}, codexClient, 30*time.Second)
	catalog := models.NewCatalog(opencodeMock.URL, cfg.UpstreamAPIKey, 5*time.Minute)
	handler := NewHandler(cfg, catalog, router, logger)

	body := `{"model":"custom","max_tokens":100,"stream":false,"messages":[{"role":"user","content":"test"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleMessages(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "non-stream fallback") {
		t.Errorf("Expected fallback response, got: %s", w.Body.String())
	}
	t.Logf("✅ Non-streaming Codex fallback works")
}
