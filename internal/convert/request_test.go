package convert

import (
	"encoding/json"
	"testing"

	"github.com/claude-code-opencode/claude-proxy/internal/anthropic"
	"github.com/claude-code-opencode/claude-proxy/internal/openai"
)

// Ensure the openai import is used
var _ = openai.ChatMessage{}

func TestRequest_PlainUserMessage(t *testing.T) {
	req := &anthropic.MessageRequest{
		Model: "big-pickle",
		Messages: []anthropic.Message{
			{
				Role:    "user",
				Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{{Type: "text", Text: "Hello"}}},
			},
		},
		MaxTokens: 100,
	}

	oai, err := Request(req, "big-pickle")
	if err != nil {
		t.Fatal(err)
	}

	if oai.Model != "big-pickle" {
		t.Errorf("model = %q, want big-pickle", oai.Model)
	}
	if len(oai.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(oai.Messages))
	}
	if oai.Messages[0].Role != "user" {
		t.Errorf("role = %q, want user", oai.Messages[0].Role)
	}
	if oai.Messages[0].Content != "Hello" {
		t.Errorf("content = %q, want Hello", oai.Messages[0].Content)
	}
	if oai.MaxTokens != 100 {
		t.Errorf("max_tokens = %d, want 100", oai.MaxTokens)
	}
}

func TestRequest_SystemPrompt(t *testing.T) {
	sysJSON, _ := json.Marshal("You are a helpful assistant")
	req := &anthropic.MessageRequest{
		Model:  "big-pickle",
		System: sysJSON,
		Messages: []anthropic.Message{
			{
				Role:    "user",
				Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{{Type: "text", Text: "Hello"}}},
			},
		},
		MaxTokens: 100,
	}

	oai, err := Request(req, "big-pickle")
	if err != nil {
		t.Fatal(err)
	}

	if len(oai.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(oai.Messages))
	}
	if oai.Messages[0].Role != "system" {
		t.Errorf("first message role = %q, want system", oai.Messages[0].Role)
	}
	if oai.Messages[0].Content != "You are a helpful assistant" {
		t.Errorf("system content = %q", oai.Messages[0].Content)
	}
}

func TestRequest_SystemPromptAsBlocks(t *testing.T) {
	sysJSON, _ := json.Marshal([]anthropic.ContentBlock{
		{Type: "text", Text: "System part 1"},
		{Type: "text", Text: "System part 2"},
	})
	req := &anthropic.MessageRequest{
		Model:  "big-pickle",
		System: sysJSON,
		Messages: []anthropic.Message{
			{
				Role:    "user",
				Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{{Type: "text", Text: "Hello"}}},
			},
		},
		MaxTokens: 100,
	}

	oai, err := Request(req, "big-pickle")
	if err != nil {
		t.Fatal(err)
	}

	if oai.Messages[0].Role != "system" {
		t.Errorf("first message role = %q, want system", oai.Messages[0].Role)
	}
	content, ok := oai.Messages[0].Content.(string)
	if !ok {
		t.Fatalf("system content is not string: %T", oai.Messages[0].Content)
	}
	if content != "System part 1\nSystem part 2" {
		t.Errorf("system content = %q", content)
	}
}

func TestRequest_AssistantMessage(t *testing.T) {
	req := &anthropic.MessageRequest{
		Model: "big-pickle",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{{Type: "text", Text: "Hello"}}}},
			{Role: "assistant", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{{Type: "text", Text: "Hi there"}}}},
			{Role: "user", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{{Type: "text", Text: "How are you?"}}}},
		},
		MaxTokens: 100,
	}

	oai, err := Request(req, "big-pickle")
	if err != nil {
		t.Fatal(err)
	}

	if len(oai.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(oai.Messages))
	}
	if oai.Messages[1].Role != "assistant" {
		t.Errorf("second message role = %q, want assistant", oai.Messages[1].Role)
	}
	if oai.Messages[1].Content != "Hi there" {
		t.Errorf("second message content = %q, want Hi there", oai.Messages[1].Content)
	}
}

