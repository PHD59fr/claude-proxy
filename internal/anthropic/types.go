package anthropic

import "encoding/json"

// MessageRequest is the incoming Anthropic Messages API request.
type MessageRequest struct {
	Model         string          `json:"model"`
	Messages      []Message       `json:"messages"`
	System        json.RawMessage `json:"system,omitempty"`
	MaxTokens     int             `json:"max_tokens"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Tools         []Tool          `json:"tools,omitempty"`
	ToolChoice    *ToolChoice     `json:"tool_choice,omitempty"`
	Metadata      *Metadata       `json:"metadata,omitempty"`
	TopK          *int            `json:"top_k,omitempty"`
	Thinking      *ThinkingConfig `json:"thinking,omitempty"`
}

// ThinkingConfig controls extended thinking behavior.
type ThinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens *int   `json:"budget_tokens,omitempty"`
}

// Message is a single conversation message.
type Message struct {
	Role    string         `json:"role"`
	Content MessageContent `json:"content"`
}

// MessageContent handles both string and array content.
type MessageContent struct {
	Parts []ContentBlock
}

func (mc *MessageContent) UnmarshalJSON(data []byte) error {
	// Try string first
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		mc.Parts = []ContentBlock{{Type: "text", Text: s}}
		return nil
	}
	// Try array of content blocks
	var blocks []ContentBlock
	if err := json.Unmarshal(data, &blocks); err == nil {
		mc.Parts = blocks
		return nil
	}
	return json.Unmarshal(data, &mc.Parts)
}

func (mc MessageContent) MarshalJSON() ([]byte, error) {
	if len(mc.Parts) == 1 && mc.Parts[0].Type == "text" {
		return json.Marshal(mc.Parts[0].Text)
	}
	return json.Marshal(mc.Parts)
}

// ContentBlock is a polymorphic content block.
type ContentBlock struct {
	Type string `json:"type"`

	// text block
	Text string `json:"text,omitempty"`

	// image block
	Source *ImageSource `json:"source,omitempty"`

	// tool_use block
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result block
	ToolUseID string      `json:"tool_use_id,omitempty"`
	Content   interface{} `json:"content,omitempty"`
	IsError   bool        `json:"is_error,omitempty"`
}

func (cb *ContentBlock) UnmarshalJSON(data []byte) error {
	// Raw type for detecting
	type rawBlock struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Source    *ImageSource    `json:"source"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
		IsError   bool            `json:"is_error"`
	}
	var raw rawBlock
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	cb.Type = raw.Type
	cb.Text = raw.Text
	cb.Source = raw.Source
	cb.ID = raw.ID
	cb.Name = raw.Name
	cb.Input = raw.Input
	cb.ToolUseID = raw.ToolUseID
	cb.IsError = raw.IsError

	// For tool_result, content can be string or array
	if raw.Type == "tool_result" && len(raw.Content) > 0 {
		var s string
		if err := json.Unmarshal(raw.Content, &s); err == nil {
			cb.Content = s
			return nil
		}
		var blocks []ContentBlock
		if err := json.Unmarshal(raw.Content, &blocks); err == nil {
			cb.Content = blocks
			return nil
		}
	}

	// For tool_use in assistant messages, content may have inner blocks (rare but possible)
	// input is already a json.RawMessage, fine as-is

	return nil
}

// ImageSource represents an image in a content block.
type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// Tool represents a tool definition.
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	InputSchema interface{} `json:"input_schema"`
}

// ToolChoice controls tool selection.
type ToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

// Metadata for the request.
type Metadata struct {
	UserID string `json:"user_id,omitempty"`
}

// MessageResponse is the non-streaming Anthropic Messages response.
type MessageResponse struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Content      []ContentBlock `json:"content"`
	Model        string         `json:"model"`
	StopReason   string         `json:"stop_reason"`
	StopSequence *string        `json:"stop_sequence"`
	Usage        Usage          `json:"usage"`
}

// Usage tracks token counts.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// SSEEvent types for streaming
type SSEMessageStart struct {
	Type    string         `json:"type"`
	Message SSEMessageData `json:"message"`
}

type SSEMessageData struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content"`
	Model      string         `json:"model"`
	StopReason *string        `json:"stop_reason"`
	Usage      *Usage         `json:"usage"`
}

type SSEContentBlockStart struct {
	Type         string       `json:"type"`
	Index        int          `json:"index"`
	ContentBlock ContentBlock `json:"content_block"`
}

type SSEContentBlockDelta struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta Delta  `json:"delta"`
}

type Delta struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// For tool use
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	// For input_json_delta
	PartialJSON string `json:"partial_json,omitempty"`
}

type SSEContentBlockStop struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

type SSEMessageDelta struct {
	Type  string   `json:"type"`
	Delta MsgDelta `json:"delta"`
	Usage *Usage   `json:"usage,omitempty"`
}

type MsgDelta struct {
	StopReason   string  `json:"stop_reason"`
	StopSequence *string `json:"stop_sequence,omitempty"`
}

type SSEMessageStop struct {
	Type string `json:"type"`
}

type SSEPing struct {
	Type string `json:"type"`
}
