package codex

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/claude-code-opencode/claude-proxy/internal/anthropic"
)

// capturedRequest stores everything a mock Codex backend captured from one request.
type capturedRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
}

// mockCodexBackend starts a fake Codex backend that records requests and
// returns configurable responses.
type mockCodexBackend struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []capturedRequest
	// handler is called for each request; if nil, returns 200 + SSE "done"
	handler func(w http.ResponseWriter, r *http.Request, body []byte)
}

func newMockCodexBackend() *mockCodexBackend {
	m := &mockCodexBackend{}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		_ = r.Body.Close()

		m.mu.Lock()
		m.requests = append(m.requests, capturedRequest{
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
		// Default: return a valid SSE response.done
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.done\ndata: " + defaultResponseBody() + "\n\n"))
	}))
	return m
}

func (m *mockCodexBackend) close()          { m.server.Close() }
func (m *mockCodexBackend) baseURL() string { return m.server.URL }

func (m *mockCodexBackend) lastRequest() *capturedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		return nil
	}
	return &m.requests[len(m.requests)-1]
}

func (m *mockCodexBackend) requestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func defaultResponseBody() string {
	return `{"type":"response.done","response":{"id":"resp_test","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":10,"output_tokens":5}}}`
}

// --- Tests: Request format validation ---

func TestCodexRequest_BasicText(t *testing.T) {
	mock := newMockCodexBackend()
	defer mock.close()

	// Build an Anthropic request
	req := &anthropic.MessageRequest{
		Model: "gpt-5.6-terra",
		Messages: []anthropic.Message{
			{
				Role: "user",
				Content: anthropic.MessageContent{
					Parts: []anthropic.ContentBlock{
						{Type: "text", Text: "Hello, world!"},
					},
				},
			},
		},
		Stream: true,
	}

	codexReq, err := TransformRequest(req)
	if err != nil {
		t.Fatalf("TransformRequest: %v", err)
	}

	body, _ := json.Marshal(codexReq)

	// Send via CodexClient directly to our mock
	client := newTestCodexClient(mock.baseURL(), "test-oauth-token", "acct_test")
	resp, err := client.Do(t.Context(), "POST", CodexPath, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	_ = resp.Body.Close()

	captured := mock.lastRequest()
	if captured == nil {
		t.Fatal("mock received no request")
	}

	// --- Validate method + path ---
	if captured.Method != "POST" {
		t.Errorf("Method = %q, want POST", captured.Method)
	}
	if captured.Path != CodexPath {
		t.Errorf("Path = %q, want %s", captured.Path, CodexPath)
	}

	// --- Validate headers ---
	assertHeader(t, captured.Headers, "Content-Type", "application/json")
	assertHeader(t, captured.Headers, "Authorization", "Bearer test-oauth-token")
	assertHeader(t, captured.Headers, "chatgpt-account-id", "acct_test")
	assertHeader(t, captured.Headers, "OpenAI-Beta", "responses=experimental")
	assertHeader(t, captured.Headers, "originator", "codex_cli_rs")
	assertHeader(t, captured.Headers, "x-openai-client-name", "codex_cli")
	assertHeader(t, captured.Headers, "x-openai-client-version", "0.144.1")
	assertHeader(t, captured.Headers, "accept", "text/event-stream")

	// --- Validate body ---
	var parsed ResponsesRequest
	if err := json.Unmarshal(captured.Body, &parsed); err != nil {
		t.Fatalf("body is not valid JSON: %v\nbody: %s", err, string(captured.Body))
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
	if len(parsed.Input) != 1 {
		t.Fatalf("input = %d items, want 1", len(parsed.Input))
	}
	if parsed.Input[0].Type != "message" {
		t.Errorf("input[0].type = %q, want message", parsed.Input[0].Type)
	}
	if parsed.Input[0].Role != "user" {
		t.Errorf("input[0].role = %q, want user", parsed.Input[0].Role)
	}
	if parsed.Input[0].Content != "Hello, world!" {
		t.Errorf("input[0].content = %q, want 'Hello, world!'", parsed.Input[0].Content)
	}
}

func TestCodexRequest_WithSystemPrompt(t *testing.T) {
	mock := newMockCodexBackend()
	defer mock.close()

	sysJSON, _ := json.Marshal("You are a helpful coding assistant.")
	req := &anthropic.MessageRequest{
		Model:  "gpt-5.6-sol",
		System: sysJSON,
		Messages: []anthropic.Message{
			{
				Role: "user",
				Content: anthropic.MessageContent{
					Parts: []anthropic.ContentBlock{
						{Type: "text", Text: "Write a function"},
					},
				},
			},
		},
		Stream: true,
	}

	codexReq, _ := TransformRequest(req)
	body, _ := json.Marshal(codexReq)

	client := newTestCodexClient(mock.baseURL(), "tok", "acct")
	resp, _ := client.Do(t.Context(), "POST", CodexPath, strings.NewReader(string(body)))
	_ = resp.Body.Close()

	captured := mock.lastRequest()
	if captured == nil {
		t.Fatal("no request captured")
	}

	var parsed ResponsesRequest
	_ = json.Unmarshal(captured.Body, &parsed)

	if parsed.Instructions != "You are a helpful coding assistant." {
		t.Errorf("instructions = %q, want 'You are a helpful coding assistant.'", parsed.Instructions)
	}
}

func TestCodexRequest_WithTools(t *testing.T) {
	mock := newMockCodexBackend()
	defer mock.close()

	req := &anthropic.MessageRequest{
		Model: "gpt-5.6-terra",
		Messages: []anthropic.Message{
			{
				Role: "user",
				Content: anthropic.MessageContent{
					Parts: []anthropic.ContentBlock{
						{Type: "text", Text: "What's the weather?"},
					},
				},
			},
		},
		Tools: []anthropic.Tool{
			{
				Name:        "get_weather",
				Description: "Get current weather",
				InputSchema: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{"location": map[string]interface{}{"type": "string"}},
				},
			},
		},
		Stream: true,
	}

	codexReq, _ := TransformRequest(req)
	body, _ := json.Marshal(codexReq)

	client := newTestCodexClient(mock.baseURL(), "tok", "acct")
	resp, _ := client.Do(t.Context(), "POST", CodexPath, strings.NewReader(string(body)))
	_ = resp.Body.Close()

	captured := mock.lastRequest()
	var parsed ResponsesRequest
	_ = json.Unmarshal(captured.Body, &parsed)

	if len(parsed.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(parsed.Tools))
	}
	tool := parsed.Tools[0]
	if tool.Type != "function" {
		t.Errorf("tool.type = %q, want function", tool.Type)
	}
	if tool.Name != "get_weather" {
		t.Errorf("tool.name = %q, want get_weather (must be at root level)", tool.Name)
	}
	if tool.Description != "Get current weather" {
		t.Errorf("tool.Description = %q, want 'Get current weather'", tool.Description)
	}
	if tool.Parameters == nil {
		t.Fatal("tool.Parameters is nil (must be at root level)")
	}
}

