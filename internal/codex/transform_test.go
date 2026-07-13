package codex

import (
	"encoding/json"
	"testing"

	"github.com/claude-code-opencode/claude-proxy/internal/anthropic"
)

func TestTransformRequest_BasicText(t *testing.T) {
	req := &anthropic.MessageRequest{
		Model: "claude-sonnet-4-20250514",
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
	}

	out, err := TransformRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Model != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want %q", out.Model, "claude-sonnet-4-20250514")
	}
	if len(out.Input) != 1 {
		t.Fatalf("input items = %d, want 1", len(out.Input))
	}
	item := out.Input[0]
	if item.Type != "message" {
		t.Errorf("input[0].Type = %q, want %q", item.Type, "message")
	}
	if item.Role != "user" {
		t.Errorf("input[0].Role = %q, want %q", item.Role, "user")
	}
	if item.Content != "Hello, world!" {
		t.Errorf("input[0].Content = %q, want %q", item.Content, "Hello, world!")
	}
}

func TestTransformRequest_WithSystem(t *testing.T) {
	sysJSON, _ := json.Marshal("You are a helpful assistant.")
	req := &anthropic.MessageRequest{
		Model:  "claude-sonnet-4-20250514",
		System: sysJSON,
		Messages: []anthropic.Message{
			{
				Role: "user",
				Content: anthropic.MessageContent{
					Parts: []anthropic.ContentBlock{
						{Type: "text", Text: "Hi"},
					},
				},
			},
		},
	}

	out, err := TransformRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Instructions != "You are a helpful assistant." {
		t.Errorf("instructions = %q, want %q", out.Instructions, "You are a helpful assistant.")
	}
}

func TestTransformRequest_ToolCalls(t *testing.T) {
	inputJSON, _ := json.Marshal(map[string]string{"query": "weather"})
	req := &anthropic.MessageRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []anthropic.Message{
			{
				Role: "assistant",
				Content: anthropic.MessageContent{
					Parts: []anthropic.ContentBlock{
						{Type: "text", Text: "Let me check."},
						{
							Type:  "tool_use",
							ID:    "toolu_123",
							Name:  "get_weather",
							Input: inputJSON,
						},
					},
				},
			},
		},
	}

	out, err := TransformRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(out.Input) != 2 {
		t.Fatalf("input items = %d, want 2", len(out.Input))
	}
	// First item: text message
	if out.Input[0].Type != "message" {
		t.Errorf("input[0].Type = %q, want %q", out.Input[0].Type, "message")
	}
	if out.Input[0].Role != "assistant" {
		t.Errorf("input[0].Role = %q, want %q", out.Input[0].Role, "assistant")
	}
	// Second item: function_call
	fc := out.Input[1]
	if fc.Type != "function_call" {
		t.Errorf("input[1].Type = %q, want %q", fc.Type, "function_call")
	}
	if fc.ID != "fc_toolu_123" {
		t.Errorf("input[1].ID = %q, want fc_toolu_123 (transformed from call_ prefix)", fc.ID)
	}
	if fc.Name != "get_weather" {
		t.Errorf("input[1].Name = %q, want %q", fc.Name, "get_weather")
	}
}

func TestTransformRequest_ToolResults(t *testing.T) {
	req := &anthropic.MessageRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []anthropic.Message{
			{
				Role: "user",
				Content: anthropic.MessageContent{
					Parts: []anthropic.ContentBlock{
						{
							Type:      "tool_result",
							ToolUseID: "toolu_123",
							Content:   "72 degrees, sunny",
						},
					},
				},
			},
		},
	}

	out, err := TransformRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(out.Input) != 1 {
		t.Fatalf("input items = %d, want 1", len(out.Input))
	}
	item := out.Input[0]
	if item.Type != "function_call_output" {
		t.Errorf("input[0].Type = %q, want %q", item.Type, "function_call_output")
	}
	if item.CallID != "fc_toolu_123" {
		t.Errorf("input[0].CallID = %q, want fc_toolu_123 (transformed from call_ prefix)", item.CallID)
	}
	if item.Output != "72 degrees, sunny" {
		t.Errorf("input[0].Output = %q, want %q", item.Output, "72 degrees, sunny")
	}
}

