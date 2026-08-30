// Package providers defines the Provider abstraction behind OpenCode Zen, the
// Codex (ChatGPT) backend and any additional OpenAI-compatible upstream.
//
// A Provider knows its own wire protocol: how to build a request payload, how
// to check a model, and how to decode a response into the Anthropic format the
// proxy exposes. The Registry resolves provider names (from the model
// preference list) to Provider instances, so the request path in the handler
// is provider-agnostic and a new backend only needs to implement the
// interface. Sentinel errors returned by Provider methods live in the errs
// subpackage so provider implementations never import this one.
package providers

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/anthropic"
	"github.com/claude-code-opencode/claude-proxy/internal/openai"
)

// Provider is a named model backend reachable through the proxy.
type Provider interface {
	// Name identifies the provider in the model preference list ("zen",
	// "codex", or a configured custom upstream name).
	Name() string

	// Check verifies that the named model is usable on this provider right
	// now, without consuming meaningful quota.
	Check(ctx context.Context, model string, authOverride ...string) error

	// Invoke encodes the Anthropic message request into the provider's wire
	// format and performs the upstream call. stream selects the streaming
	// format. The returned *http.Response must be closed by the caller.
	Invoke(ctx context.Context, req *anthropic.MessageRequest, model, defaultModel string, stream bool, authOverride ...string) (*http.Response, error)

	// ParseResponse converts a non-stream upstream response body into a
	// marshaled Anthropic response. model is the provider model that was
	// called; originalModel is the name the client requested.
	ParseResponse(model string, respBody []byte, originalModel string) ([]byte, error)

	// StreamChunks turns the upstream streaming body into OpenAI-style stream
	// chunks for the shared stream converter. Errors during the stream appear
	// as chunks with a non-nil Err field.
	StreamChunks(ctx context.Context, model string, respBody io.Reader, originalModel string, idleTimeout time.Duration) <-chan openai.StreamChunk
}