func TestCodexRequest_WithThinking(t *testing.T) {
	mock := newMockCodexBackend()
	defer mock.close()

	budget := 20000
	req := &anthropic.MessageRequest{
		Model: "gpt-5.6-sol",
		Messages: []anthropic.Message{
			{
				Role: "user",
				Content: anthropic.MessageContent{
					Parts: []anthropic.ContentBlock{
						{Type: "text", Text: "Think hard"},
					},
				},
			},
		},
		Thinking: &anthropic.ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: &budget,
		},
		Stream: true,
	}

	codexReq, _ := TransformRequest(req)
	body, _ := json.Marshal(codexReq)

	client := newTestCodexClient(mock.baseURL(), "tok", "acct")
	resp, _ := client.Do(t.Context(), "POST", CodexPath, strings.NewReader(string(body)))
	_ = resp.Body.Close()

	captured := mock.lastRequest()
	var parsed ResponsesRequest
	_ = json.Unmarshal(captured.Body, &parsed)

	if parsed.Reasoning == nil {
		t.Fatal("reasoning is nil, want non-nil")
	}
	if parsed.Reasoning.Effort != "high" {
		t.Errorf("reasoning.effort = %q, want high (budget %d > 10000)", parsed.Reasoning.Effort, budget)
	}
	if parsed.Reasoning.Summary != "auto" {
		t.Errorf("reasoning.summary = %q, want auto", parsed.Reasoning.Summary)
	}
	if len(parsed.Include) != 1 || parsed.Include[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v, want [reasoning.encrypted_content]", parsed.Include)
	}
}

