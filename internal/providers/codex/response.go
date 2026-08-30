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
	hasToolUse := false
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
			callID := item.CallID
			if callID == "" {
				callID = item.ID
			}
			// Some backends omit the function_call arguments or send an empty
			// string; Anthropic clients reject a tool_use input of null/empty,
			// so fall back to an empty object like the streaming path does.
			args := item.Arguments
			if args == "" || args == "null" {
				args = "{}"
			}
			resp.Content = append(resp.Content, anthropic.ContentBlock{
				Type:  "tool_use",
				ID:    callID,
				Name:  item.Name,
				Input: json.RawMessage(args),
			})
			hasToolUse = true
		}
	}

	// If no content at all, add empty text
	if len(resp.Content) == 0 {
		resp.Content = []anthropic.ContentBlock{
			{Type: "text", Text: ""},
		}
	}

	// Map stop reason. Truncation (max_output_tokens) takes precedence over tool
	// use so the client learns the output was cut short rather than assuming the
	// assistant finished its tool call.
	if done.Status == "incomplete" && done.IncompleteDetails != nil && done.IncompleteDetails.Reason == "max_output_tokens" {
		resp.StopReason = "max_tokens"
	} else {
		switch done.Status {
		case "completed", "incomplete":
			if hasToolUse {
				resp.StopReason = "tool_use"
			} else {
				resp.StopReason = "end_turn"
			}
		}
	}

	return resp
}
