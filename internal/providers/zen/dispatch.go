package zen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/anthropic"
	"github.com/claude-code-opencode/claude-proxy/internal/convert"
	"github.com/claude-code-opencode/claude-proxy/internal/openai"
	"github.com/claude-code-opencode/claude-proxy/internal/providers/codex"
	"github.com/claude-code-opencode/claude-proxy/internal/providers/errs"
)

// Invoke encodes an Anthropic message request into this provider's wire format
// (chat completions or Responses, depending on the model) and performs the
// upstream call. The returned *http.Response must have its body closed by the
// caller.
func (c *Client) Invoke(ctx context.Context, msgReq *anthropic.MessageRequest, model, defaultModel string, stream bool, authOverride ...string) (*http.Response, error) {
	req := *msgReq
	req.Model = model

	var upstreamReq interface{}
	path := ChatCompletionsPath
	if c.UsesResponsesAPI(model) {
		out, err := codex.TransformResponsesRequest(&req)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errs.ErrInvalidRequest, err)
		}
		upstreamReq = out
		path = ResponsesPath
	} else {
		out, err := convert.Request(&req, defaultModel)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errs.ErrInvalidRequest, err)
		}
		upstreamReq = out
	}

	reqBody, err := json.Marshal(upstreamReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errs.ErrMarshal, err)
	}

	return c.Do(ctx, "POST", path, bytes.NewReader(reqBody), authOverride...)
}

// ParseResponse converts a non-stream upstream response into a marshaled
// Anthropic response. A failed decode is a hard error (the fallback loop does
// not retry); a response whose status reports a failure returns errs.ErrFailed.
func (c *Client) ParseResponse(model string, respBody []byte, originalModel string) ([]byte, error) {
	if c.UsesResponsesAPI(model) {
		var responsesResp codex.ResponsesDoneBody
		if err := json.Unmarshal(respBody, &responsesResp); err != nil {
			return nil, fmt.Errorf("decode responses response: %w", err)
		}
		if codex.IsFailedStatus(responsesResp.Status) {
			return nil, errs.ErrFailed
		}
		return json.Marshal(codex.TransformResponse(&responsesResp, originalModel))
	}

	var oaiResp openai.ChatCompletionResponse
	if err := json.Unmarshal(respBody, &oaiResp); err != nil {
		return nil, fmt.Errorf("decode chat completions response: %w", err)
	}
	return json.Marshal(convert.Response(&oaiResp, originalModel))
}

// StreamChunks converts the upstream streaming body into OpenAI-style stream
// chunks handled by the shared stream converter.
func (c *Client) StreamChunks(ctx context.Context, model string, respBody io.Reader, originalModel string, idleTimeout time.Duration) <-chan openai.StreamChunk {
	if c.UsesResponsesAPI(model) {
		return codex.ToOpenAIStream(codex.ParseResponsesStreamWithTimeout(ctx, respBody, idleTimeout), originalModel)
	}
	return openai.ParseStreamWithTimeout(ctx, respBody, idleTimeout)
}
