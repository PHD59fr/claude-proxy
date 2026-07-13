package convert

import (
	"encoding/json"
	"testing"

	"github.com/claude-code-opencode/claude-proxy/internal/anthropic"
	"github.com/claude-code-opencode/claude-proxy/internal/openai"
)

func strPtr(s string) *string { return &s }

func TestResponse_PlainText(t *testing.T) {
	resp := &openai.ChatCompletionResponse{
		ID:    "chatcmpl-123",
		Model: "big-pickle",
		Choices: []struct {
			Index   int                `json:"index"`
			Message openai.ChatMessage `json:"message"`
			Finish  *string            `json:"finish_reason"`
		}{
			{
				Index: 0,
				Message: openai.ChatMessage{
					Role:    "assistant",
					Content: "Hello, world!",
				},
				Finish: strPtr("stop"),
			},
		},
		Usage: &openai.Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}

	anthResp := Response(resp, "big-pickle")

	if anthResp == nil {
		t.Fatal("response is nil")
	}
	if anthResp.Type != "message" {
		t.Errorf("type = %q, want message", anthResp.Type)
	}
	if anthResp.Role != "assistant" {
		t.Errorf("role = %q, want assistant", anthResp.Role)
	}
	if anthResp.Model != "big-pickle" {
		t.Errorf("model = %q, want big-pickle", anthResp.Model)
	}
	if len(anthResp.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(anthResp.Content))
	}
	if anthResp.Content[0].Type != "text" {
		t.Errorf("content type = %q, want text", anthResp.Content[0].Type)
	}
	if anthResp.Content[0].Text != "Hello, world!" {
		t.Errorf("content text = %q, want Hello, world!", anthResp.Content[0].Text)
	}
	if anthResp.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want end_turn", anthResp.StopReason)
	}
	if anthResp.Usage.InputTokens != 10 {
		t.Errorf("input_tokens = %d, want 10", anthResp.Usage.InputTokens)
	}
	if anthResp.Usage.OutputTokens != 20 {
		t.Errorf("output_tokens = %d, want 20", anthResp.Usage.OutputTokens)
	}
}

func TestResponse_EmptyText(t *testing.T) {
	resp := &openai.ChatCompletionResponse{
		ID:    "chatcmpl-456",
		Model: "big-pickle",
		Choices: []struct {
			Index   int                `json:"index"`
			Message openai.ChatMessage `json:"message"`
			Finish  *string            `json:"finish_reason"`
		}{
			{
				Index: 0,
				Message: openai.ChatMessage{
					Role:    "assistant",
					Content: "",
				},
				Finish: strPtr("stop"),
			},
		},
	}

	anthResp := Response(resp, "big-pickle")

	if len(anthResp.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(anthResp.Content))
	}
	if anthResp.Content[0].Type != "text" {
		t.Errorf("content type = %q, want text", anthResp.Content[0].Type)
	}
}

func TestResponse_ToolCall(t *testing.T) {
	resp := &openai.ChatCompletionResponse{
		ID:    "chatcmpl-789",
		Model: "big-pickle",
		Choices: []struct {
			Index   int                `json:"index"`
			Message openai.ChatMessage `json:"message"`
			Finish  *string            `json:"finish_reason"`
		}{
			{
				Index: 0,
				Message: openai.ChatMessage{
					Role:    "assistant",
					Content: "",
					ToolCalls: []openai.ToolCall{
						{
							ID:   "call_abc",
							Type: "function",
							Function: openai.FuncCall{
								Name:      "get_weather",
								Arguments: `{"location":"Paris"}`,
							},
						},
					},
				},
				Finish: strPtr("tool_calls"),
			},
		},
	}

	anthResp := Response(resp, "big-pickle")

	if len(anthResp.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(anthResp.Content))
	}
	if anthResp.Content[0].Type != "tool_use" {
		t.Errorf("content type = %q, want tool_use", anthResp.Content[0].Type)
	}
	if anthResp.Content[0].ID != "call_abc" {
		t.Errorf("tool id = %q, want call_abc", anthResp.Content[0].ID)
	}
	if anthResp.Content[0].Name != "get_weather" {
		t.Errorf("tool name = %q, want get_weather", anthResp.Content[0].Name)
	}
	if anthResp.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", anthResp.StopReason)
	}
}

