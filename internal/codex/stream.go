package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/ioutil"
)

// CodexStreamChunk represents a parsed chunk from the Codex SSE stream.
type CodexStreamChunk struct {
	// TextDelta is a text content delta (for streaming text)
	TextDelta string
	// ToolCallStart is emitted when a new function_call starts
	ToolCallStart *OutputItem
	// ToolCallIndex is the output index of the tool call started by ToolCallStart.
	ToolCallIndex int
	// ToolCallDelta is emitted for function_call argument deltas
	ToolCallDelta *ToolCallDeltaInfo
	// Done is set when the stream is complete, with the final response
	Done *ResponsesDoneBody
	// Err is set on errors
	Err error
}

// ToolCallDeltaInfo carries incremental tool call data.
type ToolCallDeltaInfo struct {
	Index     int
	CallID    string
	Name      string
	Arguments string
}

// StreamIdleTimeout is the default idle timeout applied to Codex streams
// when the request timeout configuration is unset or invalid.
const StreamIdleTimeout = 30 * time.Second

// ParseCodexStream reads the Codex backend SSE stream and yields chunks.
func ParseCodexStream(ctx context.Context, reader io.Reader) <-chan CodexStreamChunk {
	return parseCodexStreamInternal(ctx, reader, StreamIdleTimeout)
}

// ParseCodexStreamWithTimeout is like ParseCodexStream but uses the supplied
// idle timeout, typically the configured request timeout.
func ParseCodexStreamWithTimeout(ctx context.Context, reader io.Reader, idleTimeout time.Duration) <-chan CodexStreamChunk {
	if idleTimeout <= 0 {
		idleTimeout = StreamIdleTimeout
	}
	return parseCodexStreamInternal(ctx, reader, idleTimeout)
}