func TestRequest_Tools(t *testing.T) {
	req := &anthropic.MessageRequest{
		Model: "big-pickle",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{{Type: "text", Text: "Hello"}}}},
		},
		Tools: []anthropic.Tool{
			{
				Name:        "get_weather",
				Description: "Get weather info",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"location": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
		MaxTokens: 100,
	}

	oai, err := Request(req, "big-pickle")
	if err != nil {
		t.Fatal(err)
	}

	if len(oai.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(oai.Tools))
	}
	if oai.Tools[0].Type != "function" {
		t.Errorf("tool type = %q, want function", oai.Tools[0].Type)
	}
	if oai.Tools[0].Function.Name != "get_weather" {
		t.Errorf("tool name = %q, want get_weather", oai.Tools[0].Function.Name)
	}
}

func TestRequest_ToolChoice(t *testing.T) {
	tests := []struct {
		name     string
		tc       *anthropic.ToolChoice
		expected interface{}
	}{
		{
			name:     "auto",
			tc:       &anthropic.ToolChoice{Type: "auto"},
			expected: "auto",
		},
		{
			name:     "any",
			tc:       &anthropic.ToolChoice{Type: "any"},
			expected: map[string]string{"type": "required"},
		},
		{
			name:     "tool",
			tc:       &anthropic.ToolChoice{Type: "tool", Name: "get_weather"},
			expected: map[string]interface{}{"type": "function", "function": map[string]string{"name": "get_weather"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &anthropic.MessageRequest{
				Model: "big-pickle",
				Messages: []anthropic.Message{
					{Role: "user", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{{Type: "text", Text: "Hello"}}}},
				},
				ToolChoice: tt.tc,
				MaxTokens:  100,
			}

			oai, err := Request(req, "big-pickle")
			if err != nil {
				t.Fatal(err)
			}

			got, _ := json.Marshal(oai.ToolChoice)
			want, _ := json.Marshal(tt.expected)
			if string(got) != string(want) {
				t.Errorf("tool_choice = %s, want %s", got, want)
			}
		})
	}
}

func TestRequest_ToolResults(t *testing.T) {
	req := &anthropic.MessageRequest{
		Model: "big-pickle",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{{Type: "text", Text: "What's the weather?"}}}},
			{Role: "assistant", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{
				{Type: "tool_use", ID: "call_123", Name: "get_weather", Input: json.RawMessage(`{"location":"Paris"}`)},
			}}},
			{Role: "user", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{
				{Type: "tool_result", ToolUseID: "call_123", Content: "Sunny, 22°C"},
			}}},
		},
		MaxTokens: 100,
	}

	oai, err := Request(req, "big-pickle")
	if err != nil {
		t.Fatal(err)
	}

	// Find tool message
	var toolMsg *openai.ChatMessage
	for i := range oai.Messages {
		if oai.Messages[i].Role == "tool" {
			toolMsg = &oai.Messages[i]
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("no tool message found")
	}
	if toolMsg.ToolCallID != "call_123" {
		t.Errorf("tool_call_id = %q, want call_123", toolMsg.ToolCallID)
	}
	if toolMsg.Content != "Sunny, 22°C" {
		t.Errorf("content = %q, want Sunny, 22°C", toolMsg.Content)
	}
}

