package codex

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// stallReader blocks on Read until Close is called or the context is cancelled.
type stallReader struct {
	closed chan struct{}
}

func newStallReader() *stallReader {
	return &stallReader{closed: make(chan struct{})}
}

func (r *stallReader) Read(p []byte) (int, error) {
	<-r.closed
	return 0, io.EOF
}

func (r *stallReader) Close() error {
	select {
	case <-r.closed:
	default:
		close(r.closed)
	}
	return nil
}

func TestParseCodexStream_TextOnly(t *testing.T) {
	// Realistic streaming: text arrives via response.output_text.delta with an
	// item_id, and the response.done body also carries the full text. The text
	// from response.done must NOT be re-emitted (no duplication).
	input := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
		"",
		"event: response.in_progress",
		`data: {"type":"response.in_progress","response":{"id":"resp_1","status":"in_progress"}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hello"}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":" world"}`,
		"",
		"event: response.done",
		`data: {"type":"response.done","response":{"id":"resp_1","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Hello world"}]}]}}`,
		"",
	}, "\n")

	ctx := context.Background()
	ch := ParseCodexStream(ctx, strings.NewReader(input))

	var chunks []CodexStreamChunk
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}

	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3 (2 text deltas + 1 done)", len(chunks))
	}

	// First text delta
	if chunks[0].TextDelta != "Hello" {
		t.Errorf("chunk[0].TextDelta = %q, want %q", chunks[0].TextDelta, "Hello")
	}
	if chunks[0].Done != nil {
		t.Error("chunk[0] should not be Done")
	}

	// Second text delta
	if chunks[1].TextDelta != " world" {
		t.Errorf("chunk[1].TextDelta = %q, want %q", chunks[1].TextDelta, " world")
	}

	// Done chunk
	if chunks[2].Done == nil {
		t.Fatal("chunk[2] should be Done")
	}
	if chunks[2].Done.ID != "resp_1" {
		t.Errorf("Done.ID = %q, want %q", chunks[2].Done.ID, "resp_1")
	}
	if chunks[2].Done.Status != "completed" {
		t.Errorf("Done.Status = %q, want %q", chunks[2].Done.Status, "completed")
	}
	if len(chunks[2].Done.Output) != 1 {
		t.Fatalf("Done.Output length = %d, want 1", len(chunks[2].Done.Output))
	}
	if chunks[2].Done.Output[0].Type != "message" {
		t.Errorf("Done.Output[0].Type = %q, want %q", chunks[2].Done.Output[0].Type, "message")
	}
}

func TestParseCodexStream_TextOnlyInDone(t *testing.T) {
	// Fallback case: no response.output_text.delta events are streamed, so the
	// text must be extracted from the response.done body.
	input := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_2","status":"in_progress"}}`,
		"",
		"event: response.done",
		`data: {"type":"response.done","response":{"id":"resp_2","status":"completed","output":[{"type":"message","id":"msg_2","role":"assistant","content":[{"type":"output_text","text":"The answer is 42"}]}]}}`,
		"",
	}, "\n")

	ctx := context.Background()
	ch := ParseCodexStream(ctx, strings.NewReader(input))

	var chunks []CodexStreamChunk
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}

	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2 (1 done-body text + 1 done)", len(chunks))
	}

	if chunks[0].TextDelta != "The answer is 42" {
		t.Errorf("chunk[0].TextDelta = %q, want %q", chunks[0].TextDelta, "The answer is 42")
	}
	if chunks[1].Done == nil {
		t.Fatal("chunk[1] should be Done")
	}
}

func TestParseCodexStream_TextDeltaStringFormat(t *testing.T) {
	// The OpenAI Responses API sends delta as a plain string, not an object.
	input := strings.Join([]string{
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","item_id":"msg_3","output_index":0,"content_index":0,"delta":"plain string delta"}`,
		"",
	}, "\n")

	ctx := context.Background()
	ch := ParseCodexStream(ctx, strings.NewReader(input))

	var chunks []CodexStreamChunk
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}

	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(chunks))
	}
	if chunks[0].TextDelta != "plain string delta" {
		t.Errorf("TextDelta = %q, want %q", chunks[0].TextDelta, "plain string delta")
	}
}

