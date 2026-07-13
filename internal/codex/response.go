package codex

import (
	"encoding/json"

	"github.com/claude-code-opencode/claude-proxy/internal/anthropic"
)

// TransformResponse converts a Codex ResponsesDoneBody to an Anthropic MessageResponse.
func TransformResponse(done *ResponsesDoneBody, requestedModel string) *anthropic.MessageResponse {
	if done == nil {
		return nil
	}

	msgID := done.ID
	if msgID == "" {
		msgID = "msg_codex"
	}

	resp := &anthropic.MessageResponse{
		ID:           msgID,
		Type:         "message",
		Role:         "assistant",
		Model:        requestedModel,
		Content:      []anthropic.ContentBlock{},
		StopReason:   "end_turn",
		StopSequence: nil,
		Usage:        anthropic.Usage{},
	}

	if done.Usage != nil {
		resp.Usage = anthropic.Usage{
			InputTokens:  done.Usage.InputTokens,
			OutputTokens: done.Usage.OutputTokens,
		}
	}

	// Convert output items to content blocks
	for _, item := range done.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Type == "output_text" && c.Text != "" {
					resp.Content = append(resp.Content, anthropic.ContentBlock{
						Type: "text",
						Text: c.Text,
					})
				}
			}
		case "function_call":
			resp.Content = append(resp.Content, anthropic.ContentBlock{
				Type:  "tool_use",
				ID:    item.ID,
				Name:  item.Name,
				Input: json.RawMessage(item.Arguments),
			})
		}
	}

	// If no content at all, add empty text
	if len(resp.Content) == 0 {
		resp.Content = []anthropic.ContentBlock{
			{Type: "text", Text: ""},
		}
	}

	// Map stop reason
	switch done.Status {
	case "completed":
		resp.StopReason = "end_turn"
	case "incomplete":
		resp.StopReason = "max_tokens"
	}

	return resp
}
