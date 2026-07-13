package convert

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/claude-code-opencode/claude-proxy/internal/openai"
)

// mockFlusher tracks flush calls
type mockFlusher struct {
	flushCount int
}

func (f *mockFlusher) Flush() { f.flushCount++ }

// writeStreamer wraps a bytes.Buffer and a mockFlusher
type writeStreamer struct {
	*bytes.Buffer
	*mockFlusher
}

func newTestWriter() *writeStreamer {
	return &writeStreamer{
		&bytes.Buffer{},
		&mockFlusher{},
	}
}

func makeChunk(model string, choices []openai.ChunkChoice) openai.StreamChunk {
	return openai.StreamChunk{
		Chunk: openai.ChatCompletionChunk{
			Model:   model,
			Choices: choices,
		},
	}
}

func TestStreamConverter_BasicText(t *testing.T) {
	w := newTestWriter()
	sc := NewStreamConverter(w, "big-pickle", "msg_test")

	finishReason := "stop"
	ch := make(chan openai.StreamChunk, 16)
	go func() {
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, Delta: openai.ChatDelta{Role: "assistant"}},
		})
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, Delta: openai.ChatDelta{Content: "Hello"}},
		})
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, Delta: openai.ChatDelta{Content: " world"}, FinishReason: &finishReason},
		})
		close(ch)
	}()

	err := sc.Convert(ch)
	if err != nil {
		t.Fatal(err)
	}

	output := w.String()

	events := parseSSEEvents(output)
	assertEventOrder(t, events, []string{
		"message_start",
		"ping",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	})

	if !strings.Contains(output, `"text":"Hello"`) {
		t.Error("missing Hello text delta")
	}
	if !strings.Contains(output, `"text":" world"`) {
		t.Error("missing ' world' text delta")
	}
}

func TestStreamConverter_ToolCall(t *testing.T) {
	w := newTestWriter()
	sc := NewStreamConverter(w, "big-pickle", "msg_tool")

	ch := make(chan openai.StreamChunk, 16)
	go func() {
		// Tool call start
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, Delta: openai.ChatDelta{
				ToolCalls: []openai.ToolCallDelta{
					{Index: 0, ID: "call_123", Type: "function", Function: openai.FuncDelta{Name: "get_weather"}},
				},
			}},
		})
		// Arguments chunk 1
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, Delta: openai.ChatDelta{
				ToolCalls: []openai.ToolCallDelta{
					{Index: 0, Function: openai.FuncDelta{Arguments: `{"loc`}},
				},
			}},
		})
		// Arguments chunk 2
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, Delta: openai.ChatDelta{
				ToolCalls: []openai.ToolCallDelta{
					{Index: 0, Function: openai.FuncDelta{Arguments: `":"Paris"}`}},
				},
			}},
		})
		// Finish
		finishReason := "tool_calls"
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, FinishReason: &finishReason},
		})
		close(ch)
	}()

	err := sc.Convert(ch)
	if err != nil {
		t.Fatal(err)
	}

	output := w.String()

	events := parseSSEEvents(output)
	assertEventOrder(t, events, []string{
		"message_start",
		"ping",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	})

	if !strings.Contains(output, `"type":"tool_use"`) {
		t.Error("missing tool_use content_block_start")
	}
	if !strings.Contains(output, `"id":"call_123"`) {
		t.Error("missing tool call id")
	}
	if !strings.Contains(output, `"name":"get_weather"`) {
		t.Error("missing tool call name")
	}
	if !strings.Contains(output, `"type":"input_json_delta"`) {
		t.Error("missing input_json_delta")
	}
}

func TestStreamConverter_MultipleToolCalls(t *testing.T) {
	w := newTestWriter()
	sc := NewStreamConverter(w, "big-pickle", "msg_multi")

	ch := make(chan openai.StreamChunk, 16)
	go func() {
		// Tool call 0 starts
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, Delta: openai.ChatDelta{
				ToolCalls: []openai.ToolCallDelta{
					{Index: 0, ID: "call_a", Type: "function", Function: openai.FuncDelta{Name: "tool_a"}},
				},
			}},
		})
		// Tool call 1 starts
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, Delta: openai.ChatDelta{
				ToolCalls: []openai.ToolCallDelta{
					{Index: 1, ID: "call_b", Type: "function", Function: openai.FuncDelta{Name: "tool_b"}},
				},
			}},
		})
		// Args for tool call 0
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, Delta: openai.ChatDelta{
				ToolCalls: []openai.ToolCallDelta{
					{Index: 0, Function: openai.FuncDelta{Arguments: `{"a":1}`}},
				},
			}},
		})
		// Args for tool call 1
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, Delta: openai.ChatDelta{
				ToolCalls: []openai.ToolCallDelta{
					{Index: 1, Function: openai.FuncDelta{Arguments: `{"b":2}`}},
				},
			}},
		})
		// Finish
		finishReason := "tool_calls"
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, FinishReason: &finishReason},
		})
		close(ch)
	}()

	err := sc.Convert(ch)
	if err != nil {
		t.Fatal(err)
	}

	output := w.String()

	count := strings.Count(output, `"type":"tool_use"`)
	if count != 2 {
		t.Errorf("tool_use count = %d, want 2", count)
	}

	count = strings.Count(output, `"content_block_start"`)
	if count != 2 {
		t.Errorf("content_block_start count = %d, want 2", count)
	}

	count = strings.Count(output, `"content_block_stop"`)
	if count != 2 {
		t.Errorf("content_block_stop count = %d, want 2", count)
	}
}

