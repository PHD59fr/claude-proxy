package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/claude-code-opencode/claude-proxy/internal/anthropic"
)

// TransformRequest converts an Anthropic MessageRequest to a Codex ResponsesRequest.
func TransformRequest(req *anthropic.MessageRequest) (*ResponsesRequest, error) {
	if req == nil {
		return nil, fmt.Errorf("nil request")
	}

	model := NormalizeModel(req.Model)

	out := &ResponsesRequest{
		Model:  model,
		Store:  false,
		Stream: true,
	}

	// System prompt → instructions
	if len(req.System) > 0 {
		out.Instructions = extractSystemPrompt(req.System)
	}

	// Messages → input items
	for _, msg := range req.Messages {
		items, err := convertToInputItems(&msg)
		if err != nil {
			return nil, fmt.Errorf("convert message: %w", err)
		}
		out.Input = append(out.Input, items...)
	}

	// Tools
	if len(req.Tools) > 0 {
		out.Tools = make([]ResponsesTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			out.Tools = append(out.Tools, ResponsesTool{
				Type:        "function",
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
				Strict:      &codexNonStrict,
			})
		}
	}

	// Thinking → reasoning
	if req.Thinking != nil && (req.Thinking.Type == "enabled" || req.Thinking.Type == "adaptive") {
		effort := "medium"
		if req.Thinking.BudgetTokens != nil && *req.Thinking.BudgetTokens > 10000 {
			effort = "high"
		}
		out.Reasoning = &Reasoning{
			Effort:  effort,
			Summary: "auto",
		}
		out.Include = []string{"reasoning.encrypted_content"}
	}

	return out, nil
}

// codexFunctionCallID transforms an Anthropic tool_use ID to the format
// expected by the Codex backend. Anthropic sends "call_xxx", Codex requires IDs
// starting with "fc". If the ID already starts with "fc", it is returned as-is.
func codexFunctionCallID(anthropicID string) string {
	if strings.HasPrefix(anthropicID, "fc") {
		return anthropicID
	}
	// Strip "call_" prefix if present, then prepend "fc_"
	id := strings.TrimPrefix(anthropicID, "call_")
	return "fc_" + id
}

// extractSystemPrompt extracts text from the system prompt (string or content blocks).
func extractSystemPrompt(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []anthropic.ContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// convertToInputItems converts an Anthropic Message to Responses API InputItems.
func convertToInputItems(msg *anthropic.Message) ([]InputItem, error) {
	var items []InputItem

	role := msg.Role
	if role != "assistant" {
		role = "user"
	}

	// Handle tool_result messages
	for _, part := range msg.Content.Parts {
		if part.Type == "tool_result" {
			content := ""
			switch c := part.Content.(type) {
			case string:
				content = c
			case []anthropic.ContentBlock:
				var texts []string
				for _, b := range c {
					if b.Type == "text" {
						texts = append(texts, b.Text)
					}
				}
				content = strings.Join(texts, "\n")
			}
			if part.IsError {
				content = "Error: " + content
			}
			items = append(items, InputItem{
				Type:   "function_call_output",
				CallID: codexFunctionCallID(part.ToolUseID),
				Output: content,
			})
			continue
		}
	}

	// Check if there were tool results
	hasToolResults := false
	for _, part := range msg.Content.Parts {
		if part.Type == "tool_result" {
			hasToolResults = true
			break
		}
	}
	if hasToolResults && role == "user" {
		// Tool results already added, skip empty user messages
		regularParts := 0
		for _, part := range msg.Content.Parts {
			if part.Type != "tool_result" {
				regularParts++
			}
		}
		if regularParts == 0 {
			return items, nil
		}
	}

	// Handle text content
	var texts []string
	var toolCalls []anthropic.ContentBlock
	for _, part := range msg.Content.Parts {
		switch part.Type {
		case "text":
			texts = append(texts, part.Text)
		case "tool_use":
			toolCalls = append(toolCalls, part)
		}
	}

	// Text message
	text := strings.Join(texts, "\n")
	if text != "" {
		items = append(items, InputItem{
			Type:    "message",
			Role:    role,
			Content: text,
		})
	}

	// Tool calls (assistant only)
	for _, tc := range toolCalls {
		args := string(tc.Input)
		if args == "" || args == "null" {
			args = "{}"
		}
		// Codex backend requires function_call IDs to start with "fc".
		// Anthropic sends IDs starting with "call_" — transform them.
		fcID := codexFunctionCallID(tc.ID)
		items = append(items, InputItem{
			Type:      "function_call",
			ID:        fcID,
			CallID:    fcID,
			Name:      tc.Name,
			Arguments: args,
		})
	}

	return items, nil
}
