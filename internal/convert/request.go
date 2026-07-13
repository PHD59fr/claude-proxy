package convert

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/claude-code-opencode/claude-proxy/internal/anthropic"
	"github.com/claude-code-opencode/claude-proxy/internal/openai"
)

// Request converts an Anthropic Messages request to OpenAI Chat Completions request.
func Request(req *anthropic.MessageRequest, defaultModel string) (*openai.ChatCompletionRequest, error) {
	if req == nil {
		return nil, fmt.Errorf("nil request")
	}

	model := req.Model
	if model == "" {
		model = defaultModel
	}

	oai := &openai.ChatCompletionRequest{
		Model:       model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		TopK:        req.TopK,
		Stream:      req.Stream,
	}

	if req.Stream {
		oai.StreamOptions = &openai.StreamOptions{
			IncludeUsage: true,
		}
	}

	// System message
	if len(req.System) > 0 {
		systemText := extractSystemPrompt(req.System)
		if systemText != "" {
			oai.Messages = append(oai.Messages, openai.ChatMessage{
				Role:    "system",
				Content: systemText,
			})
		}
	}

	// Stop sequences
	if len(req.StopSequences) > 0 {
		oai.Stop = req.StopSequences
	}

	// Convert messages
	for i, msg := range req.Messages {
		if err := validateMessage(&msg); err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
		oaiMsgs, err := convertMessage(&msg)
		if err != nil {
			return nil, fmt.Errorf("convert message: %w", err)
		}
		oai.Messages = append(oai.Messages, oaiMsgs...)
	}

	// Convert tools
	if len(req.Tools) > 0 {
		oai.Tools = make([]openai.Tool, 0, len(req.Tools))
		for _, t := range req.Tools {
			oai.Tools = append(oai.Tools, openai.Tool{
				Type: "function",
				Function: openai.Function{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.InputSchema,
				},
			})
		}
	}

	// Convert tool_choice
	if req.ToolChoice != nil {
		oai.ToolChoice = convertToolChoice(req.ToolChoice)
		if req.ToolChoice.DisableParallelToolUse {
			disabled := false
			oai.ParallelToolCalls = &disabled
		}
	}

	return oai, nil
}

func extractSystemPrompt(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try string
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try array of content blocks
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

func validateMessage(msg *anthropic.Message) error {
	if msg.Role != "user" && msg.Role != "assistant" {
		return fmt.Errorf("role %q is not supported", msg.Role)
	}
	for i, part := range msg.Content.Parts {
		switch part.Type {
		case "text", "tool_use", "tool_result":
		case "image":
			if part.Source == nil || part.Source.Type != "base64" || part.Source.MediaType == "" || part.Source.Data == "" {
				return fmt.Errorf("content[%d]: only base64 image sources are supported", i)
			}
		default:
			return fmt.Errorf("content[%d]: block type %q is not supported", i, part.Type)
		}
	}
	return nil
}

func convertMessage(msg *anthropic.Message) ([]openai.ChatMessage, error) {
	var result []openai.ChatMessage

	// Handle tool_result messages - these go as "tool" role
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
			result = append(result, openai.ChatMessage{
				Role:       "tool",
				Content:    content,
				ToolCallID: part.ToolUseID,
			})
			continue
		}
	}

	// If we had tool_results, those are already added above
	// Now handle the regular content blocks
	hasToolResults := false
	regularParts := make([]anthropic.ContentBlock, 0)
	for _, part := range msg.Content.Parts {
		if part.Type == "tool_result" {
			hasToolResults = true
		} else {
			regularParts = append(regularParts, part)
		}
	}

	if hasToolResults && msg.Role == "user" {
		// Tool results are already added as separate tool messages
		// Don't create a user message for them
		if len(regularParts) == 0 {
			return result, nil
		}
	}

	// Handle regular content
	switch msg.Role {
	case "user":
		text, images := classifyUserContent(regularParts)
		if text == "" && len(images) == 0 && len(regularParts) > 0 {
			// Fallback: serialize all parts as text
			texts := make([]string, 0)
			for _, p := range regularParts {
				if p.Type == "text" {
					texts = append(texts, p.Text)
				}
			}
			text = strings.Join(texts, "\n")
		}
		if len(images) > 0 {
			// Multimodal content
			oaiParts := make([]interface{}, 0)
			if text != "" {
				oaiParts = append(oaiParts, map[string]string{"type": "text", "text": text})
			}
			for _, img := range images {
				oaiParts = append(oaiParts, map[string]interface{}{
					"type": "image_url",
					"image_url": map[string]string{
						"url": fmt.Sprintf("data:%s;base64,%s", img.MediaType, img.Data),
					},
				})
			}
			result = append(result, openai.ChatMessage{Role: "user", Content: oaiParts})
		} else {
			result = append(result, openai.ChatMessage{
				Role:    "user",
				Content: text,
			})
		}

	case "assistant":
		text, toolCalls := classifyAssistantContent(regularParts)
		oaiMsg := openai.ChatMessage{
			Role: "assistant",
		}
		if text != "" && len(toolCalls) > 0 {
			// Both text and tool calls
			oaiMsg.Content = text
			oaiMsg.ToolCalls = toolCalls
		} else if len(toolCalls) > 0 {
			oaiMsg.ToolCalls = toolCalls
			oaiMsg.Content = ""
		} else {
			oaiMsg.Content = text
		}
		result = append(result, oaiMsg)
	}

	return result, nil
}

func classifyUserContent(parts []anthropic.ContentBlock) (string, []anthropic.ImageSource) {
	var texts []string
	var images []anthropic.ImageSource
	for _, p := range parts {
		switch p.Type {
		case "text":
			texts = append(texts, p.Text)
		case "image":
			if p.Source != nil {
				images = append(images, *p.Source)
			}
		}
	}
	return strings.Join(texts, "\n"), images
}

func classifyAssistantContent(parts []anthropic.ContentBlock) (string, []openai.ToolCall) {
	var texts []string
	var toolCalls []openai.ToolCall
	for _, p := range parts {
		switch p.Type {
		case "text":
			texts = append(texts, p.Text)
		case "tool_use":
			args := string(p.Input)
			if args == "" || args == "null" {
				args = "{}"
			}
			toolCalls = append(toolCalls, openai.ToolCall{
				ID:   p.ID,
				Type: "function",
				Function: openai.FuncCall{
					Name:      p.Name,
					Arguments: args,
				},
			})
		}
	}
	return strings.Join(texts, "\n"), toolCalls
}

func convertToolChoice(tc *anthropic.ToolChoice) interface{} {
	switch tc.Type {
	case "auto":
		return "auto"
	case "any":
		return map[string]string{"type": "required"}
	case "none":
		return "none"
	case "tool":
		return map[string]interface{}{
			"type":     "function",
			"function": map[string]string{"name": tc.Name},
		}
	default:
		return "auto"
	}
}
