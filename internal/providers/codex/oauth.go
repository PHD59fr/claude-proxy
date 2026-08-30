package codex

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	OAuthClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	OAuthAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	OAuthTokenURL     = "https://auth.openai.com/oauth/token"
	OAuthRedirectURI  = "http://localhost:1455/auth/callback"
	OAuthScope        = "openid profile email offline_access"
	OAuthCallbackPort = 1455
)

var (
	configPathCache string
	tokensCache     *OAuthTokens
	tokensMu        sync.RWMutex
)

// OAuthTokens holds the OAuth token set.
type OAuthTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // Unix millis
	AccountID    string `json:"account_id"`
}

// PKCEPair holds a PKCE verifier and challenge.
type PKCEPair struct {
	Verifier  string
	Challenge string
}

// GeneratePKCE creates a random PKCE verifier and its SHA-256 challenge.
func GeneratePKCE() (PKCEPair, error) {
	// Generate 32 random bytes for the verifier
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return PKCEPair{}, fmt.Errorf("generate verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(buf)

	// SHA-256 hash of the verifier -> challenge
	h := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(h[:])

	return PKCEPair{Verifier: verifier, Challenge: challenge}, nil
}

// GenerateState creates a random state parameter (16 bytes -> 32 hex chars).
func GenerateState() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}
	return fmt.Sprintf("%x", buf), nil
}

// BuildAuthorizeURL constructs the full OAuth authorization URL.
func BuildAuthorizeURL(pkce PKCEPair, state string) string {
	v := url.Values{}
	v.Set("response_type", "code")
	v.Set("client_id", OAuthClientID)
	v.Set("redirect_uri", OAuthRedirectURI)
	v.Set("scope", OAuthScope)
	v.Set("code_challenge", pkce.Challenge)
	v.Set("code_challenge_method", "S256")
	v.Set("state", state)
	v.Set("id_token_add_organizations", "true")
	v.Set("codex_cli_simplified_flow", "true")
	v.Set("originator", "codex_cli_rs")
	return OAuthAuthorizeURL + "?" + v.Encode()
}

// ExchangeCode exchanges an authorization code for tokens.
func ExchangeCode(code, codeVerifier string) (*OAuthTokens, error) {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("client_id", OAuthClientID)
	data.Set("code", code)
	data.Set("code_verifier", codeVerifier)
	data.Set("redirect_uri", OAuthRedirectURI)

	return doTokenRequest(data)
}

// RefreshTokens refreshes an access token using a refresh token.
func RefreshTokens(refreshToken string) (*OAuthTokens, error) {
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", OAuthClientID)

	tokens, err := doTokenRequest(data)
	if err != nil {
		return nil, err
	}
	// Preserve the original refresh token if the response doesn't include a new one.
	// RFC 6749 Section 1.5: the authorization server MAY issue a new refresh token.
	// OpenAI typically omits refresh_token on refresh grants when rotation is disabled.
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = refreshToken
	}
	return tokens, nil
}

// oauthHTTPClient is shared by all OAuth token requests to enforce a timeout.
var oauthHTTPClient = &http.Client{Timeout: 30 * time.Second}

// doTokenRequest sends a token request to the OAuth endpoint.
func doTokenRequest(data url.Values) (*OAuthTokens, error) {
	resp, err := oauthHTTPClient.PostForm(OAuthTokenURL, data)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token request failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}

	accountID := ExtractAccountID(tokenResp.AccessToken)

	return &OAuthTokens{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).UnixMilli(),
		AccountID:    accountID,
	}, nil
}

// ExtractAccountID decodes a JWT and extracts the ChatGPT account ID.
// Signature verification is intentionally skipped: this is a local-only proxy
// with no secret key available. The token is obtained locally via OAuth and
// only used to read claims, so verification adds no security benefit here.
func ExtractAccountID(accessToken string) string {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		OpenAI struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.OpenAI.ChatGPTAccountID
}

// ExtractExpiry decodes a JWT and returns the expiry time as Unix milliseconds.
// Returns 0 if the JWT cannot be parsed or has no exp claim.
func ExtractExpiry(accessToken string) int64 {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return 0
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return 0
	}
	return claims.Exp * 1000 // convert seconds to millis
}

// ShouldRefreshToken checks if the token is expired or near expiry (5 min buffer).
func ShouldRefreshToken(tokens *OAuthTokens) bool {
	if tokens == nil || tokens.AccessToken == "" {
		return true
	}
	// Refresh if within 5 minutes of expiry
	return time.Now().UnixMilli() >= tokens.ExpiresAt-5*60*1000
}

// --- Token Storage (unified config.json) ---

// ConfigFilePath returns the path to the unified config.json.
// Defaults to config.json in the current working directory.
// Can be overridden with SetConfigFilePath.
func ConfigFilePath() string {
	tokensMu.RLock()
	if configPathCache != "" {
		p := configPathCache
		tokensMu.RUnlock()
		return p
	}
	tokensMu.RUnlock()

	p := "config.json"

	tokensMu.Lock()
	configPathCache = p
	tokensMu.Unlock()
	return p
}

// SetConfigFilePath overrides the default config file path.
// Call this when --config is provided.
func SetConfigFilePath(path string) {
	if path == "" {
		return
	}
	tokensMu.Lock()
	configPathCache = path
	tokensMu.Unlock()
}

// InvalidateTokenCache clears the in-memory token cache so the next
// LoadTokens call reads fresh credentials from disk. Call this when the
// config file is externally modified (e.g. by the config watcher).
func InvalidateTokenCache() {
	tokensMu.Lock()
	tokensCache = nil
	tokensMu.Unlock()
}