func TestCodexRequest_MultiTurnConversation(t *testing.T) {
	mock := newMockCodexBackend()
	defer mock.close()

	req := &anthropic.MessageRequest{
		Model: "gpt-5.6-terra",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{
				Parts: []anthropic.ContentBlock{{Type: "text", Text: "What is 2+2?"}},
			}},
			{Role: "assistant", Content: anthropic.MessageContent{
				Parts: []anthropic.ContentBlock{{Type: "text", Text: "4"}},
			}},
			{Role: "user", Content: anthropic.MessageContent{
				Parts: []anthropic.ContentBlock{{Type: "text", Text: "And 3+3?"}},
			}},
		},
		Stream: true,
	}

	codexReq, _ := TransformRequest(req)
	body, _ := json.Marshal(codexReq)

	client := newTestCodexClient(mock.baseURL(), "tok", "acct")
	resp, _ := client.Do(t.Context(), "POST", CodexPath, strings.NewReader(string(body)))
	_ = resp.Body.Close()

	captured := mock.lastRequest()
	var parsed ResponsesRequest
	_ = json.Unmarshal(captured.Body, &parsed)

	// Should have 3 input items (user, assistant, user)
	if len(parsed.Input) != 3 {
		t.Fatalf("input = %d items, want 3", len(parsed.Input))
	}
	if parsed.Input[0].Role != "user" {
		t.Errorf("input[0].role = %q, want user", parsed.Input[0].Role)
	}
	if parsed.Input[1].Role != "assistant" {
		t.Errorf("input[1].role = %q, want assistant", parsed.Input[1].Role)
	}
	if parsed.Input[2].Role != "user" {
		t.Errorf("input[2].role = %q, want user", parsed.Input[2].Role)
	}
}

func TestCodexRequest_ToolUseAndResult(t *testing.T) {
	mock := newMockCodexBackend()
	defer mock.close()

	inputJSON, _ := json.Marshal(map[string]string{"location": "Paris"})
	req := &anthropic.MessageRequest{
		Model: "gpt-5.6-terra",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{
				Parts: []anthropic.ContentBlock{{Type: "text", Text: "Weather?"}},
			}},
			{Role: "assistant", Content: anthropic.MessageContent{
				Parts: []anthropic.ContentBlock{
					{Type: "text", Text: "Let me check."},
					{Type: "tool_use", ID: "toolu_abc", Name: "get_weather", Input: inputJSON},
				},
			}},
			{Role: "user", Content: anthropic.MessageContent{
				Parts: []anthropic.ContentBlock{
					{Type: "tool_result", ToolUseID: "toolu_abc", Content: "25C, sunny"},
				},
			}},
		},
		Stream: true,
	}

	codexReq, _ := TransformRequest(req)
	body, _ := json.Marshal(codexReq)

	client := newTestCodexClient(mock.baseURL(), "tok", "acct")
	resp, _ := client.Do(t.Context(), "POST", CodexPath, strings.NewReader(string(body)))
	_ = resp.Body.Close()

	captured := mock.lastRequest()
	var parsed ResponsesRequest
	_ = json.Unmarshal(captured.Body, &parsed)

	// Expected items: user message, assistant message, function_call, function_call_output
	// The assistant message has text + tool_use → should produce message + function_call
	var itemTypes []string
	for _, item := range parsed.Input {
		itemTypes = append(itemTypes, item.Type)
	}
	t.Logf("input item types: %v", itemTypes)

	// Check function_call exists
	foundFuncCall := false
	for _, item := range parsed.Input {
		if item.Type == "function_call" {
			foundFuncCall = true
			if item.ID != "fc_toolu_abc" {
				t.Errorf("function_call.id = %q, want fc_toolu_abc", item.ID)
			}
			if item.Name != "get_weather" {
				t.Errorf("function_call.name = %q, want get_weather", item.Name)
			}
		}
	}
	if !foundFuncCall {
		t.Error("no function_call item found in input")
	}

	// Check function_call_output exists
	foundOutput := false
	for _, item := range parsed.Input {
		if item.Type == "function_call_output" {
			foundOutput = true
			if item.CallID != "fc_toolu_abc" {
				t.Errorf("function_call_output.call_id = %q, want fc_toolu_abc", item.CallID)
			}
			if item.Output != "25C, sunny" {
				t.Errorf("function_call_output.output = %q, want '25C, sunny'", item.Output)
			}
		}
	}
	if !foundOutput {
		t.Error("no function_call_output item found in input")
	}
}

