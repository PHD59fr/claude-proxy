package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/anthropic"
	"github.com/claude-code-opencode/claude-proxy/internal/openai"
	"github.com/claude-code-opencode/claude-proxy/internal/providers/errs"
)

// Invoke encodes an Anthropic message request into the codex Responses wire
// format and performs the upstream call. The return *http.Response must be
// closed by the caller.
func (c *Client) Invoke(ctx context.Context, msgReq *anthropic.MessageRequest, model, _ string, _ bool, _ ...string) (*http.Response, error) {
	req := *msgReq
	req.Model = model
	codexReq, err := TransformRequest(&req)
	if err != nil {
		return nil, errs.ErrInvalidRequest
	}
	reqBody, err := json.Marshal(codexReq)
	if err != nil {
		return nil, errs.ErrMarshal
	}
	return c.Do(ctx, "POST", CodexPath, bytes.NewReader(reqBody))
}

// ParseResponse reassembles a non-stream codex response. The codex backend
// streams its output as SSE even for non-stream requests and closes with an
// (almost) empty body, so the output must be rebuilt from the item events.
// An empty stream returns errs.ErrNoContent (try the next model).
func (c *Client) ParseResponse(model string, respBody []byte, originalModel string) ([]byte, error) {
	events := ParseSSEEvents(respBody)
	doneBody := FindDoneBody(events)
	if doneBody == nil {
		return nil, errs.ErrNoContent
	}
	if IsFailedStatus(doneBody.Status) {
		return nil, errs.ErrFailed
	}
	var outputItems []OutputItem
	for _, evt := range events {
		if evt.Type == "response.output_item.done" && evt.Item != nil {
			outputItems = append(outputItems, *evt.Item)
		}
	}
	if len(outputItems) > 0 {
		doneBody.Output = outputItems
	}
	anthResp := TransformResponse(doneBody, originalModel)
	return json.Marshal(anthResp)
}

// StreamChunks turns the codex streaming body into OpenAI-style stream chunks.
func (c *Client) StreamChunks(ctx context.Context, model string, respBody io.Reader, originalModel string, idleTimeout time.Duration) <-chan openai.StreamChunk {
	// ParseResponsesStreamWithTimeout (not the bare ParseCodexStreamWithTimeout)
	// rejects an EOF that arrives before a terminal response event, so a
	// truncated codex stream surfaces as an error instead of an empty silent
	// stream — matching the safety net zen applies on its Responses path.
	return ToOpenAIStream(ParseResponsesStreamWithTimeout(ctx, respBody, idleTimeout), originalModel)
}

// ToOpenAIStream converts parsed codex/Responses stream chunks into the
// OpenAI-style chunks consumed by the shared stream converter.
func ToOpenAIStream(chunks <-chan CodexStreamChunk, model string) <-chan openai.StreamChunk {
	out := make(chan openai.StreamChunk, 16)
	go func() {
		defer close(out)
		hasToolUse := false
		for chunk := range chunks {
			if chunk.Err != nil {
				out <- openai.StreamChunk{Err: chunk.Err}
				return
			}
			if chunk.TextDelta != "" {
				out <- openai.StreamChunk{Chunk: openai.ChatCompletionChunk{Model: model, Choices: []openai.ChunkChoice{{Index: 0, Delta: openai.ChatDelta{Content: chunk.TextDelta}}}}}
			}
			if chunk.ToolCallStart != nil {
				hasToolUse = true
				callID := chunk.ToolCallStart.CallID
				if callID == "" {
					callID = chunk.ToolCallStart.ID
				}
				out <- openai.StreamChunk{Chunk: openai.ChatCompletionChunk{Model: model, Choices: []openai.ChunkChoice{{Index: 0, Delta: openai.ChatDelta{ToolCalls: []openai.ToolCallDelta{{Index: chunk.ToolCallIndex, ID: callID, Type: "function", Function: openai.FuncDelta{Name: chunk.ToolCallStart.Name}}}}}}}}
			}
			if chunk.ToolCallDelta != nil {
				out <- openai.StreamChunk{Chunk: openai.ChatCompletionChunk{Model: model, Choices: []openai.ChunkChoice{{Index: 0, Delta: openai.ChatDelta{ToolCalls: []openai.ToolCallDelta{{Index: chunk.ToolCallDelta.Index, Function: openai.FuncDelta{Arguments: chunk.ToolCallDelta.Arguments}}}}}}}}
			}
			if chunk.Done != nil {
				if chunk.Done.Usage != nil {
					out <- openai.StreamChunk{Chunk: openai.ChatCompletionChunk{Model: model, Usage: &openai.Usage{PromptTokens: chunk.Done.Usage.InputTokens, CompletionTokens: chunk.Done.Usage.OutputTokens, TotalTokens: chunk.Done.Usage.TotalTokens}}}
				}
				finishReason := "stop"
				// Truncation must take precedence over tool use: a stream that
				// opened a tool call and then hit the output cap should surface
				// "length" so the client knows the output was cut short.
				if chunk.Done.Status == "incomplete" && chunk.Done.IncompleteDetails != nil && chunk.Done.IncompleteDetails.Reason == "max_output_tokens" {
					finishReason = "length"
				} else if hasToolUse {
					finishReason = "tool_calls"
				}
				out <- openai.StreamChunk{Chunk: openai.ChatCompletionChunk{Model: model, Choices: []openai.ChunkChoice{{Index: 0, FinishReason: &finishReason}}}}
				out <- openai.StreamChunk{Done: true}
			}
		}
	}()
	return out
}