// SaveTokens persists the OAuth tokens into the config.json file.
// Also updates the in-memory cache.
func SaveTokens(tokens *OAuthTokens) error {
	configPath := ConfigFilePath()

	// Ensure directory exists (safety net)
	_ = os.MkdirAll(filepath.Dir(configPath), 0700)

	// Read existing config (or start with empty map)
	cfg := make(map[string]interface{})
	if data, err := os.ReadFile(configPath); err == nil {
		// Best-effort parse; if the file is corrupt we still overwrite
		_ = json.Unmarshal(data, &cfg)
	}

	// Update token fields
	cfg["codex_oauth_token"] = tokens.AccessToken
	cfg["codex_refresh_token"] = tokens.RefreshToken
	cfg["codex_account_id"] = tokens.AccountID

	// Write back
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// Update cache
	tokensMu.Lock()
	tokensCache = tokens
	tokensMu.Unlock()

	return os.WriteFile(configPath, out, 0600)
}

// LoadTokens reads OAuth tokens from the config.json file.
// Returns cached tokens if available, otherwise reads from disk.
func LoadTokens() (*OAuthTokens, error) {
	tokensMu.RLock()
	if tokensCache != nil {
		t := tokensCache
		tokensMu.RUnlock()
		return t, nil
	}
	tokensMu.RUnlock()

	configPath := ConfigFilePath()
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var cfg struct {
		CodexOAuthToken   string `json:"codex_oauth_token"`
		CodexRefreshToken string `json:"codex_refresh_token"`
		CodexAccountID    string `json:"codex_account_id"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	if cfg.CodexOAuthToken == "" {
		return nil, fmt.Errorf("no codex tokens in config")
	}

	tokens := &OAuthTokens{
		AccessToken:  cfg.CodexOAuthToken,
		RefreshToken: cfg.CodexRefreshToken,
		AccountID:    cfg.CodexAccountID,
		ExpiresAt:    ExtractExpiry(cfg.CodexOAuthToken),
	}

	// If we couldn't extract expiry from JWT, fall back to 1 hour
	if tokens.ExpiresAt == 0 {
		tokens.ExpiresAt = time.Now().Add(time.Hour).UnixMilli()
	}

	// If account_id is missing, extract it from the JWT
	if tokens.AccountID == "" {
		tokens.AccountID = ExtractAccountID(cfg.CodexOAuthToken)
	}

	tokensMu.Lock()
	tokensCache = tokens
	tokensMu.Unlock()

	return tokens, nil
}

// --- Local OAuth Callback Server ---

// OAuthServer is a local HTTP server that receives the OAuth callback.
type OAuthServer struct {
	server  *http.Server
	code    string
	state   string
	mu      sync.Mutex
	ready   chan struct{}
	closed  bool
	Timeout time.Duration // How long WaitForCode waits before timing out. Default 60s.
}

// StartOAuthServer starts a local server on port 1455 to receive the OAuth callback.
func StartOAuthServer(state string) *OAuthServer {
	srv := &OAuthServer{
		state: state,
		ready: make(chan struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", srv.handleCallback)

	srv.server = &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", OAuthCallbackPort),
		Handler: mux,
	}

	go func() {
		if err := srv.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// Port likely in use -- signal not ready
			srv.mu.Lock()
			if !srv.closed {
				close(srv.ready)
			}
			srv.mu.Unlock()
		}
	}()

	// Signal ready after a short delay (server should be listening)
	go func() {
		time.Sleep(200 * time.Millisecond)
		srv.mu.Lock()
		select {
		case <-srv.ready:
			// Already closed (port in use)
		default:
			close(srv.ready)
		}
		srv.mu.Unlock()
	}()

	return srv
}

func (s *OAuthServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	receivedState := query.Get("state")
	code := query.Get("code")

	s.mu.Lock()
	defer s.mu.Unlock()

	if receivedState != s.state {
		http.Error(w, "State mismatch", http.StatusBadRequest)
		return
	}

	s.code = code

	// Return success page
	w.Header().Set("Content-Type", "text/html")
	_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><title>Codex Auth</title></head><body>
<h2>Authentication successful!</h2>
<p>You can close this window and return to the terminal.</p>
</body></html>`))
}

// WaitForCode waits for the OAuth callback code (up to Timeout, default 60 seconds).
func (s *OAuthServer) WaitForCode() (string, error) {
	<-s.ready

	s.mu.Lock()
	code := s.code
	s.mu.Unlock()

	if code != "" {
		return code, nil
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		s.mu.Lock()
		code = s.code
		s.mu.Unlock()
		if code != "" {
			return code, nil
		}
	}
	return "", fmt.Errorf("timeout waiting for OAuth callback")
}

// Close shuts down the local server.
func (s *OAuthServer) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.server != nil {
		_ = s.server.Close()
	}
}

// Ready returns a channel that is closed when the server is listening.
func (s *OAuthServer) Ready() <-chan struct{} {
	return s.ready
}

// ParseRedirectURL extracts the authorization code from a redirect URL or raw code input.
func ParseRedirectURL(input, expectedState string) (string, error) {
	input = strings.TrimSpace(input)

	// Try as full URL with query params
	if strings.Contains(input, "?") || strings.HasPrefix(input, "http") {
		u, err := url.Parse(input)
		if err != nil {
			return "", fmt.Errorf("invalid URL: %w", err)
		}
		q := u.Query()
		code := q.Get("code")
		state := q.Get("state")
		if code == "" {
			return "", fmt.Errorf("no 'code' parameter found in URL")
		}
		if state != expectedState {
			return "", fmt.Errorf("state mismatch: got %q, expected %q", state, expectedState)
		}
		return code, nil
	}

	// Try as bare code (no state validation possible)
	if input == "" {
		return "", fmt.Errorf("empty input")
	}
	return input, nil
}
