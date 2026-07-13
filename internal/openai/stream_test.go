package openai

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/ioutil"
)

// stallReader blocks on Read until the context is cancelled or Close is called.
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

func TestIdleTimeoutReader_ClosesOnStall(t *testing.T) {
	sr := newStallReader()
	tr := ioutil.NewIdleTimeoutReader(sr, 50*time.Millisecond)

	// Read should block initially
	done := make(chan error, 1)
	go func() {
		_, err := tr.Read(make([]byte, 1))
		done <- err
	}()

	// Wait for the idle timeout to fire
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected error from idle timeout, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("idle timeout did not fire within 2s")
	}
}

func TestIdleTimeoutReader_NoTimeoutOnActiveStream(t *testing.T) {
	data := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"
	sr := strings.NewReader(data)
	tr := ioutil.NewIdleTimeoutReader(sr, 1*time.Second)

	buf := make([]byte, 1024)
	n, err := tr.Read(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n == 0 {
		t.Error("expected to read some bytes")
	}
}

func TestParseStream_StallReturnsError(t *testing.T) {
	sr := newStallReader()
	ctx := context.Background()

	ch := parseStreamInternal(ctx, sr, 50*time.Millisecond)

	// Read with timeout
	select {
	case chunk, ok := <-ch:
		if !ok {
			t.Fatal("channel closed without delivering error")
		}
		if chunk.Err == nil {
			t.Fatalf("expected error, got chunk: %+v", chunk.Chunk)
		}
		t.Logf("got expected error: %v", chunk.Err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for error")
	}
}

func TestParseStream_NormalStream(t *testing.T) {
	input := strings.Join([]string{
		`data: {"id":"test","model":"m","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`data: {"id":"test","model":"m","choices":[{"index":0,"delta":{"content":"hello"}}]}`,
		`data: {"id":"test","model":"m","choices":[{"index":0,"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}, "\n")

	ctx := context.Background()
	ch := ParseStream(ctx, strings.NewReader(input))

	var chunks []StreamChunk
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}

	// Should have 3 data chunks + 1 done
	if len(chunks) != 4 {
		t.Errorf("got %d chunks, want 4", len(chunks))
	}
	if !chunks[len(chunks)-1].Done {
		t.Error("last chunk should be Done")
	}
	if chunks[0].Chunk.Model != "m" {
		t.Errorf("model = %q, want m", chunks[0].Chunk.Model)
	}
}

func TestParseStream_ContextCancellation(t *testing.T) {
	sr := newStallReader()
	ctx, cancel := context.WithCancel(context.Background())

	ch := ParseStream(ctx, sr)

	// Cancel the context
	cancel()

	select {
	case chunk := <-ch:
		if chunk.Err == nil {
			t.Error("expected context error, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ParseStream did not return on context cancellation")
	}
}
