package codex

import (
	"encoding/json"
	"strings"
)

// ResponsesRequest is the OpenAI Responses API request format used by the Codex backend.
type ResponsesRequest struct {
	Model        string          `json:"model"`
	Input        []InputItem     `json:"input,omitempty"`
	Instructions string          `json:"instructions,omitempty"`
	Tools        []ResponsesTool `json:"tools,omitempty"`
	Store        bool            `json:"store"`
	Stream       bool            `json:"stream"`
	Reasoning    *Reasoning      `json:"reasoning,omitempty"`
	Include      []string        `json:"include,omitempty"`
}

// Reasoning controls extended thinking behavior for Codex models.
type Reasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// InputItem is a polymorphic item in the Responses API input array.
type InputItem struct {
	Type string `json:"type"`

	// message fields
	Role    string      `json:"role,omitempty"`
	Content interface{} `json:"content,omitempty"`

	// function_call fields
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`

	// function_call_output fields
	CallID string `json:"call_id,omitempty"`
	Output string `json:"output,omitempty"`
}

// ResponsesTool is a tool definition in Responses API format.
//
// IMPORTANT: the OpenAI/Codex Responses API expects `parameters`, `description`
// and `strict` at the TOOL level (siblings of `type`/`name`), NOT wrapped in a
// `details` sub-object. Wrapping them caused the backend to ignore the schema
// and substitute an empty parameter set, so models emitted `{}` arguments and
// downstream clients (Claude Code) rejected the call as "Invalid tool parameters".
type ResponsesTool struct {
	Type        string      `json:"type"`
	Name        string      `json:"name,omitempty"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
	Strict      *bool       `json:"strict,omitempty"`
}

// codexNonStrict disables OpenAI "strict" mode so that arbitrary tool schemas
// from upstream clients (which may use keywords strict mode forbids) are
// passed through verbatim instead of being rejected/emptied by the backend.
var codexNonStrict = false

// --- Responses API SSE stream events ---

// ResponsesStreamChunk represents a parsed SSE chunk from the Codex backend.
type ResponsesStreamChunk struct {
	Chunk ResponsesEvent
	Done  bool
	Err   error
}

// ResponsesEvent is the top-level event from the Responses API stream.
type ResponsesEvent struct {
	Type         string             `json:"type"`
	Response     *ResponsesDoneBody `json:"response,omitempty"`
	Delta        json.RawMessage    `json:"delta,omitempty"`
	Item         *OutputItem        `json:"item,omitempty"`
	OutputIndex  int                `json:"output_index,omitempty"`
	ContentIndex int                `json:"content_index,omitempty"`
	// ItemID identifies the output item a delta belongs to
	// (used by response.function_call_arguments.delta events).
	ItemID string `json:"item_id,omitempty"`
}

// ResponsesDoneBody is the response payload in a response.done event.
type ResponsesDoneBody struct {
	ID     string       `json:"id"`
	Status string       `json:"status"`
	Output []OutputItem `json:"output,omitempty"`
	Usage  *Usage       `json:"usage,omitempty"`
}

// ResponseDelta is a text delta in a response.output_text.delta event.
type ResponseDelta struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// OutputItem is a single output item (message, function_call, etc.).
type OutputItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	Role      string          `json:"role,omitempty"`
	Status    string          `json:"status,omitempty"`
	Content   []OutputContent `json:"content,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Output    string          `json:"output,omitempty"`
}

// OutputContent is a content block within an output item.
type OutputContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Usage tracks token counts from the Responses API.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens,omitempty"`
}

// --- Canonical Codex model names ---

const (
	ModelGPT54      = "gpt-5.4"
	ModelGPT54Mini  = "gpt-5.4-mini"
	ModelGPT56      = "gpt-5.6"
	ModelGPT56Sol   = "gpt-5.6-sol"
	ModelGPT56Terra = "gpt-5.6-terra"
	ModelGPT56Luna  = "gpt-5.6-luna"
)

// CodexBackendURL is the base URL for the ChatGPT Codex backend.
const CodexBackendURL = "https://chatgpt.com/backend-api"

// CodexPath is the API path for Codex requests.
const CodexPath = "/codex/responses"

// KnownCodexModels is the set of canonical Codex model names.
var KnownCodexModels = map[string]bool{
	ModelGPT54:      true,
	ModelGPT54Mini:  true,
	ModelGPT56:      true,
	ModelGPT56Sol:   true,
	ModelGPT56Terra: true,
	ModelGPT56Luna:  true,
}

// ParseResponsesEvent parses a single SSE data line into a ResponsesEvent.
func ParseResponsesEvent(data string) (*ResponsesEvent, error) {
	var event ResponsesEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, err
	}
	return &event, nil
}

// IsCodexModel checks if a model name is a known Codex model.
func IsCodexModel(model string) bool {
	return KnownCodexModels[model]
}

// FindDoneBody returns the last response event that carries a populated
// response body. The Codex backend terminates its SSE stream with either
// "response.done" or "response.completed"; taking the last populated event
// handles both and ignores earlier partial events (e.g. response.in_progress).
func FindDoneBody(events []ResponsesEvent) *ResponsesDoneBody {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Response != nil {
			return events[i].Response
		}
	}
	return nil
}

// IsFailedStatus reports whether a Responses API status indicates the request
// did not produce a usable result and should fall back to another model.
func IsFailedStatus(status string) bool {
	switch status {
	case "failed", "cancelled":
		return true
	default:
		return false
	}
}

// ParseSSEEvents parses a complete SSE response body into individual events.
// Used for non-streaming responses where the Codex backend still sends SSE format.
func ParseSSEEvents(body []byte) []ResponsesEvent {
	var events []ResponsesEvent
	lines := strings.Split(string(body), "\n")
	var currentEvent string
	var dataLines []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if currentEvent != "" && len(dataLines) > 0 {
				data := strings.Join(dataLines, "\n")
				var evt ResponsesEvent
				if err := json.Unmarshal([]byte(data), &evt); err == nil {
					events = append(events, evt)
				}
			}
			currentEvent = ""
			dataLines = nil
			continue
		}
		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
		}
	}

	// Flush any pending event that lacked a trailing empty-line delimiter.
	if currentEvent != "" && len(dataLines) > 0 {
		data := strings.Join(dataLines, "\n")
		var evt ResponsesEvent
		if err := json.Unmarshal([]byte(data), &evt); err == nil {
			events = append(events, evt)
		}
	}
	return events
}
