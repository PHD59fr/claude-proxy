package codex

import (
	"testing"
)

func TestTransformResponse_TextOnly(t *testing.T) {
	done := &ResponsesDoneBody{
		ID:     "resp_abc123",
		Status: "completed",
		Output: []OutputItem{
			{
				Type: "message",
				Content: []OutputContent{
					{Type: "output_text", Text: "Hello, world!"},
				},
			},
		},
	}

	resp := TransformResponse(done, "claude-3-sonnet")

	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.ID != "resp_abc123" {
		t.Errorf("ID = %q, want %q", resp.ID, "resp_abc123")
	}
	if resp.Type != "message" {
		t.Errorf("Type = %q, want %q", resp.Type, "message")
	}
	if resp.Role != "assistant" {
		t.Errorf("Role = %q, want %q", resp.Role, "assistant")
	}
	if resp.Model != "claude-3-sonnet" {
		t.Errorf("Model = %q, want %q", resp.Model, "claude-3-sonnet")
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, "end_turn")
	}
	if len(resp.Content) != 1 {
		t.Fatalf("Content length = %d, want 1", len(resp.Content))
	}
	if resp.Content[0].Type != "text" {
		t.Errorf("Content[0].Type = %q, want %q", resp.Content[0].Type, "text")
	}
	if resp.Content[0].Text != "Hello, world!" {
		t.Errorf("Content[0].Text = %q, want %q", resp.Content[0].Text, "Hello, world!")
	}
}

func TestTransformResponse_ToolCalls(t *testing.T) {
	done := &ResponsesDoneBody{
		ID:     "resp_tool1",
		Status: "completed",
		Output: []OutputItem{
			{
				Type: "function_call",
				ID:   "call_42",
				Name: "get_weather",
				// Arguments is stored as a string in OutputItem
				Arguments: `{"location":"Paris"}`,
			},
			{
				Type:      "function_call",
				ID:        "call_43",
				Name:      "get_time",
				Arguments: `{"timezone":"CET"}`,
			},
		},
	}

	resp := TransformResponse(done, "gpt-5.1-codex")

	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Content) != 2 {
		t.Fatalf("Content length = %d, want 2", len(resp.Content))
	}

	// First tool call
	if resp.Content[0].Type != "tool_use" {
		t.Errorf("Content[0].Type = %q, want %q", resp.Content[0].Type, "tool_use")
	}
	if resp.Content[0].ID != "call_42" {
		t.Errorf("Content[0].ID = %q, want %q", resp.Content[0].ID, "call_42")
	}
	if resp.Content[0].Name != "get_weather" {
		t.Errorf("Content[0].Name = %q, want %q", resp.Content[0].Name, "get_weather")
	}
	if string(resp.Content[0].Input) != `{"location":"Paris"}` {
		t.Errorf("Content[0].Input = %s, want %q", string(resp.Content[0].Input), `{"location":"Paris"}`)
	}

	// Second tool call
	if resp.Content[1].Type != "tool_use" {
		t.Errorf("Content[1].Type = %q, want %q", resp.Content[1].Type, "tool_use")
	}
	if resp.Content[1].Name != "get_time" {
		t.Errorf("Content[1].Name = %q, want %q", resp.Content[1].Name, "get_time")
	}
}

func TestTransformResponse_Empty(t *testing.T) {
	done := &ResponsesDoneBody{
		ID:     "resp_empty",
		Status: "completed",
		Output: []OutputItem{},
	}

	resp := TransformResponse(done, "gpt-5.1")

	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Content) != 1 {
		t.Fatalf("Content length = %d, want 1", len(resp.Content))
	}
	if resp.Content[0].Type != "text" {
		t.Errorf("Content[0].Type = %q, want %q", resp.Content[0].Type, "text")
	}
	if resp.Content[0].Text != "" {
		t.Errorf("Content[0].Text = %q, want empty string", resp.Content[0].Text)
	}
}

func TestTransformResponse_Nil(t *testing.T) {
	resp := TransformResponse(nil, "gpt-5.1")
	if resp != nil {
		t.Errorf("expected nil response, got %+v", resp)
	}
}

func TestTransformResponse_Usage(t *testing.T) {
	done := &ResponsesDoneBody{
		ID:     "resp_usage",
		Status: "completed",
		Output: []OutputItem{
			{
				Type: "message",
				Content: []OutputContent{
					{Type: "output_text", Text: "result"},
				},
			},
		},
		Usage: &Usage{
			InputTokens:  150,
			OutputTokens: 75,
			TotalTokens:  225,
		},
	}

	resp := TransformResponse(done, "gpt-5.1")

	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Usage.InputTokens != 150 {
		t.Errorf("Usage.InputTokens = %d, want 150", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 75 {
		t.Errorf("Usage.OutputTokens = %d, want 75", resp.Usage.OutputTokens)
	}
}

func TestTransformResponse_IncompleteStatus(t *testing.T) {
	done := &ResponsesDoneBody{
		ID:     "resp_inc",
		Status: "incomplete",
		Output: []OutputItem{
			{
				Type: "message",
				Content: []OutputContent{
					{Type: "output_text", Text: "truncated"},
				},
			},
		},
	}

	resp := TransformResponse(done, "gpt-5.1")

	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.StopReason != "max_tokens" {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, "max_tokens")
	}
}

func TestTransformResponse_EmptyID(t *testing.T) {
	done := &ResponsesDoneBody{
		ID:     "",
		Status: "completed",
		Output: []OutputItem{},
	}

	resp := TransformResponse(done, "gpt-5.1")

	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.ID != "msg_codex" {
		t.Errorf("ID = %q, want %q", resp.ID, "msg_codex")
	}
}

func TestTransformResponse_NilUsage(t *testing.T) {
	done := &ResponsesDoneBody{
		ID:     "resp_nou",
		Status: "completed",
		Output: []OutputItem{
			{
				Type: "message",
				Content: []OutputContent{
					{Type: "output_text", Text: "ok"},
				},
			},
		},
		Usage: nil,
	}

	resp := TransformResponse(done, "gpt-5.1")

	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Usage.InputTokens != 0 {
		t.Errorf("Usage.InputTokens = %d, want 0", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 0 {
		t.Errorf("Usage.OutputTokens = %d, want 0", resp.Usage.OutputTokens)
	}
}