func TestRequest_MultipleToolCalls(t *testing.T) {
	req := &anthropic.MessageRequest{
		Model: "big-pickle",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{{Type: "text", Text: "Check weather and time"}}}},
			{Role: "assistant", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{
				{Type: "tool_use", ID: "call_1", Name: "get_weather", Input: json.RawMessage(`{"location":"Paris"}`)},
				{Type: "tool_use", ID: "call_2", Name: "get_time", Input: json.RawMessage(`{"timezone":"CET"}`)},
			}}},
			{Role: "user", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{
				{Type: "tool_result", ToolUseID: "call_1", Content: "Sunny"},
				{Type: "tool_result", ToolUseID: "call_2", Content: "14:00"},
			}}},
		},
		MaxTokens: 100,
	}

	oai, err := Request(req, "big-pickle")
	if err != nil {
		t.Fatal(err)
	}

	// Find assistant message
	var assistantMsg *openai.ChatMessage
	for i := range oai.Messages {
		if oai.Messages[i].Role == "assistant" {
			assistantMsg = &oai.Messages[i]
			break
		}
	}
	if assistantMsg == nil {
		t.Fatal("no assistant message found")
	}
	if len(assistantMsg.ToolCalls) != 2 {
		t.Errorf("tool_calls = %d, want 2", len(assistantMsg.ToolCalls))
	}

	// Find tool messages
	toolCount := 0
	for _, m := range oai.Messages {
		if m.Role == "tool" {
			toolCount++
		}
	}
	if toolCount != 2 {
		t.Errorf("tool messages = %d, want 2", toolCount)
	}
}

func TestRequest_StopSequences(t *testing.T) {
	req := &anthropic.MessageRequest{
		Model:         "big-pickle",
		Messages:      []anthropic.Message{{Role: "user", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{{Type: "text", Text: "Hello"}}}}},
		StopSequences: []string{"STOP", "END"},
		MaxTokens:     100,
	}

	oai, err := Request(req, "big-pickle")
	if err != nil {
		t.Fatal(err)
	}

	if len(oai.Stop) != 2 {
		t.Fatalf("stop = %d, want 2", len(oai.Stop))
	}
	if oai.Stop[0] != "STOP" || oai.Stop[1] != "END" {
		t.Errorf("stop = %v, want [STOP END]", oai.Stop)
	}
}

func TestRequest_TemperatureAndTopP(t *testing.T) {
	temp := 0.7
	topP := 0.9
	req := &anthropic.MessageRequest{
		Model:       "big-pickle",
		Messages:    []anthropic.Message{{Role: "user", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{{Type: "text", Text: "Hello"}}}}},
		Temperature: &temp,
		TopP:        &topP,
		MaxTokens:   100,
	}

	oai, err := Request(req, "big-pickle")
	if err != nil {
		t.Fatal(err)
	}

	if oai.Temperature == nil || *oai.Temperature != 0.7 {
		t.Errorf("temperature = %v, want 0.7", oai.Temperature)
	}
	if oai.TopP == nil || *oai.TopP != 0.9 {
		t.Errorf("top_p = %v, want 0.9", oai.TopP)
	}
}

func TestRequest_NilRequest(t *testing.T) {
	_, err := Request(nil, "big-pickle")
	if err == nil {
		t.Error("expected error for nil request")
	}
}

func TestRequest_EmptyModelUsesDefault(t *testing.T) {
	req := &anthropic.MessageRequest{
		Messages:  []anthropic.Message{{Role: "user", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{{Type: "text", Text: "Hello"}}}}},
		MaxTokens: 100,
	}

	oai, err := Request(req, "big-pickle")
	if err != nil {
		t.Fatal(err)
	}

	if oai.Model != "big-pickle" {
		t.Errorf("model = %q, want big-pickle", oai.Model)
	}
}

func TestRequest_StreamOptions(t *testing.T) {
	req := &anthropic.MessageRequest{
		Model:     "big-pickle",
		Messages:  []anthropic.Message{{Role: "user", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{{Type: "text", Text: "Hello"}}}}},
		Stream:    true,
		MaxTokens: 100,
	}

	oai, err := Request(req, "big-pickle")
	if err != nil {
		t.Fatal(err)
	}

	if !oai.Stream {
		t.Error("stream = false, want true")
	}
	if oai.StreamOptions == nil {
		t.Fatal("stream_options is nil")
	}
	if !oai.StreamOptions.IncludeUsage {
		t.Error("include_usage = false, want true")
	}
}

