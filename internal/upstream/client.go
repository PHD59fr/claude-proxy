package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/codex"
	"github.com/claude-code-opencode/claude-proxy/internal/config"
)

// Client is the upstream HTTP client.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
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
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Transport: transport,
			// No Timeout — streaming reads can last much longer than the header timeout.
			// ResponseHeaderTimeout on the transport already covers connection establishment.
		},
	}
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
	key := c.apiKey
	if len(authOverride) > 0 && authOverride[0] != "" {
		key = authOverride[0]
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return c.httpClient.Do(req)
}

// Stream sends a streaming request to the upstream.
// Alias for Do — both return the raw http.Response for the caller to consume.
func (c *Client) Stream(ctx context.Context, method, path string, body io.Reader, authOverride ...string) (*http.Response, error) {
	return c.Do(ctx, method, path, body, authOverride...)
}

// Check performs a health/connectivity check.
func (c *Client) Check(ctx context.Context) error {
	u := c.baseURL + "/models"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}
	return nil
}

// CheckChatCompletion verifies the chat completions endpoint is reachable.
func (c *Client) CheckChatCompletion(ctx context.Context, model string) error {
	if model == "" {
		model = "big-pickle"
	}
	u := c.baseURL + "/chat/completions"

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

	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("chat completions returned status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) BaseURL() string {
	return c.baseURL
}

// CodexClient is an HTTP client for the ChatGPT Codex backend.
type CodexClient struct {
	baseURL    string
	httpClient *http.Client

	credentialsMu sync.RWMutex
	oauthToken    string
	accountID     string
}

// NewCodexClient creates a client for the Codex backend.
func NewCodexClient(baseURL, oauthToken, accountID string, timeout time.Duration) *CodexClient {
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
	return &CodexClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		oauthToken: oauthToken,
		accountID:  accountID,
		httpClient: &http.Client{
			Transport: transport,
		},
	}
}

// Do sends a request to the Codex backend with proper auth headers.
func (c *CodexClient) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	u := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.credentialsMu.RLock()
	oauthToken, accountID := c.oauthToken, c.accountID
	c.credentialsMu.RUnlock()

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+oauthToken)
	req.Header.Set("chatgpt-account-id", accountID)
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("x-openai-client-name", "codex_cli")
	req.Header.Set("x-openai-client-version", "0.144.1")
	req.Header.Set("accept", "text/event-stream")
	return c.httpClient.Do(req)
}

// UpdateToken updates the OAuth token used by the Codex client.
func (c *CodexClient) UpdateToken(token string) {
	c.credentialsMu.Lock()
	c.oauthToken = token
	c.credentialsMu.Unlock()
}

// Check verifies the Codex backend is reachable and tokens are valid.
func (c *CodexClient) Check(ctx context.Context) error {
	// Try a minimal request to verify connectivity and auth
	body := strings.NewReader(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":"hi"}],"store":false,"stream":true}`)
	resp, err := c.Do(ctx, "POST", codex.CodexPath, body)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("codex returned status %d", resp.StatusCode)
	}
	return nil
}

// CheckModel verifies if a specific model is accessible.
func (c *CodexClient) CheckModel(ctx context.Context, model string) error {
	reqBody := struct {
		Model string `json:"model"`
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"input"`
		Store  bool `json:"store"`
		Stream bool `json:"stream"`
	}{
		Model: model,
		Input: []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content string `json:"content"`
		}{{Type: "message", Role: "user", Content: "hi"}},
		Store:  false,
		Stream: true,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	resp, err := c.Do(ctx, "POST", codex.CodexPath, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// Router dispatches requests to the correct upstream client based on the
// upstream name associated with a model. The built-in "opencode" upstream and
// the "codex" backend are always available; additional upstreams come from
// configuration.
type Router struct {
	clients map[string]*Client

	codexMu sync.RWMutex
	codex   *CodexClient
}

// NewRouter builds a Router from a list of upstreams plus the optional Codex
// backend. The list should include the implicit "opencode" upstream (built by
// the caller from Config.UpstreamBaseURL/UpstreamAPIKey) so that models
// referencing it resolve to a real client.
func NewRouter(upstreams []config.UpstreamConfig, codexClient *CodexClient, timeout time.Duration) *Router {
	r := &Router{
		clients: make(map[string]*Client, len(upstreams)),
		codex:   codexClient,
	}
	for _, u := range upstreams {
		if u.Name == "" {
			continue
		}
		if _, exists := r.clients[u.Name]; exists {
			continue
		}
		r.clients[u.Name] = NewClient(u.BaseURL, u.APIKey, timeout)
	}
	return r
}

// ClientForModel returns the OpenAI-compatible client that serves the given
// model on the named upstream. It returns an error (and not the Codex client)
// when the upstream is "codex", since Codex is handled separately.
func (r *Router) ClientForModel(model, upstreamName string) (*Client, error) {
	if upstreamName == config.CodexUpstreamName {
		return nil, fmt.Errorf("model %q is served by the Codex backend, not an OpenAI-compatible upstream", model)
	}
	c, ok := r.clients[upstreamName]
	if !ok {
		return nil, fmt.Errorf("no upstream named %q configured for model %q", upstreamName, model)
	}
	return c, nil
}

// Codex returns the Codex backend client (nil if not configured).
func (r *Router) Codex() *CodexClient {
	r.codexMu.RLock()
	defer r.codexMu.RUnlock()
	return r.codex
}

// SetCodex replaces the Codex backend client (used on config reload).
func (r *Router) SetCodex(c *CodexClient) {
	r.codexMu.Lock()
	r.codex = c
	r.codexMu.Unlock()
}

// DefaultClient returns the built-in "opencode" upstream client (nil if not configured).
func (r *Router) DefaultClient() *Client {
	return r.clients[config.DefaultUpstreamName]
}
