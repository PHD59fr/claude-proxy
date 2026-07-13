package web

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/config"
)

// dashboardHTML holds the web dashboard markup, embedded from dashboard.html
// so the markup stays out of the Go source.
//
//go:embed dashboard.html
var dashboardHTML string

// pageHTML is the trimmed dashboard markup served at "/".
var pageHTML = strings.TrimSpace(dashboardHTML)

// ModelProvider abstracts the proxy's model probing and configuration.
type ModelProvider interface {
	GetConfig() *config.Config
	GetVersion() string
	GetModelStatuses() []ModelStatus
	TestModel(ctx context.Context, name string) error
	ResetCircuitBreakers()
	ReorderModels(names []string)
	SaveConfig(config.FileConfig) error
	StartCodexLogin() (string, error)
	CodexLoginStatus() map[string]string
}

type ModelStatus struct {
	Name          string     `json:"name"`
	Upstream      string     `json:"upstream"`
	OK            bool       `json:"ok"`
	Tested        bool       `json:"tested"`
	CheckedAt     *time.Time `json:"checked_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	DisabledUntil *time.Time `json:"disabled_until,omitempty"`
	Configured    bool       `json:"configured"`
	Order         int        `json:"order"`
}

type Server struct {
	provider ModelProvider
	mux      *http.ServeMux
	addr     string
	key      string
}

func NewServer(addr, key string, provider ModelProvider) *Server {
	s := &Server{provider: provider, addr: addr, key: key}
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/", s.requireAuth(s.handleIndex))
	s.mux.HandleFunc("/api/models", s.requireAuth(s.handleModels))
	s.mux.HandleFunc("/api/models/reorder", s.requireAuth(s.handleReorder))
	s.mux.HandleFunc("/api/config", s.requireAuth(s.handleConfig))
	s.mux.HandleFunc("/api/circuit-breaker/reset", s.requireAuth(s.handleResetBreakers))
	s.mux.HandleFunc("/api/models/test", s.requireAuth(s.handleTestModel))
	s.mux.HandleFunc("/api/config/save", s.requireAuth(s.handleSaveConfig))
	s.mux.HandleFunc("/api/codex/login", s.requireAuth(s.handleCodexLogin))
	s.mux.HandleFunc("/api/codex/login/status", s.requireAuth(s.handleCodexLoginStatus))
	return s
}

func (s *Server) validKey(token string) bool {
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.key)) == 1
}

func (s *Server) authorized(r *http.Request) bool {
	token := r.Header.Get("Authorization")
	if strings.HasPrefix(token, "Bearer ") {
		return s.validKey(strings.TrimPrefix(token, "Bearer "))
	}
	if cookie, err := r.Cookie("claude_proxy_web_key"); err == nil {
		return s.validKey(cookie.Value)
	}
	return s.validKey(r.URL.Query().Get("key"))
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "web interface authentication required", http.StatusUnauthorized)
			return
		}
		// A one-time query parameter lets a browser establish a same-site session
		// cookie; subsequent API calls do not need to expose the key in URLs.
		if key := r.URL.Query().Get("key"); s.validKey(key) {
			http.SetCookie(w, &http.Cookie{Name: "claude_proxy_web_key", Value: key, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		}
		next(w, r)
	}
}

func (s *Server) Provider() ModelProvider {
	return s.provider
}

func (s *Server) Start() error {
	srv := &http.Server{
		Addr:              s.addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("web interface on http://%s", s.addr)
	return srv.ListenAndServe()
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(pageHTML))
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	statuses := s.provider.GetModelStatuses()
	_ = json.NewEncoder(w).Encode(statuses)
}

func (s *Server) handleReorder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Models []string `json:"models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if len(req.Models) == 0 {
		http.Error(w, "empty model list", http.StatusBadRequest)
		return
	}
	s.provider.ReorderModels(req.Models)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	cfg := s.provider.GetConfig()

	// Build the effective config response with sources.
	type secretStatus struct {
		Configured bool   `json:"configured"`
		WriteOnly  bool   `json:"write_only"`
		Source     string `json:"source,omitempty"`
	}
	type fieldInfo struct {
		Value  interface{} `json:"value"`
		Source string      `json:"source,omitempty"`
		Locked bool        `json:"locked,omitempty"`
	}

	port := ""
	if m := strings.Split(cfg.ListenAddr, ":"); len(m) > 0 {
		port = m[len(m)-1]
	}

	resp := map[string]interface{}{
		"version":             s.provider.GetVersion(),
		"listen_port":         fieldInfo{Value: port, Source: cfg.Sources["listen_addr"]},
		"web_interface_port":  fieldInfo{Value: cfg.WebInterfacePort, Source: cfg.Sources["web_interface_port"]},
		"reasoning_model":     fieldInfo{Value: cfg.ReasoningModel, Source: cfg.Sources["reasoning_model"]},
		"completion_model":    fieldInfo{Value: cfg.CompletionModel, Source: cfg.Sources["completion_model"]},
		"models":              fieldInfo{Value: cfg.Models, Source: cfg.Sources["models"]},
		"allow_unlisted":      fieldInfo{Value: cfg.AllowUnlisted, Source: cfg.Sources["allow_unlisted_models"]},
		"expose_all":          fieldInfo{Value: cfg.ExposeAllModels, Source: cfg.Sources["expose_all_models"]},
		"passthrough":         fieldInfo{Value: cfg.PassthroughAPIKey, Source: cfg.Sources["passthrough_api_key"]},
		"request_timeout":     fieldInfo{Value: cfg.RequestTimeout.String(), Source: cfg.Sources["request_timeout"]},
		"model_cache_ttl":     fieldInfo{Value: cfg.ModelCacheTTL.String(), Source: cfg.Sources["model_cache_ttl"]},
		"max_body_size":       fieldInfo{Value: cfg.MaxBodySize, Source: cfg.Sources["max_body_size"]},
		"log_level":           fieldInfo{Value: cfg.LogLevel, Source: cfg.Sources["log_level"]},
		"log_format":          fieldInfo{Value: cfg.LogFormat, Source: cfg.Sources["log_format"]},
		"upstream_base_url":   fieldInfo{Value: cfg.UpstreamBaseURL, Source: cfg.Sources["upstream_base_url"]},
		"upstream_api_key":    secretStatus{Configured: cfg.UpstreamAPIKey != "", WriteOnly: true, Source: cfg.Sources["upstream_api_key"]},
		"inbound_api_key":     secretStatus{Configured: cfg.InboundAPIKey != "", WriteOnly: true, Source: cfg.Sources["inbound_api_key"]},
		"web_interface_key":   secretStatus{Configured: cfg.WebInterfaceKey != "", WriteOnly: true, Source: cfg.Sources["web_interface_key"]},
		"codex_account_id":    fieldInfo{Value: cfg.CodexAccountID, Source: cfg.Sources["codex_account_id"]},
		"codex_oauth_token":   secretStatus{Configured: cfg.CodexOAuthToken != "", WriteOnly: true, Source: cfg.Sources["codex_oauth_token"]},
		"codex_refresh_token": secretStatus{Configured: cfg.CodexRefreshToken != "", WriteOnly: true, Source: cfg.Sources["codex_refresh_token"]},
	}

	// Upstreams with masked keys
	type upstreamInfo struct {
		Name    string       `json:"name"`
		BaseURL string       `json:"base_url"`
		APIKey  secretStatus `json:"api_key"`
	}
	ups := make([]upstreamInfo, len(cfg.Upstreams))
	for i, u := range cfg.Upstreams {
		ups[i] = upstreamInfo{Name: u.Name, BaseURL: u.BaseURL, APIKey: secretStatus{Configured: u.APIKey != "", WriteOnly: true}}
	}
	resp["upstreams"] = fieldInfo{Value: ups, Source: cfg.Sources["upstreams"]}

	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var cfg config.FileConfig
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&cfg); err != nil {
		http.Error(w, "invalid configuration", http.StatusBadRequest)
		return
	}
	if err := s.provider.SaveConfig(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true,"restart_required":true}`))
}

func (s *Server) handleCodexLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	authURL, err := s.provider.StartCodexLogin()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"auth_url": authURL, "status": "pending"})
}

func (s *Server) handleCodexLoginStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.provider.CodexLoginStatus())
}

func (s *Server) handleResetBreakers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.provider.ResetCircuitBreakers()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (s *Server) handleTestModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "missing model name", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	err := s.provider.TestModel(ctx, req.Name)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok": false, "error": err.Error(),
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true,
	})
}

// ─── DefaultProvider wraps the proxy handler ────────────────────────────────

type DefaultProvider struct {
	cfg           *config.Config
	version       string
	handlerProbe  func(ctx context.Context, name string) error
	getStatuses   func() []ModelStatus
	resetBreakers func()
	saveConfig    func(config.FileConfig) error
	persistConfig func(*config.Config) error // persists full config to disk
	codexLogin    func() (string, error)
	loginMu       sync.Mutex
	loginStatus   string // "idle", "pending", "completed", "error"
	loginURL      string
	loginError    string
}

func NewDefaultProvider(cfg *config.Config, version string, handlerProbe func(ctx context.Context, name string) error, getStatuses func() []ModelStatus, resetBreakers func(), saveConfig func(config.FileConfig) error, codexLogin func() (string, error)) *DefaultProvider {
	return &DefaultProvider{
		cfg: cfg, version: version, handlerProbe: handlerProbe, getStatuses: getStatuses,
		resetBreakers: resetBreakers, saveConfig: saveConfig, codexLogin: codexLogin,
	}
}

// SetPersistConfig sets the callback used to persist config to disk after
// runtime mutations like model reorder.
func (p *DefaultProvider) SetPersistConfig(fn func(*config.Config) error) {
	p.persistConfig = fn
}

func (p *DefaultProvider) GetConfig() *config.Config {
	return p.cfg
}

func (p *DefaultProvider) GetVersion() string {
	return p.version
}

func (p *DefaultProvider) GetModelStatuses() []ModelStatus {
	if p.getStatuses != nil {
		return p.getStatuses()
	}
	return nil
}

func (p *DefaultProvider) TestModel(ctx context.Context, name string) error {
	if p.handlerProbe != nil {
		return p.handlerProbe(ctx, name)
	}
	return fmt.Errorf("no probe function configured")
}

func (p *DefaultProvider) SaveConfig(cfg config.FileConfig) error {
	if p.saveConfig == nil {
		return fmt.Errorf("configuration saving is unavailable")
	}
	return p.saveConfig(cfg)
}

func (p *DefaultProvider) ResetCircuitBreakers() {
	if p.resetBreakers != nil {
		p.resetBreakers()
	}
}

func (p *DefaultProvider) StartCodexLogin() (string, error) {
	if p.codexLogin == nil {
		return "", fmt.Errorf("codex login is not available")
	}
	p.loginMu.Lock()
	if p.loginStatus == "pending" {
		url := p.loginURL
		p.loginMu.Unlock()
		return url, nil
	}
	p.loginMu.Unlock()

	// The closure returns (authURL, nil) immediately and handles completion
	// in a background goroutine. It is responsible for setting login status.
	authURL, err := p.codexLogin()
	if err != nil {
		p.loginMu.Lock()
		p.loginStatus = "error"
		p.loginError = err.Error()
		p.loginMu.Unlock()
		return "", err
	}

	p.loginMu.Lock()
	p.loginStatus = "pending"
	p.loginURL = authURL
	p.loginError = ""
	p.loginMu.Unlock()

	return authURL, nil
}

func (p *DefaultProvider) CodexLoginStatus() map[string]string {
	p.loginMu.Lock()
	defer p.loginMu.Unlock()
	status := map[string]string{"status": p.loginStatus}
	if p.loginError != "" {
		status["error"] = p.loginError
	}
	return status
}

func (p *DefaultProvider) SetLoginComplete() {
	p.loginMu.Lock()
	defer p.loginMu.Unlock()
	p.loginStatus = "completed"
	p.loginError = ""
}

func (p *DefaultProvider) SetLoginError(msg string) {
	p.loginMu.Lock()
	defer p.loginMu.Unlock()
	p.loginStatus = "error"
	p.loginError = msg
}

func (p *DefaultProvider) ReorderModels(names []string) {
	var newModels []config.ModelSpec
	seen := make(map[string]bool)
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		for _, spec := range p.cfg.EffectiveModels() {
			if spec.Name == name {
				newModels = append(newModels, spec)
				break
			}
		}
	}
	if len(newModels) == 0 {
		return
	}
	p.cfg.Models = newModels
	p.cfg.Precompute()
	// Persist the new order to disk so it survives restarts.
	if p.persistConfig != nil {
		_ = p.persistConfig(p.cfg)
	}
}