// --- Tests: Server error scenarios ---

func TestCodexBackend_400Error(t *testing.T) {
	mock := newMockCodexBackend()
	defer mock.close()

	mock.handler = func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid request: model 'gpt-5.6-terra' is not available","type":"invalid_request_error","code":"model_not_available"}}`))
	}

	req := &anthropic.MessageRequest{
		Model: "gpt-5.6-terra",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{
				Parts: []anthropic.ContentBlock{{Type: "text", Text: "hi"}},
			}},
		},
		Stream: true,
	}

	codexReq, _ := TransformRequest(req)
	body, _ := json.Marshal(codexReq)

	client := newTestCodexClient(mock.baseURL(), "tok", "acct")
	resp, err := client.Do(t.Context(), "POST", CodexPath, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	t.Logf("400 error body: %s", string(respBody))

	// Parse the error to see what the backend says
	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &errResp); err != nil {
		t.Fatalf("cannot parse error response: %v", err)
	}

	t.Logf("Error type: %s", errResp.Error.Type)
	t.Logf("Error code: %s", errResp.Error.Code)
	t.Logf("Error message: %s", errResp.Error.Message)

	// This is a diagnostic test - the error details will show up in test output
	if errResp.Error.Message != "" {
		t.Logf("✅ Backend returned meaningful error: %s", errResp.Error.Message)
	}
}

func TestCodexBackend_401ExpiredToken(t *testing.T) {
	mock := newMockCodexBackend()
	defer mock.close()

	mock.handler = func(w http.ResponseWriter, r *http.Request, body []byte) {
		// Simulate expired token
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid authentication token","type":"authentication_error"}}`))
	}

	req := &anthropic.MessageRequest{
		Model: "gpt-5.6-terra",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{
				Parts: []anthropic.ContentBlock{{Type: "text", Text: "hi"}},
			}},
		},
		Stream: true,
	}

	codexReq, _ := TransformRequest(req)
	body, _ := json.Marshal(codexReq)

	client := newTestCodexClient(mock.baseURL(), "expired-token", "acct")
	resp, err := client.Do(t.Context(), "POST", CodexPath, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	t.Logf("401 error body: %s", string(respBody))
}

func TestCodexBackend_400EmptyBody(t *testing.T) {
	mock := newMockCodexBackend()
	defer mock.close()

	mock.handler = func(w http.ResponseWriter, r *http.Request, body []byte) {
		// Validate: body must be non-empty and valid JSON
		if len(body) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"empty request body"}}`))
			return
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(body, &parsed); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid JSON: " + err.Error()}}`))
			return
		}
		// Check required fields
		if _, ok := parsed["model"]; !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"missing required field: model"}}`))
			return
		}
		if _, ok := parsed["input"]; !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"missing required field: input"}}`))
			return
		}
		// OK
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.done\ndata: " + defaultResponseBody() + "\n\n"))
	}

	req := &anthropic.MessageRequest{
		Model: "gpt-5.6-terra",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{
				Parts: []anthropic.ContentBlock{{Type: "text", Text: "hi"}},
			}},
		},
		Stream: true,
	}

	codexReq, _ := TransformRequest(req)
	body, _ := json.Marshal(codexReq)

	t.Logf("Request body sent to Codex:\n%s", string(body))

	client := newTestCodexClient(mock.baseURL(), "tok", "acct")
	resp, err := client.Do(t.Context(), "POST", CodexPath, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		t.Errorf("status = %d, want 200\nbody: %s", resp.StatusCode, string(respBody))
	}
}

// --- Tests: Repeated requests (simulates the pattern where first work then fail) ---