func TestResponse_MultipleToolCalls(t *testing.T) {
	resp := &openai.ChatCompletionResponse{
		ID:    "chatcmpl-multi",
		Model: "big-pickle",
		Choices: []struct {
			Index   int                `json:"index"`
			Message openai.ChatMessage `json:"message"`
			Finish  *string            `json:"finish_reason"`
		}{
			{
				Index: 0,
				Message: openai.ChatMessage{
					Role: "",
					ToolCalls: []openai.ToolCall{
						{ID: "call_1", Type: "function", Function: openai.FuncCall{Name: "tool_a", Arguments: `{}`}},
						{ID: "call_2", Type: "function", Function: openai.FuncCall{Name: "tool_b", Arguments: `{"x":1}`}},
					},
				},
				Finish: strPtr("tool_calls"),
			},
		},
	}

	anthResp := Response(resp, "big-pickle")

	if len(anthResp.Content) != 2 {
		t.Fatalf("content blocks = %d, want 2", len(anthResp.Content))
	}
	if anthResp.Content[0].Name != "tool_a" || anthResp.Content[1].Name != "tool_b" {
		t.Errorf("tool names = [%s, %s], want [tool_a, tool_b]", anthResp.Content[0].Name, anthResp.Content[1].Name)
	}
}

func TestResponse_TextPlusToolCalls(t *testing.T) {
	resp := &openai.ChatCompletionResponse{
		ID:    "chatcmpl-mixed",
		Model: "big-pickle",
		Choices: []struct {
			Index   int                `json:"index"`
			Message openai.ChatMessage `json:"message"`
			Finish  *string            `json:"finish_reason"`
		}{
			{
				Index: 0,
				Message: openai.ChatMessage{
					Role:    "assistant",
					Content: "Let me check that for you.",
					ToolCalls: []openai.ToolCall{
						{ID: "call_xyz", Type: "function", Function: openai.FuncCall{Name: "search", Arguments: `{}`}},
					},
				},
				Finish: strPtr("tool_calls"),
			},
		},
	}

	anthResp := Response(resp, "big-pickle")

	if len(anthResp.Content) != 2 {
		t.Fatalf("content blocks = %d, want 2", len(anthResp.Content))
	}
	if anthResp.Content[0].Type != "text" {
		t.Errorf("first block type = %q, want text", anthResp.Content[0].Type)
	}
	if anthResp.Content[0].Text != "Let me check that for you." {
		t.Errorf("text = %q", anthResp.Content[0].Text)
	}
	if anthResp.Content[1].Type != "tool_use" {
		t.Errorf("second block type = %q, want tool_use", anthResp.Content[1].Type)
	}
}

