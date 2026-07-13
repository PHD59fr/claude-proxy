package openai

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

// StreamChunk represents a parsed SSE chunk from the upstream.
type StreamChunk struct {
	Chunk ChatCompletionChunk
	Done  bool
	Err   error
}

// StreamIdleTimeout is the default idle timeout applied to upstream streams
// when the request timeout configuration is unset or invalid.
const StreamIdleTimeout = 30 * time.Second

// ParseStream reads OpenAI SSE stream and yields chunks.
// It respects context cancellation for client disconnects.
// An idle timeout of 30s is applied to detect stalled upstream connections.
func ParseStream(ctx context.Context, reader io.Reader) <-chan StreamChunk {
	return parseStreamInternal(ctx, reader, StreamIdleTimeout)
}

// ParseStreamWithTimeout is like ParseStream but uses the supplied idle
// timeout. Callers should pass the configured request timeout so a stalled
// upstream is detected in line with the operator's expectations.
func ParseStreamWithTimeout(ctx context.Context, reader io.Reader, idleTimeout time.Duration) <-chan StreamChunk {
	if idleTimeout <= 0 {
		idleTimeout = StreamIdleTimeout
	}
	return parseStreamInternal(ctx, reader, idleTimeout)
}

// parseStreamInternal is the core ParseStream implementation with a configurable
// idle timeout. The public ParseStream uses a 30s default.
func parseStreamInternal(ctx context.Context, reader io.Reader, idleTimeout time.Duration) <-chan StreamChunk {
	ch := make(chan StreamChunk, 16)
	lines := make(chan string, 16)
	done := make(chan struct{})

	// Wrap reader with idle timeout to detect stalled connections
	r := ioutil.NewIdleTimeoutReader(reader, idleTimeout)

	// Goroutine 1: read lines from the scanner (blocking I/O).
	go func() {
		defer close(lines)
		defer func() { _ = r.Close() }() // stop idle timeout monitor
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
			// Idle timeout fired — body was closed externally.
			// scanner.Err() is nil (EOF) so we must send the error ourselves.
			select {
			case lines <- fmt.Sprintf("__ERROR__upstream stalled: no data for %v", idleTimeout):
			case <-done:
			}
		}
	}()

	// Goroutine 2: parse SSE lines, respects context cancellation.
	go func() {
		defer close(ch)
		defer close(done)                // signal scanner goroutine to stop
		defer func() { _ = r.Close() }() // unblock scanner's Read if context cancelled
		for {
			select {
			case <-ctx.Done():
				ch <- StreamChunk{Err: ctx.Err()}
				return
			case line, ok := <-lines:
				if !ok {
					// Upstream closed without [DONE] — treat as an error
					// rather than silently presenting truncated output as success.
					ch <- StreamChunk{Err: fmt.Errorf("upstream closed connection without [DONE] marker")}
					return
				}
				// Propagate scanner errors.
				if strings.HasPrefix(line, "__ERROR__") {
					ch <- StreamChunk{Err: fmt.Errorf("scanner error: %s", strings.TrimPrefix(line, "__ERROR__"))}
					return
				}
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				data := strings.TrimPrefix(line, "data: ")
				data = strings.TrimSpace(data)
				if data == "[DONE]" {
					ch <- StreamChunk{Done: true}
					return
				}
				var chunk ChatCompletionChunk
				if err := json.Unmarshal([]byte(data), &chunk); err != nil {
					ch <- StreamChunk{Err: fmt.Errorf("unmarshal chunk: %w (data: %s)", err, truncateJSON(data, 200))}
					return
				}
				ch <- StreamChunk{Chunk: chunk}
			}
		}
	}()

	return ch
}

// truncateJSON safely truncates a JSON string for logging.
func truncateJSON(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
