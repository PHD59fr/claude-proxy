package convert

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/claude-code-opencode/claude-proxy/internal/anthropic"
	"github.com/claude-code-opencode/claude-proxy/internal/openai"
)

// Flusher is the interface required for SSE flushing.
type Flusher interface {
	Flush()
}

// StreamConverter converts OpenAI streaming chunks to Anthropic SSE events.
// It properly tracks multiple parallel tool calls by index.
type StreamConverter struct {
	w        io.Writer
	flusher  Flusher
	model    string
	msgID    string
	blockIdx int
	started  bool
	finished bool // true after message_delta has been sent

	// Text block state
	textOpen bool

	// Usage tracking
	inputTokens  int
	outputTokens int

	// Tool call tracking: OpenAI streams tool calls incrementally by index.
	// We track each tool call's accumulated state.
	toolCalls map[int]*toolCallState
}

type toolCallState struct {
	id         string
	name       string
	arguments  strings.Builder
	blockIndex int
	started    bool
	closed     bool
}

// NewStreamConverter creates a new stream converter that writes to w.
// If w implements Flusher (e.g. http.ResponseWriter), events are flushed immediately.
func NewStreamConverter(w io.Writer, model, msgID string) *StreamConverter {
	sc := &StreamConverter{
		w:         w,
		model:     model,
		msgID:     msgID,
		toolCalls: make(map[int]*toolCallState),
	}
	if f, ok := w.(Flusher); ok {
		sc.flusher = f
	}
	return sc
}

// Convert reads chunks from the channel and writes Anthropic SSE events to w.
func (sc *StreamConverter) Convert(chunks <-chan openai.StreamChunk) error {
	for chunk := range chunks {
		if chunk.Err != nil {
			return chunk.Err
		}

		if chunk.Done {
			// A Messages request has exactly one lifecycle. Codex can expose
			// intermediate response.done events; defer completion until the
			// upstream channel closes so a later delta cannot reopen a message.
			continue
		}

		c := chunk.Chunk

		if !sc.started {
			if err := sc.writeMessageStart(c.Model); err != nil {
				return err
			}
			if err := sc.writePing(); err != nil {
				return err
			}
			sc.started = true
		}

		if len(c.Choices) == 0 {
			// Usage-only chunk (no choices) — capture token counts
			if c.Usage != nil {
				sc.inputTokens = c.Usage.PromptTokens
				sc.outputTokens = c.Usage.CompletionTokens
			}
			continue
		}

		choice := c.Choices[0]
		delta := choice.Delta

		// Text content delta
		if delta.Content != "" {
			if err := sc.ensureTextBlock(); err != nil {
				return err
			}
			if err := sc.writeTextDelta(delta.Content); err != nil {
				return err
			}
		}

		// Tool call deltas — OpenAI sends these incrementally by index
		for _, tc := range delta.ToolCalls {
			if err := sc.handleToolCallDelta(&tc); err != nil {
				return err
			}
		}

		// Finish reason on last chunk
		if choice.FinishReason != nil {
			if !sc.finished {
				if err := sc.closeOpenBlocks(); err != nil {
					return err
				}
				stopReason := mapFinishReason(choice.FinishReason)
				if err := sc.writeMessageDelta(stopReason); err != nil {
					return err
				}
				sc.finished = true
			}
		}
	}

	// Channel closed — finish gracefully
	return sc.finish()
}

// finish sends message_delta + message_stop if not already sent.
func (sc *StreamConverter) finish() error {
	if !sc.started {
		// No data received at all — send minimal response
		if err := sc.writeMessageStart(sc.model); err != nil {
			return err
		}
		if err := sc.writePing(); err != nil {
			return err
		}
	}

	if err := sc.closeOpenBlocks(); err != nil {
		return err
	}
	if !sc.finished {
		if err := sc.writeMessageDelta("end_turn"); err != nil {
			return err
		}
		sc.finished = true
	}
	return sc.writeMessageStop()
}

// ensureTextBlock opens a text content block if one isn't already open.
func (sc *StreamConverter) ensureTextBlock() error {
	if sc.textOpen {
		return nil
	}
	// Close any open tool call blocks first
	if err := sc.closeOpenBlocks(); err != nil {
		return err
	}
	if err := sc.writeContentBlockStart("text"); err != nil {
		return err
	}
	sc.textOpen = true
	return nil
}

// handleToolCallDelta processes an incremental tool call delta from OpenAI.
func (sc *StreamConverter) handleToolCallDelta(tc *openai.ToolCallDelta) error {
	idx := tc.Index

	state, exists := sc.toolCalls[idx]
	if !exists {
		// New tool call — close text block if open, close any previously complete tool calls
		if sc.textOpen {
			if err := sc.writeContentBlockStop(); err != nil {
				return err
			}
			sc.textOpen = false
		}

		state = &toolCallState{
			blockIndex: sc.blockIdx,
			started:    false,
		}
		sc.toolCalls[idx] = state
	}

	// Accumulate ID
	if tc.ID != "" {
		state.id = tc.ID
	}

	// Accumulate function name
	if tc.Function.Name != "" {
		state.name = tc.Function.Name
	}

	// Start the content block once we have both ID and name
	if !state.started && state.id != "" && state.name != "" {
		// Close any previously complete tool call blocks (those with a lower index
		// that haven't been updated in this chunk)
		if err := sc.closePreviousToolCalls(idx); err != nil {
			return err
		}
		if err := sc.writeToolUseBlockStart(state.blockIndex, state.id, state.name); err != nil {
			return err
		}
		state.started = true
	}

	// Accumulate arguments
	if tc.Function.Arguments != "" {
		state.arguments.WriteString(tc.Function.Arguments)
		if err := sc.writeToolInputDelta(state.blockIndex, tc.Function.Arguments); err != nil {
			return err
		}
	}

	return nil
}

