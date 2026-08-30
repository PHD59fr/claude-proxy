package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/config"
	"github.com/claude-code-opencode/claude-proxy/internal/log"
	"github.com/claude-code-opencode/claude-proxy/internal/models"
	"github.com/claude-code-opencode/claude-proxy/internal/providers"
	"github.com/claude-code-opencode/claude-proxy/internal/providers/codex"
)

type Server struct {
	cfg     *config.Config
	srv     *http.Server
	handler *Handler
	logger  *log.Logger
	ctx     context.Context
	cancel  context.CancelFunc
}

func NewServer(cfg *config.Config, logger *log.Logger) *Server {
	catalog := models.NewCatalog(cfg.ZenBaseURL, cfg.ZenAPIKey, cfg.ModelCacheTTL)
	router := buildRegistry(cfg)

	handler := NewHandler(cfg, catalog, router, logger)

	ctx, cancel := context.WithCancel(context.Background())

	return &Server{
		cfg:     cfg,
		handler: handler,
		logger:  logger,
		ctx:     ctx,
		cancel:  cancel,
	}
}

// buildRegistry assembles the provider registry from the given config: the
// built-in "zen" upstream, any configured additional upstreams, and the
// optional codex backend.
func buildRegistry(cfg *config.Config) *providers.Registry {
	var codexClient providers.Provider
	if cfg.CodexOAuthToken != "" && cfg.CodexAccountID != "" {
		codexClient = codex.NewClient(codex.CodexBackendURL, cfg.CodexOAuthToken, cfg.CodexAccountID, cfg.RequestTimeout)
	}

	upstreams := []config.UpstreamConfig{{
		Name:    config.DefaultUpstreamName,
		BaseURL: cfg.ZenBaseURL,
		APIKey:  cfg.ZenAPIKey,
	}}
	upstreams = append(upstreams, cfg.Upstreams...)
	return providers.NewRegistry(upstreams, codexClient, cfg.RequestTimeout)
}