// parseCodexStreamInternal is the core implementation with a configurable idle timeout.
func parseCodexStreamInternal(ctx context.Context, reader io.Reader, idleTimeout time.Duration) <-chan CodexStreamChunk {
	ch := make(chan CodexStreamChunk, 16)
	lines := make(chan string, 16)
	done := make(chan struct{})

	r := ioutil.NewIdleTimeoutReader(reader, idleTimeout)

	// Goroutine 1: read lines from the scanner (blocking I/O).
	go func() {
		defer close(lines)
		defer func() { _ = r.Close() }()
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-done:
				return
			}
		}
		if err := scanner.Err(); err != nil && err != io.EOF {
			select {
			case lines <- fmt.Sprintf("__ERROR__%s", err.Error()):
			case <-done:
			}
		} else if r.IsTimedOut() {
			select {
			case lines <- fmt.Sprintf("__ERROR__upstream stalled: no data for %v", idleTimeout):
			case <-done:
			}
		}
	}()

	// Goroutine 2: parse SSE lines, respects context cancellation.
	go func() {
		defer close(ch)
		defer close(done)
		defer func() { _ = r.Close() }()

		var currentEvent string
		var dataLines []string
		// streamedArgs tracks which function-call items have had at least one
		// argument delta streamed, so we can fall back to the final arguments
		// in response.done only when necessary.
		streamedArgs := make(map[string]bool)
		// itemIndex maps a function-call item ID to its output index, used by
		// the response.done fallback to align arguments with the started block.
		itemIndex := make(map[string]int)
		// streamedText tracks which message item IDs have had text deltas
		// streamed, so the response.done fallback does not re-emit them.
		streamedText := make(map[string]bool)

		for {
			select {
			case <-ctx.Done():
				ch <- CodexStreamChunk{Err: ctx.Err()}
				return
			case line, ok := <-lines:
				if !ok {
					// Channel closed: flush any pending event that lacked a
					// trailing empty-line delimiter (bufio.Scanner does not
					// emit a final empty line for inputs ending with \n).
					if currentEvent != "" && len(dataLines) > 0 {
						data := strings.Join(dataLines, "\n")
						processCodexEvent(ch, currentEvent, data, streamedArgs, itemIndex, streamedText)
					}
					return
				}
				if strings.HasPrefix(line, "__ERROR__") {
					ch <- CodexStreamChunk{Err: fmt.Errorf("scanner error: %s", strings.TrimPrefix(line, "__ERROR__"))}
					return
				}
				line = strings.TrimSpace(line)

				// Empty line = end of SSE event, process accumulated data
				if line == "" {
					if currentEvent != "" && len(dataLines) > 0 {
						data := strings.Join(dataLines, "\n")
						processCodexEvent(ch, currentEvent, data, streamedArgs, itemIndex, streamedText)
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
		}
	}()

	return ch
}

// processCodexEvent handles a single Codex SSE event. streamedArgs records
// which function-call item IDs have received at least one argument delta so
// the response.done fallback only supplies arguments when they were not
// already streamed incrementally. itemIndex records each function-call item's
// output index so the fallback can align arguments with the started block.
// streamedText records message item IDs whose text was already streamed via
// response.output_text.delta so the response.done fallback does not duplicate it.
func processCodexEvent(ch chan<- CodexStreamChunk, eventType, data string, streamedArgs map[string]bool, itemIndex map[string]int, streamedText map[string]bool) {
	switch eventType {
	case "response.done":
		var event ResponsesEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			ch <- CodexStreamChunk{Err: fmt.Errorf("parse response.done: %w", err)}
			return
		}
		if event.Response != nil {
			// Fallback: some backends only include the full arguments in the
			// final response. Emit them for any function call whose arguments
			// were not already streamed incrementally.
			//
			// We track a monotonically increasing index for emitted tool calls
			// so that a function_call whose itemIndex was never populated (e.g.
			// a backend that streams arguments deltas without a preceding
			// output_item.added) still gets a unique, collision-free index
			// instead of defaulting to 0.
			fallbackIdx := 0
			for _, item := range event.Response.Output {
				if item.Type != "function_call" {
					// Emit text from message output items so it is not lost
					// when response.output_text.delta events are absent.
					if item.Type == "message" && !streamedText[item.ID] {
						for _, c := range item.Content {
							if c.Type == "output_text" && c.Text != "" {
								ch <- CodexStreamChunk{TextDelta: c.Text}
							}
						}
					}
					continue
				}
				if streamedArgs[item.ID] {
					continue
				}
				if item.Arguments != "" && item.Arguments != "{}" {
					idx, ok := itemIndex[item.ID]
					if !ok {
						idx = fallbackIdx
						fallbackIdx++
					}
					ch <- CodexStreamChunk{ToolCallDelta: &ToolCallDeltaInfo{
						Index:     idx,
						CallID:    item.ID,
						Name:      item.Name,
						Arguments: item.Arguments,
					}}
				}
			}
			ch <- CodexStreamChunk{Done: event.Response}
		}

	case "response.output_text.delta":
		var event ResponsesEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return // skip malformed deltas
		}
		if len(event.Delta) > 0 {
			// The OpenAI Responses API sends delta as a plain string,
			// but some backends may send it as an object with a "text" field.
			// Try string first, then object.
			var textStr string
			if err := json.Unmarshal(event.Delta, &textStr); err == nil && textStr != "" {
				if event.ItemID != "" {
					streamedText[event.ItemID] = true
				}
				ch <- CodexStreamChunk{TextDelta: textStr}
			} else {
				var rd ResponseDelta
				if err := json.Unmarshal(event.Delta, &rd); err == nil && rd.Text != "" {
					if event.ItemID != "" {
						streamedText[event.ItemID] = true
					}
					ch <- CodexStreamChunk{TextDelta: rd.Text}
				}
			}
		}

	case "response.output_item.added":
		var event ResponsesEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return
		}
		if event.Item != nil && event.Item.Type == "function_call" {
			itemIndex[event.Item.ID] = event.OutputIndex
			ch <- CodexStreamChunk{ToolCallStart: event.Item, ToolCallIndex: event.OutputIndex}
		}

	case "response.function_call_arguments.delta":
		var event ResponsesEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return // skip malformed deltas
		}
		if event.ItemID == "" {
			return
		}
		var delta string
		if len(event.Delta) > 0 {
			if err := json.Unmarshal(event.Delta, &delta); err != nil {
				return
			}
		}
		if delta == "" {
			return
		}
		streamedArgs[event.ItemID] = true
		ch <- CodexStreamChunk{ToolCallDelta: &ToolCallDeltaInfo{
			Index:     event.OutputIndex,
			CallID:    event.ItemID,
			Arguments: delta,
		}}

	case "response.output_item.done":
		// Could be used for completion signals, but we handle completion via response.done

	case "response.in_progress", "response.created":
		// Acknowledgment events, no action needed

	default:
		// Unknown events are silently skipped
	}
}
