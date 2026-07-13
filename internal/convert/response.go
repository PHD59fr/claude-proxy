package convert

import (
	"encoding/json"

	"github.com/claude-code-opencode/claude-proxy/internal/anthropic"
	"github.com/claude-code-opencode/claude-proxy/internal/openai"
)

// Response converts an OpenAI Chat Completions response to Anthropic Messages response.
func Response(oaiResp *openai.ChatCompletionResponse, requestedModel string) *anthropic.MessageResponse {
	if oaiResp == nil {
		return nil
	}

	msgID := oaiResp.ID
	if msgID == "" {
		msgID = "msg_proxy"
	}

	model := requestedModel
	if oaiResp.Model != "" {
		model = oaiResp.Model
	}

	resp := &anthropic.MessageResponse{
		ID:           msgID,
		Type:         "message",
		Role:         "assistant",
		Model:        model,
		Content:      []anthropic.ContentBlock{},
		StopReason:   "end_turn",
		StopSequence: nil,
		Usage:        anthropic.Usage{},
	}

	if len(oaiResp.Choices) == 0 {
		// Empty response
		resp.Content = []anthropic.ContentBlock{
			{Type: "text", Text: ""},
		}
		return resp
	}

	choice := oaiResp.Choices[0]

	// Content
	if contentStr, ok := choice.Message.Content.(string); ok && contentStr != "" {
		resp.Content = append(resp.Content, anthropic.ContentBlock{
			Type: "text",
			Text: contentStr,
		})
	}

	// Tool calls
	for _, tc := range choice.Message.ToolCalls {
		resp.Content = append(resp.Content, anthropic.ContentBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: jsonRawMessage(tc.Function.Arguments),
		})
	}

	// If no content at all, add empty text
	if len(resp.Content) == 0 {
		resp.Content = []anthropic.ContentBlock{
			{Type: "text", Text: ""},
		}
	}

	// Finish reason
	resp.StopReason = mapFinishReason(choice.Finish)

	// Usage
	if oaiResp.Usage != nil {
		resp.Usage = anthropic.Usage{
			InputTokens:  oaiResp.Usage.PromptTokens,
			OutputTokens: oaiResp.Usage.CompletionTokens,
		}
	}

	return resp
}

func mapFinishReason(reason *string) string {
	if reason == nil {
		return "end_turn"
	}
	switch *reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		// Anthropic has no exact equivalent. The caller still receives the
		// provider response rather than a fabricated error envelope.
		return "end_turn"
	default:
		return "end_turn"
	}
}

func jsonRawMessage(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(s)
}