func TestResponse_FinishReasonMapping(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"stop", "end_turn"},
		{"length", "max_tokens"},
		{"tool_calls", "tool_use"},
		{"content_filter", "end_turn"},
		{"", "end_turn"},
		{"unknown", "end_turn"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := mapFinishReason(&tt.input)
			if got != tt.expected {
				t.Errorf("mapFinishReason(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestResponse_NilFinishReason(t *testing.T) {
	got := mapFinishReason(nil)
	if got != "end_turn" {
		t.Errorf("mapFinishReason(nil) = %q, want end_turn", got)
	}
}

func TestResponse_NilResponse(t *testing.T) {
	anthResp := Response(nil, "big-pickle")
	if anthResp != nil {
		t.Error("expected nil response for nil input")
	}
}

func TestResponse_NoChoices(t *testing.T) {
	resp := &openai.ChatCompletionResponse{
		ID:    "chatcmpl-empty",
		Model: "big-pickle",
		Choices: []struct {
			Index   int                `json:"index"`
			Message openai.ChatMessage `json:"message"`
			Finish  *string            `json:"finish_reason"`
		}{},
	}

	anthResp := Response(resp, "big-pickle")

	if len(anthResp.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(anthResp.Content))
	}
	if anthResp.Content[0].Type != "text" {
		t.Errorf("content type = %q, want text", anthResp.Content[0].Type)
	}
}

func TestResponse_NoUsage(t *testing.T) {
	resp := &openai.ChatCompletionResponse{
		ID:    "chatcmpl-nousage",
		Model: "big-pickle",
		Choices: []struct {
			Index   int                `json:"index"`
			Message openai.ChatMessage `json:"message"`
			Finish  *string            `json:"finish_reason"`
		}{
			{
				Index:   0,
				Message: openai.ChatMessage{Role: "assistant", Content: "Hi"},
				Finish:  strPtr("stop"),
			},
		},
		Usage: nil,
	}

	anthResp := Response(resp, "big-pickle")

	if anthResp.Usage.InputTokens != 0 {
		t.Errorf("input_tokens = %d, want 0", anthResp.Usage.InputTokens)
	}
}

func TestResponse_PreservesUpstreamModel(t *testing.T) {
	resp := &openai.ChatCompletionResponse{
		ID:    "chatcmpl-model",
		Model: "deepseek-v4-flash-free",
		Choices: []struct {
			Index   int                `json:"index"`
			Message openai.ChatMessage `json:"message"`
			Finish  *string            `json:"finish_reason"`
		}{
			{
				Index:   0,
				Message: openai.ChatMessage{Role: "assistant", Content: "Hi"},
				Finish:  strPtr("stop"),
			},
		},
	}

	anthResp := Response(resp, "big-pickle")

	// Should use upstream model when available
	if anthResp.Model != "deepseek-v4-flash-free" {
		t.Errorf("model = %q, want deepseek-v4-flash-free", anthResp.Model)
	}
}

func TestResponse_ToolCallEmptyArgs(t *testing.T) {
	resp := &openai.ChatCompletionResponse{
		ID:    "chatcmpl-emptyargs",
		Model: "big-pickle",
		Choices: []struct {
			Index   int                `json:"index"`
			Message openai.ChatMessage `json:"message"`
			Finish  *string            `json:"finish_reason"`
		}{
			{
				Index: 0,
				Message: openai.ChatMessage{
					ToolCalls: []openai.ToolCall{
						{ID: "call_empty", Type: "function", Function: openai.FuncCall{Name: "noop", Arguments: ""}},
					},
				},
				Finish: strPtr("tool_calls"),
			},
		},
	}

	anthResp := Response(resp, "big-pickle")

	if len(anthResp.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(anthResp.Content))
	}
	// Should get {} for empty args
	if string(anthResp.Content[0].Input) != "{}" {
		t.Errorf("input = %s, want {}", string(anthResp.Content[0].Input))
	}
}

func TestResponse_JSONSerialization(t *testing.T) {
	resp := &openai.ChatCompletionResponse{
		ID:    "chatcmpl-json",
		Model: "big-pickle",
		Choices: []struct {
			Index   int                `json:"index"`
			Message openai.ChatMessage `json:"message"`
			Finish  *string            `json:"finish_reason"`
		}{
			{
				Index:   0,
				Message: openai.ChatMessage{Role: "assistant", Content: "test"},
				Finish:  strPtr("stop"),
			},
		},
		Usage: &openai.Usage{PromptTokens: 5, CompletionTokens: 10},
	}

	anthResp := Response(resp, "big-pickle")

	data, err := json.Marshal(anthResp)
	if err != nil {
		t.Fatal(err)
	}

	var parsed anthropic.MessageResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}

	// ID comes from upstream (msg_json or chatcmpl-json)
	if parsed.Type != "message" {
		t.Errorf("type = %q, want message", parsed.Type)
	}
	if parsed.Role != "assistant" {
		t.Errorf("role = %q, want assistant", parsed.Role)
	}
	if len(parsed.Content) != 1 {
		t.Fatalf("content = %d, want 1", len(parsed.Content))
	}
}