// closePreviousToolCalls closes tool call blocks with index < current that are already started.
func (sc *StreamConverter) closePreviousToolCalls(currentIdx int) error {
	for idx, state := range sc.toolCalls {
		if idx < currentIdx && state.started && !state.closed {
			if err := sc.writeContentBlockStopAt(state.blockIndex); err != nil {
				return err
			}
			state.closed = true
		}
	}
	return nil
}

// closeOpenBlocks closes all open content blocks.
func (sc *StreamConverter) closeOpenBlocks() error {
	// Close text block
	if sc.textOpen {
		if err := sc.writeContentBlockStop(); err != nil {
			return err
		}
		sc.textOpen = false
	}

	// Close all tool call blocks (sorted by blockIndex)
	var indices []int
	for idx := range sc.toolCalls {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	for _, idx := range indices {
		state := sc.toolCalls[idx]
		if state.started && !state.closed {
			if err := sc.writeContentBlockStopAt(state.blockIndex); err != nil {
				return err
			}
			state.closed = true
		}
	}
	return nil
}

// writeSSE writes an SSE event and flushes if possible.
func (sc *StreamConverter) writeSSE(event string, data interface{}) error {
	var sb strings.Builder
	if event != "" {
		sb.WriteString("event: ")
		sb.WriteString(event)
		sb.WriteString("\n")
	}
	sb.WriteString("data: ")
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("marshal SSE data: %w", err)
		}
		sb.Write(b)
	}
	sb.WriteString("\n\n")
	_, err := io.WriteString(sc.w, sb.String())
	if err != nil {
		return err
	}
	if sc.flusher != nil {
		sc.flusher.Flush()
	}
	return nil
}

func (sc *StreamConverter) writeMessageStart(model string) error {
	if model == "" {
		model = sc.model
	}
	msg := anthropic.SSEMessageStart{
		Type: "message_start",
		Message: anthropic.SSEMessageData{
			ID:      sc.msgID,
			Type:    "message",
			Role:    "assistant",
			Model:   model,
			Content: []anthropic.ContentBlock{},
			Usage: &anthropic.Usage{
				InputTokens: sc.inputTokens,
			},
		},
	}
	return sc.writeSSE("message_start", msg)
}

func (sc *StreamConverter) writePing() error {
	return sc.writeSSE("ping", anthropic.SSEPing{Type: "ping"})
}

func (sc *StreamConverter) writeContentBlockStart(blockType string) error {
	block := anthropic.SSEContentBlockStart{
		Type:  "content_block_start",
		Index: sc.blockIdx,
		ContentBlock: anthropic.ContentBlock{
			Type: blockType,
		},
	}
	sc.blockIdx++
	return sc.writeSSE("content_block_start", block)
}

func (sc *StreamConverter) writeToolUseBlockStart(blockIdx int, id, name string) error {
	block := anthropic.SSEContentBlockStart{
		Type:  "content_block_start",
		Index: blockIdx,
		ContentBlock: anthropic.ContentBlock{
			Type:  "tool_use",
			ID:    id,
			Name:  name,
			Input: json.RawMessage("{}"),
		},
	}
	sc.blockIdx++
	return sc.writeSSE("content_block_start", block)
}

func (sc *StreamConverter) writeTextDelta(text string) error {
	// The text block lives at index blockIdx-1. ensureTextBlock opens it before
	// any delta is written; guard against a stray call so we never emit a
	// negative or wrong index if the contract is violated.
	if !sc.textOpen || sc.blockIdx == 0 {
		return fmt.Errorf("writeTextDelta called without an open text block")
	}
	delta := anthropic.SSEContentBlockDelta{
		Type:  "content_block_delta",
		Index: sc.blockIdx - 1, // The block we're writing into
		Delta: anthropic.Delta{
			Type: "text_delta",
			Text: text,
		},
	}
	return sc.writeSSE("content_block_delta", delta)
}

func (sc *StreamConverter) writeToolInputDelta(blockIdx int, partialJSON string) error {
	delta := anthropic.SSEContentBlockDelta{
		Type:  "content_block_delta",
		Index: blockIdx,
		Delta: anthropic.Delta{
			Type:        "input_json_delta",
			PartialJSON: partialJSON,
		},
	}
	return sc.writeSSE("content_block_delta", delta)
}

func (sc *StreamConverter) writeContentBlockStop() error {
	return sc.writeContentBlockStopAt(sc.blockIdx - 1)
}

func (sc *StreamConverter) writeContentBlockStopAt(blockIdx int) error {
	stop := anthropic.SSEContentBlockStop{
		Type:  "content_block_stop",
		Index: blockIdx,
	}
	return sc.writeSSE("content_block_stop", stop)
}

func (sc *StreamConverter) writeMessageDelta(stopReason string) error {
	delta := anthropic.SSEMessageDelta{
		Type: "message_delta",
		Delta: anthropic.MsgDelta{
			StopReason: stopReason,
		},
		Usage: &anthropic.Usage{
			OutputTokens: sc.outputTokens,
		},
	}
	return sc.writeSSE("message_delta", delta)
}

func (sc *StreamConverter) writeMessageStop() error {
	return sc.writeSSE("message_stop", anthropic.SSEMessageStop{Type: "message_stop"})
}