func TestTransformRequest_Thinking(t *testing.T) {
	budget := 20000
	req := &anthropic.MessageRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []anthropic.Message{
			{
				Role: "user",
				Content: anthropic.MessageContent{
					Parts: []anthropic.ContentBlock{
						{Type: "text", Text: "Think hard about this."},
					},
				},
			},
		},
		Thinking: &anthropic.ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: &budget,
		},
	}

	out, err := TransformRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Reasoning == nil {
		t.Fatal("reasoning is nil, want non-nil")
	}
	if out.Reasoning.Effort != "high" {
		t.Errorf("reasoning.effort = %q, want %q (budget %d > 10000)", out.Reasoning.Effort, "high", budget)
	}
	if out.Reasoning.Summary != "auto" {
		t.Errorf("reasoning.summary = %q, want %q", out.Reasoning.Summary, "auto")
	}
	if len(out.Include) != 1 || out.Include[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v, want [reasoning.encrypted_content]", out.Include)
	}
}

func TestTransformRequest_ThinkingMedium(t *testing.T) {
	budget := 5000
	req := &anthropic.MessageRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []anthropic.Message{
			{
				Role: "user",
				Content: anthropic.MessageContent{
					Parts: []anthropic.ContentBlock{
						{Type: "text", Text: "Think a bit."},
					},
				},
			},
		},
		Thinking: &anthropic.ThinkingConfig{
			Type:         "enabled",
			BudgetTokens: &budget,
		},
	}

	out, err := TransformRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Reasoning == nil {
		t.Fatal("reasoning is nil, want non-nil")
	}
	if out.Reasoning.Effort != "medium" {
		t.Errorf("reasoning.effort = %q, want %q (budget %d <= 10000)", out.Reasoning.Effort, "medium", budget)
	}
}

func TestTransformRequest_NilRequest(t *testing.T) {
	_, err := TransformRequest(nil)
	if err == nil {
		t.Error("expected error for nil request, got nil")
	}
}

func TestTransformRequest_StoreFalse(t *testing.T) {
	req := &anthropic.MessageRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []anthropic.Message{
			{
				Role: "user",
				Content: anthropic.MessageContent{
					Parts: []anthropic.ContentBlock{
						{Type: "text", Text: "hi"},
					},
				},
			},
		},
	}

	out, err := TransformRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Store != false {
		t.Errorf("store = %v, want false", out.Store)
	}
	if !out.Stream {
		t.Error("stream = false, want true")
	}
}

func TestExtractSystemPrompt(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		raw, _ := json.Marshal("You are helpful.")
		got := extractSystemPrompt(raw)
		if got != "You are helpful." {
			t.Errorf("extractSystemPrompt(string) = %q, want %q", got, "You are helpful.")
		}
	})

	t.Run("blocks", func(t *testing.T) {
		blocks := []anthropic.ContentBlock{
			{Type: "text", Text: "Line one."},
			{Type: "text", Text: "Line two."},
		}
		raw, _ := json.Marshal(blocks)
		got := extractSystemPrompt(raw)
		want := "Line one.\nLine two."
		if got != want {
			t.Errorf("extractSystemPrompt(blocks) = %q, want %q", got, want)
		}
	})

	t.Run("empty", func(t *testing.T) {
		got := extractSystemPrompt(nil)
		if got != "" {
			t.Errorf("extractSystemPrompt(nil) = %q, want %q", got, "")
		}
	})
}

func TestTransformRequest_ToolDefinition(t *testing.T) {
	req := &anthropic.MessageRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []anthropic.Message{
			{
				Role: "user",
				Content: anthropic.MessageContent{
					Parts: []anthropic.ContentBlock{
						{Type: "text", Text: "Go"},
					},
				},
			},
		},
		Tools: []anthropic.Tool{
			{
				Name:        "search",
				Description: "Search the web",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"q": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
	}

	out, err := TransformRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(out.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(out.Tools))
	}
	tool := out.Tools[0]
	if tool.Type != "function" {
		t.Errorf("tools[0].Type = %q, want %q", tool.Type, "function")
	}
	// The Codex backend requires the tool name/description/parameters at the
	// root level (siblings of type/name), not wrapped in a `details` object.
	if tool.Name != "search" {
		t.Errorf("tools[0].Name = %q, want %q (required by Codex backend at root level)", tool.Name, "search")
	}
	if tool.Description != "Search the web" {
		t.Errorf("tools[0].Description = %q, want %q", tool.Description, "Search the web")
	}
	if tool.Parameters == nil {
		t.Fatal("tools[0].Parameters is nil")
	}
}