func TestRequest_ImageContent(t *testing.T) {
	req := &anthropic.MessageRequest{
		Model: "big-pickle",
		Messages: []anthropic.Message{
			{
				Role: "user",
				Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{
					{Type: "text", Text: "What's in this image?"},
					{Type: "image", Source: &anthropic.ImageSource{
						Type:      "base64",
						MediaType: "image/png",
						Data:      "iVBORw0KGgo=",
					}},
				}},
			},
		},
		MaxTokens: 100,
	}

	oai, err := Request(req, "big-pickle")
	if err != nil {
		t.Fatal(err)
	}

	if len(oai.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(oai.Messages))
	}

	// Content should be an array (multimodal)
	contentArr, ok := oai.Messages[0].Content.([]interface{})
	if !ok {
		t.Fatalf("content is not array: %T", oai.Messages[0].Content)
	}
	if len(contentArr) != 2 {
		t.Errorf("content parts = %d, want 2", len(contentArr))
	}
}

func TestRequest_ToolResultWithError(t *testing.T) {
	req := &anthropic.MessageRequest{
		Model: "big-pickle",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{{Type: "text", Text: "Hello"}}}},
			{Role: "assistant", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{
				{Type: "tool_use", ID: "call_err", Name: "fail_tool", Input: json.RawMessage(`{}`)},
			}}},
			{Role: "user", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{
				{Type: "tool_result", ToolUseID: "call_err", Content: "something went wrong", IsError: true},
			}}},
		},
		MaxTokens: 100,
	}

	oai, err := Request(req, "big-pickle")
	if err != nil {
		t.Fatal(err)
	}

	var toolMsg *openai.ChatMessage
	for i := range oai.Messages {
		if oai.Messages[i].Role == "tool" {
			toolMsg = &oai.Messages[i]
			break
		}
	}
	if toolMsg == nil {
		t.Fatal("no tool message found")
	}
	if toolMsg.Content != "Error: something went wrong" {
		t.Errorf("content = %q, want 'Error: something went wrong'", toolMsg.Content)
	}
}

func TestRequest_TopKForwarded(t *testing.T) {
	topK := 40
	req := &anthropic.MessageRequest{
		Model:     "big-pickle",
		Messages:  []anthropic.Message{{Role: "user", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{{Type: "text", Text: "Hello"}}}}},
		TopK:      &topK,
		MaxTokens: 100,
	}

	oai, err := Request(req, "big-pickle")
	if err != nil {
		t.Fatal(err)
	}

	if oai.TopK == nil {
		t.Fatal("top_k is nil")
	}
	if *oai.TopK != 40 {
		t.Errorf("top_k = %d, want 40", *oai.TopK)
	}
}

func TestRequest_EmptyMessages(t *testing.T) {
	req := &anthropic.MessageRequest{
		Model:     "big-pickle",
		Messages:  []anthropic.Message{},
		MaxTokens: 100,
	}

	oai, err := Request(req, "big-pickle")
	if err != nil {
		t.Fatal(err)
	}

	if oai.Model != "big-pickle" {
		t.Errorf("model = %q, want big-pickle", oai.Model)
	}
	if len(oai.Messages) != 0 {
		t.Errorf("messages = %d, want 0", len(oai.Messages))
	}
}

func TestRequest_ToolChoiceNone(t *testing.T) {
	req := &anthropic.MessageRequest{
		Model: "big-pickle",
		Messages: []anthropic.Message{
			{Role: "user", Content: anthropic.MessageContent{Parts: []anthropic.ContentBlock{{Type: "text", Text: "Hello"}}}},
		},
		ToolChoice: &anthropic.ToolChoice{Type: "none"},
		MaxTokens:  100,
	}

	oai, err := Request(req, "big-pickle")
	if err != nil {
		t.Fatal(err)
	}

	got, _ := json.Marshal(oai.ToolChoice)
	if string(got) != `"none"` {
		t.Errorf("tool_choice = %s, want \"none\"", got)
	}
}