func (s *Server) Setup() {
	// Liveness must remain available to container orchestrators even when the
	// inference API is protected. Readiness intentionally stays protected.
	protected := http.NewServeMux()
	protected.HandleFunc("/v1/messages", s.handler.HandleMessages)
	protected.HandleFunc("/v1/models", s.handler.HandleModels)
	protected.HandleFunc("/readyz", s.handler.HandleReadyz)
	protected.HandleFunc("/version", s.handler.HandleVersion)

	var protectedHandler http.Handler = protected
	protectedHandler = bodySizeLimit(s.cfg.MaxBodySize)(protectedHandler)
	protectedHandler = auth(s.cfg.InboundAPIKey, s.cfg.PassthroughAPIKey)(protectedHandler)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handler.HandleHealthz)
	mux.Handle("/", protectedHandler)

	h := requestID(mux)

	s.srv = &http.Server{
		Addr:              s.cfg.ListenAddr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      0, // No timeout for streaming
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

func (s *Server) Start() error {
	// Setup is called before Start in tests; call it here if not already set up
	if s.srv == nil {
		s.Setup()
	}

	// Fetch models when a service credential is available. Pure passthrough
	// mode has no caller credential in a background goroutine.
	if s.cfg.ZenAPIKey != "" {
		go func() {
			if err := s.RefreshOpenCodeModels(s.ctx); err != nil {
				s.logger.Warn("failed to fetch models on startup", "error", err.Error())
			} else {
				s.logger.Info("models fetched successfully")
			}
		}()
	}
	if s.handler.loadRouter().Codex() != nil {
		go func() {
			ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
			defer cancel()
			if err := s.RefreshCodexModels(ctx); err != nil {
				s.logger.Warn("failed to fetch Codex models on startup", "error", err.Error())
			}
		}()
	}

	return s.srv.ListenAndServe()
}

func (s *Server) RefreshOpenCodeModels(ctx context.Context) error {
	if err := s.handler.catalog.FetchWithContext(ctx); err != nil {
		return err
	}
	s.handler.ApplyDiscoveredOpenCodeModels(s.handler.catalog.CachedModels())
	return nil
}

func (s *Server) RefreshCodexModels(ctx context.Context) error {
	client := s.handler.loadRouter().Codex()
	if client == nil {
		return nil
	}
	modelsClient, ok := client.(interface {
		Models(ctx context.Context) ([]codex.ModelInfo, error)
	})
	if !ok {
		return nil
	}
	discovered, err := modelsClient.Models(ctx)
	if err != nil {
		return err
	}
	s.handler.SetDiscoveredCodexModels(discovered)
	return nil
}

func (s *Server) Client() providers.Provider {
	return s.handler.loadRouter().Default()
}

func (s *Server) Catalog() *models.Catalog {
	return s.handler.catalog
}

func (s *Server) Handler() *Handler {
	return s.handler
}

// RebuildRouter creates a new Router from the supplied config and publishes
// it on the handler so subsequent requests use the new upstreams.
func (s *Server) RebuildRouter(cfg *config.Config) {
	s.handler.replaceRouter(buildRegistry(cfg))
	s.handler.catalog.Reconfigure(cfg.ZenBaseURL, cfg.ZenAPIKey, cfg.ModelCacheTTL)
}

// StartTokenRefresh starts a background goroutine that refreshes Codex tokens.
func (s *Server) StartTokenRefresh() {
	if s.cfg.CodexOAuthToken == "" {
		return
	}
	go func() {
		for {
			select {
			case <-s.ctx.Done():
				return
			default:
			}

			tokens, err := codex.LoadTokens()
			if err != nil {
				s.logger.Warn("failed to load tokens for refresh", "error", err.Error())
				time.Sleep(5 * time.Minute)
				continue
			}
			untilExpiry := time.Until(time.UnixMilli(tokens.ExpiresAt))
			waitTime := untilExpiry - 5*time.Minute
			if waitTime < time.Minute {
				waitTime = time.Minute
			}

			select {
			case <-s.ctx.Done():
				return
			case <-time.After(waitTime):
			}

			refreshed, err := codex.RefreshTokens(tokens.RefreshToken)
			if err != nil {
				s.logger.Error("token refresh failed", "error", err.Error())
				continue
			}
			if err := codex.SaveTokens(refreshed); err != nil {
				s.logger.Warn("failed to save refreshed token", "error", err.Error())
			}
			if client := s.handler.loadRouter().Codex(); client != nil {
				if c, ok := client.(interface{ UpdateToken(token string) }); ok {
					c.UpdateToken(refreshed.AccessToken)
				}
			}
			s.logger.Info("codex token refreshed",
				"expires", time.UnixMilli(refreshed.ExpiresAt).Format(time.RFC3339),
			)
		}
	}()
}

// StartConfigWatcher watches the config file for changes and reloads.
func (s *Server) StartConfigWatcher() {
	go func() {
		configPath := codex.ConfigFilePath()
		var lastMod time.Time

		// Get initial modification time
		if info, err := os.Stat(configPath); err == nil {
			lastMod = info.ModTime()
		}

		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				info, err := os.Stat(configPath)
				if err != nil {
					continue
				}

				if info.ModTime().After(lastMod) {
					lastMod = info.ModTime()
					s.reloadConfig(configPath)
				}
			}
		}
	}()
}

func (s *Server) reloadConfig(configPath string) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		s.logger.Warn("failed to read config file", "error", err.Error())
		return
	}

	// Parse token fields
	var cfg struct {
		CodexOAuthToken string `json:"codex_oauth_token"`
		CodexAccountID  string `json:"codex_account_id"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		s.logger.Warn("failed to parse config file", "error", err.Error())
		return
	}

	if cfg.CodexOAuthToken == "" || cfg.CodexAccountID == "" {
		s.logger.Warn("ignoring incomplete codex config reload")
		return
	}

	// Publish a fully initialized client so in-flight requests retain their
	// existing client while later requests observe the new credentials.
	s.handler.loadRouter().SetCodex(codex.NewClient(
		codex.CodexBackendURL,
		cfg.CodexOAuthToken,
		cfg.CodexAccountID,
		s.cfg.RequestTimeout,
	))
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		defer cancel()
		if err := s.RefreshCodexModels(ctx); err != nil {
			s.logger.Warn("failed to refresh Codex models after login", "error", err.Error())
		}
	}()
	// Invalidate the token cache so the refresh goroutine reads the new
	// credentials from disk instead of using stale in-memory tokens.
	codex.InvalidateTokenCache()
	s.logger.Info("codex config reloaded")
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.cancel()
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}