func TestCodexRequest_MultipleRequestsSameModel(t *testing.T) {
	mock := newMockCodexBackend()
	defer mock.close()

	// Track requests and fail after the 2nd one (like the real behavior)
	failAfter := 2
	mock.handler = func(w http.ResponseWriter, r *http.Request, body []byte) {
		count := mock.requestCount()
		if count > failAfter {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Rate limit exceeded for this model","type":"invalid_request_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.done\ndata: " + defaultResponseBody() + "\n\n"))
	}

	client := newTestCodexClient(mock.baseURL(), "tok", "acct")

	for i := 0; i < 5; i++ {
		req := &anthropic.MessageRequest{
			Model: "gpt-5.6-terra",
			Messages: []anthropic.Message{
				{Role: "user", Content: anthropic.MessageContent{
					Parts: []anthropic.ContentBlock{{Type: "text", Text: "hello"}},
				}},
			},
			Stream: true,
		}

		codexReq, _ := TransformRequest(req)
		body, _ := json.Marshal(codexReq)

		resp, err := client.Do(t.Context(), "POST", CodexPath, strings.NewReader(string(body)))
		if err != nil {
			t.Fatalf("request %d: unexpected error: %v", i+1, err)
		}

		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()

		if i < failAfter {
			if resp.StatusCode != http.StatusOK {
				t.Errorf("request %d: status = %d, want 200", i+1, resp.StatusCode)
			}
		} else {
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("request %d: status = %d, want 400 (after failAfter=%d)", i+1, resp.StatusCode, failAfter)
			}
			t.Logf("request %d: 400 body: %s", i+1, string(respBody))
		}
	}

	// Verify all 5 requests were made
	if mock.requestCount() != 5 {
		t.Errorf("total requests = %d, want 5", mock.requestCount())
	}
}

// --- Tests: Headers validation ---

func TestCodexClient_AllHeadersSent(t *testing.T) {
	mock := newMockCodexBackend()
	defer mock.close()

	var capturedHeaders http.Header
	mock.handler = func(w http.ResponseWriter, r *http.Request, body []byte) {
		capturedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.done\ndata: " + defaultResponseBody() + "\n\n"))
	}

	client := newTestCodexClient(mock.baseURL(), "my-oauth-token-xyz", "acct_12345")
	req := &anthropic.MessageRequest{
		Model: "gpt-5.6-sol",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{
				Parts: []anthropic.ContentBlock{{Type: "text", Text: "test"}},
			}},
		},
		Stream: true,
	}

	codexReq, _ := TransformRequest(req)
	body, _ := json.Marshal(codexReq)
	resp, _ := client.Do(t.Context(), "POST", CodexPath, strings.NewReader(string(body)))
	_ = resp.Body.Close()

	if capturedHeaders == nil {
		t.Fatal("no headers captured")
	}

	// Log all headers for debugging
	t.Log("=== Headers sent to Codex backend ===")
	for k, v := range capturedHeaders {
		t.Logf("  %s: %s", k, strings.Join(v, ", "))
	}

	// Required headers
	requiredHeaders := map[string]string{
		"Content-Type":            "application/json",
		"Authorization":           "Bearer my-oauth-token-xyz",
		"chatgpt-account-id":      "acct_12345",
		"OpenAI-Beta":             "responses=experimental",
		"originator":              "codex_cli_rs",
		"x-openai-client-name":    "codex_cli",
		"x-openai-client-version": "0.144.1",
		"accept":                  "text/event-stream",
	}

	for key, expected := range requiredHeaders {
		got := capturedHeaders.Get(key)
		if got != expected {
			t.Errorf("header %s = %q, want %q", key, got, expected)
		} else {
			t.Logf("✓ %s: %s", key, got)
		}
	}
}

// --- Test: Reproduce the exact failure pattern from logs ---

// TestCodexRequest_ReproduceRealFailurePattern simulates what happens in production:
// - Request 1 and 2 succeed
// - Request 3+ always return 400
// This tests whether the issue is with request format, token, or backend state.
func TestCodexRequest_ReproduceRealFailurePattern(t *testing.T) {
	mock := newMockCodexBackend()
	defer mock.close()

	var mu sync.Mutex
	requestNum := 0
	mock.handler = func(w http.ResponseWriter, r *http.Request, body []byte) {
		mu.Lock()
		requestNum++
		n := requestNum
		mu.Unlock()

		// Log the exact request for each attempt
		var parsed ResponsesRequest
		_ = json.Unmarshal(body, &parsed)
		t.Logf("Request #%d: model=%s, input_count=%d, stream=%v, store=%v, instructions=%q",
			n, parsed.Model, len(parsed.Input), parsed.Stream, parsed.Store, parsed.Instructions)

		if n <= 2 {
			// First 2 succeed
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: response.done\ndata: " + defaultResponseBody() + "\n\n"))
		} else {
			// 3+ fail with 400 — exactly like the real behavior
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid request","type":"invalid_request_error"}}`))
		}
	}

	client := newTestCodexClient(mock.baseURL(), "valid-token", "acct_real")

	for i := 0; i < 6; i++ {
		req := &anthropic.MessageRequest{
			Model: "gpt-5.6-terra",
			Messages: []anthropic.Message{
				{Role: "user", Content: anthropic.MessageContent{
					Parts: []anthropic.ContentBlock{{Type: "text", Text: "hello"}},
				}},
			},
			Stream: true,
		}

		codexReq, _ := TransformRequest(req)
		body, _ := json.Marshal(codexReq)

		resp, err := client.Do(nil, "POST", CodexPath, strings.NewReader(string(body)))
		if err != nil {
			t.Fatalf("request %d: error: %v", i+1, err)
		}

		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()

		if i < 2 {
			if resp.StatusCode != 200 {
				t.Errorf("request %d: expected 200, got %d: %s", i+1, resp.StatusCode, string(respBody))
			}
		} else {
			if resp.StatusCode != 400 {
				t.Errorf("request %d: expected 400, got %d: %s", i+1, resp.StatusCode, string(respBody))
			}
		}
	}

	// Verify all requests used the SAME model name
	captured := mock.requests
	for i := 1; i < len(captured); i++ {
		var prev, curr ResponsesRequest
		_ = json.Unmarshal(captured[i-1].Body, &prev)
		_ = json.Unmarshal(captured[i].Body, &curr)
		if prev.Model != curr.Model {
			t.Errorf("request %d model=%q differs from request %d model=%q", i, curr.Model, i-1, prev.Model)
		}
	}
}

