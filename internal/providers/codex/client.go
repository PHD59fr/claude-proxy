package codex

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

	"github.com/claude-code-opencode/claude-proxy/internal/config"
	"github.com/claude-code-opencode/claude-proxy/internal/providers/errs"
)

// Client is an HTTP client for the OpenAI Codex / ChatGPT backend.
type Client struct {
	baseURL    string
	httpClient *http.Client

	credentialsMu sync.RWMutex
	oauthToken    string
	accountID     string
}

// Name returns the canonical provider name ("codex").
func (c *Client) Name() string {
	return config.CodexUpstreamName
}

// NewClient creates a client for the codex backend. pass the base URL (usually
// CodexBackendURL) or the URL of a test double.
func NewClient(baseURL, oauthToken, accountID string, timeout time.Duration) *Client {
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
		baseURL:    strings.TrimRight(baseURL, "/"),
		oauthToken: oauthToken,
		accountID:  accountID,
		httpClient: &http.Client{
			Transport: transport,
		},
	}
}

// Do sends a request to the codex backend with proper auth headers.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
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

// UpdateToken updates the OAuth token used by the client.
func (c *Client) UpdateToken(token string) {
	c.credentialsMu.Lock()
	c.oauthToken = token
	c.credentialsMu.Unlock()
}

// CheckBackend verifies the codex backend is reachable and tokens are valid.
func (c *Client) CheckBackend(ctx context.Context) error {
	body := strings.NewReader(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":"hi"}],"store":false,"stream":true}`)
	resp, err := c.Do(ctx, "POST", CodexPath, body)
	if err != nil {
		return err
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		// 429 and 5xx are temporary conditions and must precede the body
		// heuristics so they never blacklist a healthy model as permanently
		// unavailable.
		if resp.StatusCode == 429 {
			if errs.IsGlobalRateLimit(respBody) {
				return fmt.Errorf("codex returned status %d: %w", resp.StatusCode, errs.ErrGlobalRateLimited)
			}
			return fmt.Errorf("codex returned status %d: %w", resp.StatusCode, errs.ErrRateLimited)
		}
		if resp.StatusCode >= 500 {
			return fmt.Errorf("codex returned status %d", resp.StatusCode)
		}
		if errs.IsModelUnavailable(respBody) {
			return fmt.Errorf("codex returned status %d: %w", resp.StatusCode, errs.ErrModelUnavailable)
		}
		return fmt.Errorf("codex returned status %d", resp.StatusCode)
	}
	return nil
}

// Check verifies that the named model is usable on this provider (Provider
// interface).
func (c *Client) Check(ctx context.Context, model string, _ ...string) error {
	return c.CheckModel(ctx, model)
}

// CheckModel verifies if a specific model is accessible.
func (c *Client) CheckModel(ctx context.Context, model string) error {
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
	resp, err := c.Do(ctx, "POST", CodexPath, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		// 429 and 5xx are temporary conditions and must precede the body
		// heuristics so they never blacklist a healthy model as permanently
		// unavailable.
		if resp.StatusCode == 429 {
			if errs.IsGlobalRateLimit(respBody) {
				return fmt.Errorf("status %d: %w", resp.StatusCode, errs.ErrGlobalRateLimited)
			}
			return fmt.Errorf("status %d: %w", resp.StatusCode, errs.ErrRateLimited)
		}
		if resp.StatusCode >= 500 {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		if errs.IsModelUnavailable(respBody) {
			return fmt.Errorf("status %d: %w", resp.StatusCode, errs.ErrModelUnavailable)
		}
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// Models returns the account-scoped Codex model catalog.
func (c *Client) Models(ctx context.Context) ([]ModelInfo, error) {
	c.credentialsMu.RLock()
	oauthToken, accountID := c.oauthToken, c.accountID
	c.credentialsMu.RUnlock()

	u := c.baseURL + "/codex/models?client_version=" + CodexClientVersion
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("create models request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+oauthToken)
	req.Header.Set("chatgpt-account-id", accountID)
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("version", CodexClientVersion)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch codex models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch codex models: status %d", resp.StatusCode)
	}
	var result ModelsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode codex models: %w", err)
	}
	return VisibleModels(result.Models), nil
}