func TestStreamConverter_TextThenToolCall(t *testing.T) {
	w := newTestWriter()
	sc := NewStreamConverter(w, "big-pickle", "msg_mixed")

	ch := make(chan openai.StreamChunk, 16)
	go func() {
		// Text content
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, Delta: openai.ChatDelta{Content: "Let me check."}},
		})
		// Tool call
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, Delta: openai.ChatDelta{
				ToolCalls: []openai.ToolCallDelta{
					{Index: 0, ID: "call_mixed", Type: "function", Function: openai.FuncDelta{Name: "search", Arguments: "{}"}},
				},
			}},
		})
		// Finish
		finishReason := "tool_calls"
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, FinishReason: &finishReason},
		})
		close(ch)
	}()

	err := sc.Convert(ch)
	if err != nil {
		t.Fatal(err)
	}

	output := w.String()

	events := parseSSEEvents(output)
	assertEventOrder(t, events, []string{
		"message_start",
		"ping",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"content_block_start",
		"content_block_stop",
		"message_delta",
		"message_stop",
	})
}

func TestStreamConverter_UpstreamDisconnect(t *testing.T) {
	w := newTestWriter()
	sc := NewStreamConverter(w, "big-pickle", "msg_disconnect")

	ch := make(chan openai.StreamChunk, 16)
	go func() {
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, Delta: openai.ChatDelta{Content: "partial"}},
		})
		close(ch)
	}()

	err := sc.Convert(ch)
	if err != nil {
		t.Fatal(err)
	}

	output := w.String()
	if !strings.Contains(output, `"message_stop"`) {
		t.Error("missing message_stop on upstream disconnect")
	}
}

func TestStreamConverter_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	w := newTestWriter()

	ch := make(chan openai.StreamChunk, 16)
	go func() {
		ch <- openai.StreamChunk{Err: ctx.Err()}
		close(ch)
	}()

	sc := NewStreamConverter(w, "big-pickle", "msg_cancel")
	err := sc.Convert(ch)
	if err == nil {
		t.Error("expected error for context cancellation")
	}
}

func TestStreamConverter_EmptyStream(t *testing.T) {
	w := newTestWriter()
	sc := NewStreamConverter(w, "big-pickle", "msg_empty")

	ch := make(chan openai.StreamChunk, 16)
	close(ch)

	err := sc.Convert(ch)
	if err != nil {
		t.Fatal(err)
	}

	output := w.String()
	if !strings.Contains(output, `"message_start"`) {
		t.Error("missing message_start for empty stream")
	}
	if !strings.Contains(output, `"message_stop"`) {
		t.Error("missing message_stop for empty stream")
	}
}

func TestStreamConverter_FlusherCalled(t *testing.T) {
	w := newTestWriter()
	sc := NewStreamConverter(w, "big-pickle", "msg_flush")

	ch := make(chan openai.StreamChunk, 16)
	go func() {
		finishReason := "stop"
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, Delta: openai.ChatDelta{Content: "hi"}, FinishReason: &finishReason},
		})
		close(ch)
	}()

	err := sc.Convert(ch)
	if err != nil {
		t.Fatal(err)
	}

	if w.flushCount == 0 {
		t.Error("Flush was never called")
	}
}

func TestStreamConverter_SSEFormat(t *testing.T) {
	w := newTestWriter()
	sc := NewStreamConverter(w, "big-pickle", "msg_sse")

	ch := make(chan openai.StreamChunk, 16)
	go func() {
		finishReason := "stop"
		ch <- makeChunk("big-pickle", []openai.ChunkChoice{
			{Index: 0, Delta: openai.ChatDelta{Content: "test"}, FinishReason: &finishReason},
		})
		close(ch)
	}()

	_ = sc.Convert(ch)

	output := w.String()

	for _, block := range strings.Split(output, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		if !strings.HasPrefix(block, "data: ") && !strings.HasPrefix(block, "event: ") {
			t.Errorf("SSE block missing expected prefix: %q", block[:min(50, len(block))])
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// parseSSEEvents extracts the event type from each SSE event in the output
func parseSSEEvents(output string) []string {
	var events []string
	for _, block := range strings.Split(output, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		eventType := "message_start" // default event type
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "event: ") {
				eventType = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				var obj struct {
					Type string `json:"type"`
				}
				if json.Unmarshal([]byte(data), &obj) == nil && obj.Type != "" {
					eventType = obj.Type
				}
			}
		}
		events = append(events, eventType)
	}
	return events
}

// assertEventOrder checks that expected events appear in order in the actual events
func assertEventOrder(t *testing.T, actual, expected []string) {
	t.Helper()
	if len(actual) < len(expected) {
		t.Errorf("too few events: got %d, want at least %d", len(actual), len(expected))
		t.Errorf("actual: %v", actual)
		t.Errorf("expected: %v", expected)
		return
	}

	j := 0
	for i := 0; i < len(actual) && j < len(expected); i++ {
		if actual[i] == expected[j] {
			j++
		}
	}

	if j != len(expected) {
		t.Errorf("event order mismatch")
		t.Errorf("actual events:   %v", actual)
		t.Errorf("expected events: %v", expected)
	}
}