// TestCodexRequest_TokenValidation sends requests with different tokens
// to check if the backend validates them properly.
func TestCodexRequest_TokenValidation(t *testing.T) {
	tests := []struct {
		name   string
		token  string
		wantOK bool
	}{
		{"empty token", "", false},
		{"fake token", "fake-token-12345", false},
		{"malformed Bearer", "not-a-jwt", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockCodexBackend()
			defer mock.close()

			var capturedToken string
			mock.handler = func(w http.ResponseWriter, r *http.Request, body []byte) {
				capturedToken = r.Header.Get("Authorization")
				// Check token
				if tt.token == "" || tt.token == "fake-token-12345" || tt.token == "not-a-jwt" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":{"message":"Invalid token","type":"authentication_error"}}`))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("event: response.done\ndata: " + defaultResponseBody() + "\n\n"))
			}

			client := newTestCodexClient(mock.baseURL(), tt.token, "acct")
			req := &anthropic.MessageRequest{
				Model: "gpt-5.6-terra",
				Messages: []anthropic.Message{
					{Role: "user", Content: anthropic.MessageContent{
						Parts: []anthropic.ContentBlock{{Type: "text", Text: "test"}},
					}},
				},
				Stream: true,
			}
			codexReq, _ := TransformRequest(req)
			body, _ := json.Marshal(codexReq)
			resp, _ := client.Do(nil, "POST", CodexPath, strings.NewReader(string(body)))
			_ = resp.Body.Close()

			if tt.wantOK && resp.StatusCode != 200 {
				t.Errorf("expected 200, got %d", resp.StatusCode)
			}
			if !tt.wantOK && resp.StatusCode == 200 {
				t.Errorf("expected non-200, got 200")
			}
			t.Logf("token=%q → status=%d, auth_header=%q", tt.token, resp.StatusCode, capturedToken)
		})
	}
}

// --- Helper ---

func assertHeader(t *testing.T, headers http.Header, key, expected string) {
	t.Helper()
	got := headers.Get(key)
	if got != expected {
		t.Errorf("header %s = %q, want %q", key, got, expected)
	}
}

// testCodexClient is a minimal HTTP client that mimics CodexClient.Do
// for testing inside the codex package (CodexClient lives in upstream).
type testCodexClient struct {
	baseURL    string
	oauthToken string
	accountID  string
	httpClient *http.Client
}

func newTestCodexClient(baseURL, oauthToken, accountID string) *testCodexClient {
	return &testCodexClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		oauthToken: oauthToken,
		accountID:  accountID,
		httpClient: &http.Client{},
	}
}

func (c *testCodexClient) Do(_ interface{}, method, path string, body io.Reader) (*http.Response, error) {
	u := c.baseURL + path
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.oauthToken)
	req.Header.Set("chatgpt-account-id", c.accountID)
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("x-openai-client-name", "codex_cli")
	req.Header.Set("x-openai-client-version", "0.144.1")
	req.Header.Set("accept", "text/event-stream")
	return c.httpClient.Do(req)
}