func TestParseCodexStream_ToolCalls(t *testing.T) {
	input := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_tc","status":"in_progress"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","item":{"type":"function_call","id":"call_42","name":"get_weather","call_id":"fc_1"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","item":{"type":"function_call","id":"call_43","name":"get_time","call_id":"fc_2"}}`,
		"",
		"event: response.done",
		`data: {"type":"response.done","response":{"id":"resp_tc","status":"completed","output":[{"type":"function_call","id":"call_42","name":"get_weather","arguments":"{\"city\":\"Paris\"}"},{"type":"function_call","id":"call_43","name":"get_time","arguments":"{\"tz\":\"CET\"}"}]}}`,
		"",
	}, "\n")

	ctx := context.Background()
	ch := ParseCodexStream(ctx, strings.NewReader(input))

	var chunks []CodexStreamChunk
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}

	// 2 tool call starts + 2 argument deltas (fallback from response.done) + 1 done
	if len(chunks) != 5 {
		t.Fatalf("got %d chunks, want 5 (2 tool call starts + 2 arg deltas + 1 done)", len(chunks))
	}

	// First tool call start
	if chunks[0].ToolCallStart == nil {
		t.Fatal("chunk[0] should be ToolCallStart")
	}
	if chunks[0].ToolCallStart.Type != "function_call" {
		t.Errorf("ToolCallStart.Type = %q, want %q", chunks[0].ToolCallStart.Type, "function_call")
	}
	if chunks[0].ToolCallStart.ID != "call_42" {
		t.Errorf("ToolCallStart.ID = %q, want %q", chunks[0].ToolCallStart.ID, "call_42")
	}
	if chunks[0].ToolCallStart.Name != "get_weather" {
		t.Errorf("ToolCallStart.Name = %q, want %q", chunks[0].ToolCallStart.Name, "get_weather")
	}

	// Second tool call start
	if chunks[1].ToolCallStart == nil {
		t.Fatal("chunk[1] should be ToolCallStart")
	}
	if chunks[1].ToolCallStart.ID != "call_43" {
		t.Errorf("ToolCallStart.ID = %q, want %q", chunks[1].ToolCallStart.ID, "call_43")
	}
	if chunks[1].ToolCallStart.Name != "get_time" {
		t.Errorf("ToolCallStart.Name = %q, want %q", chunks[1].ToolCallStart.Name, "get_time")
	}

	// Argument deltas forwarded from response.done (fallback when no
	// incremental function_call_arguments.delta events were streamed).
	if chunks[2].ToolCallDelta == nil || chunks[2].ToolCallDelta.Arguments != `{"city":"Paris"}` {
		t.Errorf("chunk[2].ToolCallDelta.Arguments = %q, want %q", argStr(chunks[2]), `{"city":"Paris"}`)
	}
	if chunks[3].ToolCallDelta == nil || chunks[3].ToolCallDelta.Arguments != `{"tz":"CET"}` {
		t.Errorf("chunk[3].ToolCallDelta.Arguments = %q, want %q", argStr(chunks[3]), `{"tz":"CET"}`)
	}

	// Done chunk with both function calls
	if chunks[4].Done == nil {
		t.Fatal("chunk[4] should be Done")
	}
	if len(chunks[4].Done.Output) != 2 {
		t.Fatalf("Done.Output length = %d, want 2", len(chunks[4].Done.Output))
	}
	if chunks[4].Done.Output[0].Name != "get_weather" {
		t.Errorf("Done.Output[0].Name = %q, want %q", chunks[4].Done.Output[0].Name, "get_weather")
	}
	if chunks[4].Done.Output[1].Name != "get_time" {
		t.Errorf("Done.Output[1].Name = %q, want %q", chunks[4].Done.Output[1].Name, "get_time")
	}
}

func argStr(c CodexStreamChunk) string {
	if c.ToolCallDelta == nil {
		return ""
	}
	return c.ToolCallDelta.Arguments
}

func TestParseCodexStream_ToolCallArgDeltas(t *testing.T) {
	// Real OpenAI Responses API streaming: incremental argument deltas arrive
	// via response.function_call_arguments.delta and the final response.done
	// must NOT re-emit them (no duplication).
	input := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_d","status":"in_progress"}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","name":"get_weather","call_id":"fc_1"}}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"city\""}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":":\"Paris\"}"}`,
		"",
		"event: response.done",
		`data: {"type":"response.done","response":{"id":"resp_d","status":"completed","output":[{"type":"function_call","id":"fc_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}]}}`,
		"",
	}, "\n")

	ctx := context.Background()
	ch := ParseCodexStream(ctx, strings.NewReader(input))

	var chunks []CodexStreamChunk
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}

	// Expect: 1 tool call start + 2 incremental arg deltas + 1 done = 4 chunks.
	if len(chunks) != 4 {
		t.Fatalf("got %d chunks, want 4 (start + 2 deltas + done)", len(chunks))
	}

	if chunks[0].ToolCallStart == nil || chunks[0].ToolCallStart.Name != "get_weather" {
		t.Fatalf("chunk[0] should be tool call start for get_weather")
	}
	if chunks[0].ToolCallIndex != 0 {
		t.Errorf("ToolCallIndex = %d, want 0", chunks[0].ToolCallIndex)
	}

	// Incremental deltas must be forwarded and concatenated into the full JSON.
	if chunks[1].ToolCallDelta == nil || chunks[1].ToolCallDelta.Arguments != `{"city"` {
		t.Errorf("chunk[1] = %q, want %q", argStr(chunks[1]), `{"city"`)
	}
	if chunks[2].ToolCallDelta == nil || chunks[2].ToolCallDelta.Arguments != `:"Paris"}` {
		t.Errorf("chunk[2] = %q, want %q", argStr(chunks[2]), `:"Paris"}`)
	}

	// response.done must not re-emit arguments (no 5th chunk).
	if chunks[3].Done == nil {
		t.Fatal("chunk[3] should be Done")
	}
}

func TestParseCodexStream_EmptyStream(t *testing.T) {
	ctx := context.Background()
	ch := ParseCodexStream(ctx, strings.NewReader(""))

	var chunks []CodexStreamChunk
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}

	if len(chunks) != 0 {
		t.Errorf("got %d chunks from empty stream, want 0", len(chunks))
	}
}

func TestParseCodexStream_ContextCancellation(t *testing.T) {
	sr := newStallReader()
	ctx, cancel := context.WithCancel(context.Background())

	ch := ParseCodexStream(ctx, sr)

	// Cancel the context
	cancel()

	select {
	case chunk, ok := <-ch:
		if !ok {
			t.Fatal("channel closed without delivering error")
		}
		if chunk.Err == nil {
			t.Errorf("expected context error, got chunk with TextDelta=%q Done=%v", chunk.TextDelta, chunk.Done != nil)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ParseCodexStream did not return on context cancellation")
	}
}
