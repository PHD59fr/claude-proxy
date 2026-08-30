// Package zen implements the Zen provider: OpenCode's free OpenAI-compatible
// API and, for a subset of models (gpt-/grok-/muse- prefixes), the Responses
// API. Any OpenAI-compatible upstream can be served by Client; the "zen"
// provider instance is the built-in default upstream, while custom upstreams
// configured in config.json are additional instances with another Name.
package zen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/claude-code-opencode/claude-proxy/internal/providers/errs"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	// ChatCompletionsPath is the OpenAI-compatible chat completions endpoint.
	ChatCompletionsPath = "/chat/completions"

	// ResponsesPath is the OpenAI Responses API endpoint.
	ResponsesPath = "/responses"
)

// Client is the HTTP client for a Zen-style (OpenAI-compatible) upstream.
// Name identifies the provider in the model preference list: "zen" enables the
// Responses API for the gpt-/grok-/muse- model families, other names behave as
// plain OpenAI-compatible chat-completions endpoints.
type Client struct {
	name    string
	baseURL string
	apiKey  string
	http    *http.Client
}

// defaultUserAgent identifies this proxy to the upstream. OpenCode Zen's
// free-tier rate limiter treats requests whose User-Agent does not look like
// an application client (e.g. Go's default "Go-http-client/1.1" or "curl/8")
// as bots and answers 429 FreeUsageLimitError. The UA string must contain
// "opencode" to be recognized.
const defaultUserAgent = "opencode-proxy/1.0"

// userAgentTransport stamps a default User-Agent on every outbound request.
type userAgentTransport struct {
	base http.RoundTripper
	ua   string
}

func (t *userAgentTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("User-Agent") == "" {
		r.Header.Set("User-Agent", t.ua)
	}
	return t.base.RoundTrip(r)
}

// NewClient builds a client for an OpenAI-compatible endpoint served under the
// given provider name. Passing name == "zen" enables Responses-API routing;
// other names behave as plain chat-completions providers.
func NewClient(name, baseURL, apiKey string, timeout time.Duration) *Client {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
	}
	return &Client{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http: &http.Client{
			Transport: &userAgentTransport{base: transport, ua: defaultUserAgent},
			// No Timeout — streaming reads can last much longer than the header
			// timeout. ResponseHeaderTimeout on the transport already covers
			// connection establishment.
		},
	}
}

// Name returns the provider name ("zen" for the built-in upstream, or the name
// of a configured custom upstream).
func (c *Client) Name() string {
	return c.name
}

// BaseURL returns the upstream base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

func (c *Client) authorizationKey(authOverride ...string) string {
	if len(authOverride) > 0 && authOverride[0] != "" {
		return authOverride[0]
	}
	return c.apiKey
}

// Do sends a request to the upstream and returns the response.
// If authOverride is non-empty, it is used instead of the configured API key.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader, authOverride ...string) (*http.Response, error) {
	u := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key := c.authorizationKey(authOverride...); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return c.http.Do(req)
}

// Stream sends a streaming request to the upstream.
// Alias for Do — both return the raw http.Response for the caller to consume.
func (c *Client) Stream(ctx context.Context, method, path string, body io.Reader, authOverride ...string) (*http.Response, error) {
	return c.Do(ctx, method, path, body, authOverride...)
}

// Check verifies that the named model is usable on this provider, probing the
// chat completions or Responses endpoint depending on the model family.
func (c *Client) Check(ctx context.Context, model string, authOverride ...string) error {
	if c.UsesResponsesAPI(model) {
		return c.checkResponses(ctx, model, authOverride...)
	}
	return c.checkChatCompletion(ctx, model, authOverride...)
}

// CheckChatCompletion verifies a model through the chat completions endpoint.
func (c *Client) CheckChatCompletion(ctx context.Context, model string, authOverride ...string) error {
	return c.checkChatCompletion(ctx, model, authOverride...)
}

func (c *Client) checkChatCompletion(ctx context.Context, model string, authOverride ...string) error {
	if model == "" {
		model = "big-pickle"
	}
	reqBody := struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}{
		Model:     model,
		MaxTokens: 1,
		Messages: []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{{Role: "user", Content: "hi"}},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	resp, err := c.Do(ctx, http.MethodPost, ChatCompletionsPath, bytes.NewReader(bodyBytes), authOverride...)
	if err != nil {
		return err
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		// Rate limits and transient 5xx are never "model unavailable": they are
		// temporary conditions and must not permanently blacklist a healthy model
		// (the caller marks ErrModelUnavailable as a lasting disable). Check them
		// before the body heuristics so a 429/5xx is never misclassified.
		if resp.StatusCode == 429 {
			if errs.IsGlobalRateLimit(respBody) {
				return fmt.Errorf("chat completions returned status %d: %w", resp.StatusCode, errs.ErrGlobalRateLimited)
			}
			return fmt.Errorf("chat completions returned status %d: %w", resp.StatusCode, errs.ErrRateLimited)
		}
		if resp.StatusCode >= 500 {
			return fmt.Errorf("chat completions returned status %d", resp.StatusCode)
		}
		// Only a genuine 4xx client error (400/404/...) can mean the model is gone.
		if errs.IsModelUnavailable(respBody) {
			return fmt.Errorf("chat completions returned status %d: %w", resp.StatusCode, errs.ErrModelUnavailable)
		}
		return fmt.Errorf("chat completions returned status %d", resp.StatusCode)
	}
	return nil
}

// CheckResponses verifies a model through the Responses API.
func (c *Client) CheckResponses(ctx context.Context, model string, authOverride ...string) error {
	return c.checkResponses(ctx, model, authOverride...)
}

func (c *Client) checkResponses(ctx context.Context, model string, authOverride ...string) error {
	reqBody := struct {
		Model           string `json:"model"`
		Input           string `json:"input"`
		MaxOutputTokens int    `json:"max_output_tokens"`
		Store           bool   `json:"store"`
	}{Model: model, Input: "hi", MaxOutputTokens: 1, Store: false}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	resp, err := c.Do(ctx, http.MethodPost, ResponsesPath, bytes.NewReader(bodyBytes), authOverride...)
	if err != nil {
		return err
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		// Same precedence as checkChatCompletion: 429 and 5xx are temporary and
		// must precede the body heuristics so they never blacklist a healthy
		// model as permanently unavailable.
		if resp.StatusCode == 429 {
			if errs.IsGlobalRateLimit(respBody) {
				return fmt.Errorf("responses returned status %d: %w", resp.StatusCode, errs.ErrGlobalRateLimited)
			}
			return fmt.Errorf("responses returned status %d: %w", resp.StatusCode, errs.ErrRateLimited)
		}
		if resp.StatusCode >= 500 {
			return fmt.Errorf("responses returned status %d", resp.StatusCode)
		}
		if errs.IsModelUnavailable(respBody) {
			return fmt.Errorf("responses returned status %d: %w", resp.StatusCode, errs.ErrModelUnavailable)
		}
		return fmt.Errorf("responses returned status %d", resp.StatusCode)
	}
	return nil
}

// UsesResponsesAPI reports whether the model is served through the Responses
// API. Only the built-in "zen" provider routes these model families to the
// Responses endpoint; custom upstreams with the same IDs keep their plain
// OpenAI-compatible behavior.
func (c *Client) UsesResponsesAPI(model string) bool {
	if c.name != "zen" {
		return false
	}
	return strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "grok-") || strings.HasPrefix(model, "muse-")
}
