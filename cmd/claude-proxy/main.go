package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/claude-code-opencode/claude-proxy/internal/config"
	"github.com/claude-code-opencode/claude-proxy/internal/log"
	"github.com/claude-code-opencode/claude-proxy/internal/models"
	"github.com/claude-code-opencode/claude-proxy/internal/providers/codex"
	"github.com/claude-code-opencode/claude-proxy/internal/providers/errs"
	"github.com/claude-code-opencode/claude-proxy/internal/providers/zen"
	"github.com/claude-code-opencode/claude-proxy/internal/proxy"
	"github.com/claude-code-opencode/claude-proxy/internal/web"
)

// version is overwritten at build time with -ldflags. The fallback guarantees
// the banner remains informative for go run and builds without Make/Docker.
var version = "dev"

func main() {
	args := os.Args[1:]

	if len(args) == 0 {
		printUsage()
		os.Exit(0)
	}

	switch args[0] {
	case "version":
		cmdVersion()
	case "models":
		cmdModels(args[1:])
	case "check":
		cmdCheck(args[1:])
	case "healthcheck":
		cmdHealthcheck(args[1:])
	case "serve":
		cmdServe(args[1:])
	case "codex-login":
		cmdCodexLogin()
	case "config":
		cmdConfig(args[1:])
	case "-h", "--help", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

func persistConfig(cfg *config.Config) error {
	path := cfg.ConfigFile
	if path == "" {
		path = "config.json"
	}
	data, err := json.MarshalIndent(configToFileConfig(cfg), "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}

func generateWebInterfaceKey() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func cmdHealthcheck(args []string) {
	// Load config to respect the configured listen address. When no config
	// file exists the default 127.0.0.1:3000 is used.
	cfg, err := config.Load(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck config load:", err)
		os.Exit(1)
	}
	addr := cfg.ListenAddr
	if addr == "" {
		addr = "127.0.0.1:3000"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck request:", err)
		os.Exit(1)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck failed:", err)
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck returned status %d\n", resp.StatusCode)
		os.Exit(1)
	}
}

func cmdVersion() {
	fmt.Printf("claude-proxy %s\n", version)
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: claude-proxy <command> [flags]

Commands:
  serve          Start the proxy server
  config         Interactive configuration wizard (--show, --export)
  codex-login    Authenticate with ChatGPT for Codex backend
  version        Print version
  models         List available models
  check          Validate config and upstream connectivity
  healthcheck    Check local liveness endpoint
  help           Show this help message

Global flags:
  --config <path>        Path to JSON config file

Run 'claude-proxy serve --help' for serve flags.
`)
}

type modelStatus struct {
	name string
	ok   bool
}

func printBanner(cfg *config.Config, h *proxy.Handler, v string, modelStatuses []modelStatus) {
	const w = 55

	center := func(s string) string {
		rw := utf8.RuneCountInString(s)
		if rw >= w {
			return string([]rune(s)[:w])
		}
		pad := (w - rw) / 2
		right := w - rw - pad
		return strings.Repeat(" ", pad) + s + strings.Repeat(" ", right)
	}

	top := "╔" + strings.Repeat("═", w+2) + "╗"
	mid := "╠" + strings.Repeat("═", w+2) + "╣"
	bot := "╚" + strings.Repeat("═", w+2) + "╝"

	line := func(s string) string {
		// ANSI colour escapes have zero terminal width. Strip the sequences for
		// padding so coloured model status indicators keep the box aligned.
		visible := strings.NewReplacer("\033[32m", "", "\033[31m", "", "\033[0m", "").Replace(s)
		rw := utf8.RuneCountInString(visible)
		if rw < w {
			s += strings.Repeat(" ", w-rw)
		}
		return "║ " + s + " ║"
	}

	title := "claude-proxy"
	if v != "" {
		title += "  v" + v
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, top)
	fmt.Fprintln(os.Stderr, line(center(title)))
	fmt.Fprintln(os.Stderr, mid)
	fmt.Fprintln(os.Stderr, line("  Version:  "+v))
	fmt.Fprintln(os.Stderr, line("  Port:     "+extractPort(cfg.ListenAddr)))

	if len(cfg.Models) > 0 {
		// Show ordered models, dropping Codex models when Codex isn't configured.
		codexAvailable := cfg.CodexOAuthToken != ""
		shownModels := make([]config.ModelSpec, 0, len(cfg.Models))
		for _, m := range cfg.EffectiveModels() {
			if m.Upstream == config.CodexUpstreamName && !codexAvailable {
				continue
			}
			if h.IsModelUpstreamUnavailable(m.Upstream, m.Name) {
				// The upstream no longer serves this model — hide it.
				continue
			}
			shownModels = append(shownModels, m)
		}
		if len(shownModels) > 0 {
			fmt.Fprintln(os.Stderr, mid)
			fmt.Fprintln(os.Stderr, line("  Ordered models (proxy priority)"))
			for i, m := range shownModels {
				marker := "  "
				if i == 0 {
					marker = "* "
				}
				label := m.Name + "@" + m.Upstream
				fmt.Fprintln(os.Stderr, line(fmt.Sprintf("    %s%d. %s", marker, i+1, label)))
			}
		}
	}

	if len(modelStatuses) > 0 {
		fmt.Fprintln(os.Stderr, mid)
		fmt.Fprintln(os.Stderr, line("  Available models"))
		for _, ms := range modelStatuses {
			icon := "\033[32m●\033[0m"
			if !ms.ok {
				icon = "\033[31m●\033[0m"
			}
			fmt.Fprintln(os.Stderr, line("    "+icon+" "+ms.name))
		}
	}

	env := []string{
		"export ANTHROPIC_BASE_URL=http://127.0.0.1:" + extractPort(cfg.ListenAddr),
		"export ANTHROPIC_AUTH_TOKEN=unused",
		"export ANTHROPIC_MODEL=custom",
		"export CLAUDE_CODE_SUBAGENT_MODEL=custom",
		"export CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1",
		"unset ANTHROPIC_API_KEY",
	}

	fmt.Fprintln(os.Stderr, mid)
	fmt.Fprintln(os.Stderr, line("  How to use:"))
	for _, e := range env {
		fmt.Fprintln(os.Stderr, line("  "+e))
	}
	fmt.Fprintln(os.Stderr, line("  Then run:"))
	fmt.Fprintln(os.Stderr, line("    claude --model custom"))
	fmt.Fprintln(os.Stderr, bot)
	fmt.Fprintln(os.Stderr)
}

func cmdServe(args []string) {
	cfg, err := config.Load(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// If --config was provided, use that path for token storage too
	if cfg.ConfigFile != "" {
		codex.SetConfigFilePath(cfg.ConfigFile)
	}

	if issues := cfg.Validate(); len(issues) > 0 {
		for _, issue := range issues {
			fmt.Fprintf(os.Stderr, "Config error: %s\n", issue)
		}
		os.Exit(1)
	}

	// Codex: auto-load tokens if available
	if tokens, err := codex.LoadTokens(); err == nil {
		if codex.ShouldRefreshToken(tokens) {
			fmt.Println("Refreshing Codex OAuth token...")
			refreshed, err := codex.RefreshTokens(tokens.RefreshToken)
			if err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  Token refresh failed: %v\n", err)
				fmt.Fprintf(os.Stderr, "   Run 'claude-proxy codex-login' to re-authenticate.\n\n")
				os.Exit(1)
			}
			if err := codex.SaveTokens(refreshed); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not save refreshed token: %v\n", err)
			}
			cfg.CodexOAuthToken = refreshed.AccessToken
			cfg.CodexAccountID = refreshed.AccountID
			fmt.Printf("✓ Codex token refreshed (expires %s)\n\n", time.UnixMilli(refreshed.ExpiresAt).Format(time.RFC3339))
		} else {
			cfg.CodexOAuthToken = tokens.AccessToken
			cfg.CodexAccountID = tokens.AccountID
		}
	}

	logger := log.New(cfg.LogLevel, cfg.LogFormat)
	srv := proxy.NewServer(cfg, logger)

	proxy.SetVersion(version)
	srv.Setup()
	srv.StartTokenRefresh()
	srv.StartConfigWatcher()

	// Run model availability probes BEFORE starting the HTTP server, so
	// global rate limits are detected and upstreams pre-disabled before
	// any real request can trigger a cascade.
	modelStatuses := checkModelsAtStartup(cfg.ResolvedConfig(), srv)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start()
	}()

	// The running banner lists the available models; log "server starting" only
	// afterwards so it does not appear above the model table in the console.
	printBanner(srv.Handler().GetConfig(), srv.Handler(), version, modelStatuses)
	logger.Info("server starting", "addr", cfg.ListenAddr)

	// Start web interface if configured
	if cfg.WebInterfacePort != "" {
		if cfg.WebInterfaceKey == "" {
			key, err := generateWebInterfaceKey()
			if err != nil {
				logger.Error("failed to generate web interface key", "error", err.Error())
				os.Exit(1)
			}
			cfg.WebInterfaceKey = key
			if err := persistConfig(cfg); err != nil {
				logger.Error("failed to persist web interface key", "error", err.Error())
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "\nWeb interface key (shown once): %s\nOpen: http://127.0.0.1:%s/?key=%s\n\n", key, cfg.WebInterfacePort, key)
		}
		webAddr := "0.0.0.0:" + cfg.WebInterfacePort
		var webProvider *web.DefaultProvider
		webProvider = web.NewDefaultProvider(cfg, version, func(ctx context.Context, name string) error {
			return srv.Handler().TestModel(ctx, name)
		}, func() []web.ModelStatus {
			statuses := srv.Handler().GetModelStatuses()
			out := make([]web.ModelStatus, len(statuses))
			for i, status := range statuses {
				out[i] = web.ModelStatus{Name: status.Name, Upstream: status.Upstream, OK: status.OK, Tested: status.Tested, CheckedAt: status.CheckedAt, LastError: status.LastError, DisabledUntil: status.DisabledUntil, Configured: status.Configured, Order: status.Order}
			}
			return out
		}, srv.Handler().ResetCircuitBreakers, func(fileCfg config.FileConfig) error {
			// Secrets are write-only in the web document. Keep the existing web
			// administration key and Codex tokens unless an explicit CLI flow
			// replaces them.
			next := config.DefaultConfig()
			applyFileConfig(next, &fileCfg)
			// The web form does not submit model data (ordering is managed via
			// the reorder endpoint). Preserve the running model selection so a
			// plain config save does not wipe the configured "models" list.
			if len(fileCfg.Models) == 0 {
				next.Models = cfg.Models
			}
			// Preserve write-only API keys when the UI submits an existing upstream
			// without a replacement key.
			if next.ZenAPIKey == "" {
				next.ZenAPIKey = cfg.ZenAPIKey
			}
			for i := range next.Upstreams {
				if next.Upstreams[i].APIKey != "" {
					continue
				}
				for _, current := range cfg.Upstreams {
					if current.Name == next.Upstreams[i].Name {
						next.Upstreams[i].APIKey = current.APIKey
						break
					}
				}
			}
			next.WebInterfaceKey = cfg.WebInterfaceKey
			next.CodexOAuthToken = cfg.CodexOAuthToken
			next.CodexAccountID = cfg.CodexAccountID
			next.CodexRefreshToken = cfg.CodexRefreshToken
			next.ConfigFile = cfg.ConfigFile
			next.Precompute()
			if issues := next.Validate(); len(issues) > 0 {
				return fmt.Errorf("invalid configuration: %s", strings.Join(issues, "; "))
			}
			// Publish the new config atomically so concurrent handlers see a
			// consistent snapshot — no field-by-field struct copy race.
			srv.Handler().UpdateConfig(next)
			// Rebuild the router so new/changed upstreams are immediately usable.
			srv.RebuildRouter(next)
			// Keep the local cfg pointer in sync for the save and Codex flows.
			cfg = next
			webProvider.SetConfig(next)
			return persistConfig(cfg)
		}, func() (string, error) {
			pkce, err := codex.GeneratePKCE()
			if err != nil {
				return "", err
			}
			state, err := codex.GenerateState()
			if err != nil {
				return "", err
			}
			oauthSrv := codex.StartOAuthServer(state)
			authURL := codex.BuildAuthorizeURL(pkce, state)

			// Wait for callback in background goroutine
			go func() {
				code, waitErr := oauthSrv.WaitForCode()
				oauthSrv.Close()
				if waitErr != nil {
					webProvider.SetLoginError(waitErr.Error())
					return
				}
				tokens, exErr := codex.ExchangeCode(code, pkce.Verifier)
				if exErr != nil {
					webProvider.SetLoginError(exErr.Error())
					return
				}
				// Atomically update the running config to avoid race with web callback.
				// Get current config, apply token updates, persist and publish.
				current := srv.Handler().GetConfig()
				next := *current
				next.CodexOAuthToken = tokens.AccessToken
				next.CodexAccountID = tokens.AccountID
				next.Precompute()
				if issues := next.Validate(); len(issues) > 0 {
					webProvider.SetLoginError("invalid config after token update: " + strings.Join(issues, "; "))
					return
				}
				// Publish atomically so concurrent handlers see consistent snapshot.
				srv.Handler().UpdateConfig(&next)
				srv.RebuildRouter(&next)
				// Persist to disk.
				if saveErr := persistConfig(&next); saveErr != nil {
					webProvider.SetLoginError(saveErr.Error())
					return
				}
				// Keep local cfg in sync for any subsequent operations in main().
				cfg = &next
				webProvider.SetConfig(&next)
				webProvider.SetLoginComplete()
			}()

			return authURL, nil
		})
		webProvider.SetPersistConfig(func(c *config.Config) error {
			return persistConfig(c)
		})
		// Model reorder must take effect on the runtime handler immediately, not
		// only after a restart. Apply it to the handler's atomic config, keep the
		// local cfg/provider pointers in sync, then persist the new order.
		webProvider.SetReorderModels(func(names []string) error {
			srv.Handler().ReorderModels(names)
			updated := srv.Handler().GetConfig()
			cfg = updated
			webProvider.SetConfig(updated)
			return persistConfig(updated)
		})
		webSrv := web.NewServer(webAddr, cfg.WebInterfaceKey, webProvider)
		go func() {
			if err := webSrv.Start(); err != nil {
				logger.Error("web interface error", "error", err.Error())
			}
		}()
	}

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigCh:
		// Graceful shutdown requested
	case err := <-errCh:
		if err != nil {
			logger.Error("server error", "error", err.Error())
		}
	}

	logger.Info("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "error", err.Error())
	}
}

func checkModelsAtStartup(cfg *config.Config, srv *proxy.Server) []modelStatus {
	const probeTimeout = 10 * time.Second

	var statuses []modelStatus
	seen := make(map[string]bool)

	h := srv.Handler()
	add := func(name, upstream string, check func(context.Context) error) bool {
		if seen[name] {
			return true
		}
		seen[name] = true
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		err := check(ctx)
		cancel()
		if errors.Is(err, errs.ErrModelUnavailable) {
			// The upstream no longer serves this model: mark it so routing
			// skips it and listings hide it, and keep it out of the banner.
			h.MarkModelUpstreamUnavailable(upstream, name)
			return false
		}
		if errors.Is(err, errs.ErrRateLimited) {
			// Model is rate limited at startup: pre-blacklist for circuit breaker
			// cooldown so it's not tried on real requests.
			h.DisableModelUpstream(upstream, name, "rate_limit")
			statuses = append(statuses, modelStatus{name: name, ok: false})
			return false
		}
		statuses = append(statuses, modelStatus{name: name, ok: err == nil})
		h.RecordModelProbe(name, err)
		return err == nil
	}
	addDiscovered := func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		statuses = append(statuses, modelStatus{name: name, ok: true})
	}

	// Probe all configured models at startup to know which are available.
	// This avoids free-tier quota surprises on first real request.
	configuredModels := cfg.EffectiveModels()
	// Build a single Codex client up front and reuse it for every codex model
	// probe, rather than allocating one per model in the loop below.
	var codexClient *codex.Client
	if cfg.CodexOAuthToken != "" {
		codexClient = codex.NewClient(codex.CodexBackendURL, cfg.CodexOAuthToken, cfg.CodexAccountID, probeTimeout)
	}
	for _, spec := range configuredModels {
		if spec.Upstream == config.CodexUpstreamName {
			if codexClient == nil {
				add(spec.Name, spec.Upstream, func(context.Context) error { return fmt.Errorf("codex is not authenticated") })
			} else {
				c := codexClient
				add(spec.Name, spec.Upstream, func(ctx context.Context) error { return c.CheckModel(ctx, spec.Name) })
			}
		} else {
			client, err := upstreamClientForSpec(cfg, spec)
			if err == nil && (spec.Upstream != config.DefaultUpstreamName || !cfg.PassthroughAPIKey || cfg.ZenAPIKey != "") {
				add(spec.Name, spec.Upstream, func(ctx context.Context) error { return checkOpenCodeCompatibleModel(ctx, client, spec) })
			}
		}
	}

	// Discover the account's current Zen catalog without probing.
	if cfg.ZenAPIKey != "" {
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		err := srv.RefreshOpenCodeModels(ctx)
		cancel()
		if err == nil {
			for _, model := range srv.Catalog().CachedModels() {
				if config.IsOpenCodeModelSupported(model.ID) {
					addDiscovered(model.ID)
				}
			}
		}
	}

	// Codex's model endpoint is already scoped to the authenticated account.
	if cfg.CodexOAuthToken != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := srv.RefreshCodexModels(ctx)
		cancel()
		if err == nil {
			for _, model := range srv.Handler().DiscoveredCodexModels() {
				addDiscovered(model.Slug)
			}
		}
	}

	return statuses
}

func checkOpenCodeCompatibleModel(ctx context.Context, client *zen.Client, spec config.ModelSpec, authOverride ...string) error {
	if config.UsesOpenCodeResponsesAPI(spec.Name, spec.Upstream) {
		return client.CheckResponses(ctx, spec.Name, authOverride...)
	}
	return client.CheckChatCompletion(ctx, spec.Name, authOverride...)
}

func upstreamClientForSpec(cfg *config.Config, spec config.ModelSpec) (*zen.Client, error) {
	if spec.Upstream == config.DefaultUpstreamName {
		return zen.NewClient(spec.Upstream, cfg.ZenBaseURL, cfg.ZenAPIKey, 10*time.Second), nil
	}
	for _, candidate := range cfg.Upstreams {
		if candidate.Name == spec.Upstream {
			return zen.NewClient(spec.Upstream, candidate.BaseURL, candidate.APIKey, 10*time.Second), nil
		}
	}
	return nil, fmt.Errorf("unknown upstream %q", spec.Upstream)
}

func cmdModels(args []string) {
	cfg, err := config.Load(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// If --config was provided, use that path for token storage too
	if cfg.ConfigFile != "" {
		codex.SetConfigFilePath(cfg.ConfigFile)
	}

	catalog := models.NewCatalog(cfg.ZenBaseURL, cfg.ZenAPIKey, cfg.ModelCacheTTL)
	var upstreamModels []models.ModelEntry
	if cfg.PassthroughAPIKey && cfg.ZenAPIKey == "" {
		fmt.Println("OpenCode catalog skipped: passthrough mode requires a caller credential")
	} else if err := catalog.Fetch(); err != nil {
		fmt.Fprintf(os.Stderr, "OpenCode catalog error: %v\n", err)
	} else {
		upstreamModels = catalog.GetModels(false)
	}

	// Codex models available to this ChatGPT account.
	if tokens, err := codex.LoadTokens(); err == nil {
		codexClient := codex.NewClient(codex.CodexBackendURL, tokens.AccessToken, tokens.AccountID, cfg.RequestTimeout)
		fmt.Println("Codex models (ChatGPT subscription):")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		discovered, fetchErr := codexClient.Models(ctx)
		cancel()
		if fetchErr != nil {
			fmt.Printf("  unavailable: %v\n", fetchErr)
		}
		for _, model := range discovered {
			id := model.Slug
			suffix := ""
			if id == cfg.DefaultModel {
				suffix = " (default)"
			}
			fmt.Printf("  %s ✅%s\n", id, suffix)
		}
		fmt.Println()
	}

	compatible := make([]models.ModelEntry, 0, len(upstreamModels))
	for _, model := range upstreamModels {
		if config.IsOpenCodeModelSupported(model.ID) {
			compatible = append(compatible, model)
		}
	}
	fmt.Println("OpenCode Zen models supported by this proxy:")
	for _, m := range compatible {
		suffix := ""
		if m.ID == cfg.DefaultModel {
			suffix = " (default)"
		}
		fmt.Printf("  %s ✅%s\n", m.ID, suffix)
	}

	fmt.Printf("\nTotal: %d supported Zen, %d returned by Zen\n", len(compatible), len(upstreamModels))
}

func cmdCheck(args []string) {
	cfg, err := config.Load(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== Configuration ===")
	fmt.Printf("Listen:      %s\n", cfg.ListenAddr)
	fmt.Printf("Upstream:    %s\n", cfg.ZenBaseURL)
	fmt.Printf("API Key:     %s\n", cfg.MaskedKey())
	fmt.Printf("Default:     %s\n", cfg.DefaultModel)
	fmt.Printf("Passthrough: %v\n", cfg.PassthroughAPIKey)

	// If --config was provided, use that path for token storage too
	if cfg.ConfigFile != "" {
		codex.SetConfigFilePath(cfg.ConfigFile)
	}

	// Codex: auto-load tokens if available
	if tokens, err := codex.LoadTokens(); err == nil {
		if codex.ShouldRefreshToken(tokens) {
			refreshed, err := codex.RefreshTokens(tokens.RefreshToken)
			if err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  Codex token refresh failed: %v\n", err)
			} else {
				if err := codex.SaveTokens(refreshed); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not save refreshed token: %v\n", err)
				}
				cfg.CodexOAuthToken = refreshed.AccessToken
				cfg.CodexAccountID = refreshed.AccountID
			}
		} else {
			cfg.CodexOAuthToken = tokens.AccessToken
			cfg.CodexAccountID = tokens.AccountID
		}
		fmt.Printf("Codex:       ✅ %s\n", cfg.CodexAccountID)
	} else {
		fmt.Printf("Codex:       ❌ not configured\n")
	}
	fmt.Println()

	if issues := cfg.Validate(); len(issues) > 0 {
		fmt.Println("❌ Config validation errors:")
		for _, issue := range issues {
			fmt.Printf("  - %s\n", issue)
		}
		os.Exit(1)
	}
	fmt.Println("✅ Config validation passed")

	ctx := context.Background()
	if len(cfg.EffectiveModels()) == 0 {
		catalog := models.NewCatalog(cfg.ZenBaseURL, cfg.ZenAPIKey, cfg.ModelCacheTTL)
		if err := catalog.FetchWithContext(ctx); err != nil {
			fmt.Printf("❌ OpenCode model discovery: %v\n", err)
			os.Exit(1)
		}
		for _, model := range models.FilteredModels(catalog.CachedModels()) {
			if config.IsOpenCodeModelSupported(model.ID) && !config.IsOpenCodeContributorFree(model.ID) {
				cfg.Models = append(cfg.Models, config.ModelSpec{Name: model.ID, Upstream: config.DefaultUpstreamName})
			}
		}
		cfg.Precompute()
		if len(cfg.EffectiveModels()) == 0 {
			fmt.Println("❌ OpenCode model discovery returned no compatible free model")
			os.Exit(1)
		}
	}
	primary := cfg.EffectiveModels()[0]
	fmt.Println("\n=== Primary Route ===")
	var primaryErr error
	switch primary.Upstream {
	case config.CodexUpstreamName:
		if cfg.CodexOAuthToken == "" {
			primaryErr = fmt.Errorf("codex is not authenticated")
		} else {
			client := codex.NewClient(codex.CodexBackendURL, cfg.CodexOAuthToken, cfg.CodexAccountID, cfg.RequestTimeout)
			primaryErr = client.CheckModel(ctx, primary.Name)
		}
	default:
		client, err := upstreamClientForSpec(cfg, primary)
		if err != nil {
			primaryErr = err
		} else if cfg.PassthroughAPIKey && primary.Upstream == config.DefaultUpstreamName && cfg.ZenAPIKey == "" {
			fmt.Println("⚠️  Primary route probe skipped: passthrough requires a caller credential")
		} else if primary.Upstream == config.DefaultUpstreamName {
			primaryErr = checkOpenCodeCompatibleModel(ctx, client, primary)
		} else {
			primaryErr = client.CheckChatCompletion(ctx, primary.Name)
		}
	}
	if primaryErr != nil {
		fmt.Printf("❌ %s@%s: %v\n", primary.Name, primary.Upstream, primaryErr)
		os.Exit(1)
	}
	if !cfg.PassthroughAPIKey || primary.Upstream != config.DefaultUpstreamName || cfg.ZenAPIKey != "" {
		fmt.Printf("✅ %s@%s is usable\n", primary.Name, primary.Upstream)
	}

	// Account-scoped Codex catalog.
	if cfg.CodexOAuthToken != "" {
		codexClient := codex.NewClient(codex.CodexBackendURL, cfg.CodexOAuthToken, cfg.CodexAccountID, cfg.RequestTimeout)
		fmt.Println("\n=== Codex Models ===")
		discovered, err := codexClient.Models(ctx)
		if err != nil {
			fmt.Printf("❌ Catalog: %v\n", err)
			os.Exit(1)
		}
		for _, model := range discovered {
			fmt.Printf("  %s ✅\n", model.Slug)
		}
	}

	fmt.Println("\nAll checks passed!")
}

func cmdCodexLogin() {
	fmt.Println("=== Codex OAuth Login ===")
	fmt.Println()
	fmt.Println("Authenticate with your ChatGPT Plus/Pro subscription.")
	fmt.Println()

	pkce, err := codex.GeneratePKCE()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating PKCE: %v\n", err)
		os.Exit(1)
	}

	state, err := codex.GenerateState()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating state: %v\n", err)
		os.Exit(1)
	}

	authURL := codex.BuildAuthorizeURL(pkce, state)

	// Start local server to receive callback
	srv := codex.StartOAuthServer(state)
	defer srv.Close()

	// Try to open browser
	fmt.Println("Open this URL in your browser:")
	fmt.Printf("\n  %s\n\n", authURL)
	openBrowser(authURL)

	// Wait for callback: auto (server receives it) or manual (user pastes)
	fmt.Println("Waiting for authentication...")
	fmt.Println("(If browser didn't open, paste the redirect URL after logging in)")
	fmt.Println()

	code, err := srv.WaitForCode()
	if err != nil {
		// Auto-callback didn't arrive — wait for user to paste
		fmt.Print("Paste the redirect URL or code: ")
		var input string
		_, _ = fmt.Scanln(&input)
		input = strings.TrimSpace(input)

		code, err = codex.ParseRedirectURL(input, state)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Println("✓ Authorized")
	}

	fmt.Println("Exchanging code for tokens...")

	tokens, err := codex.ExchangeCode(code, pkce.Verifier)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error exchanging code: %v\n", err)
		os.Exit(1)
	}

	if err := codex.SaveTokens(tokens); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving tokens: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✓ Tokens saved successfully")
	fmt.Println()
	fmt.Printf("Account ID:  %s\n", tokens.AccountID)
	fmt.Printf("Config file: %s\n", codex.ConfigFilePath())
	fmt.Printf("Expires:     %s\n", time.UnixMilli(tokens.ExpiresAt).Format(time.RFC3339))
	fmt.Println()
	fmt.Println("You can now start the proxy with:")
	fmt.Println("  claude-proxy serve")
	fmt.Println()
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	_ = cmd.Start()
}

func cmdConfig(args []string) {
	configPath := codex.ConfigFilePath()

	// Check if config file exists, create default if not
	cfg := config.DefaultConfig()
	if data, err := os.ReadFile(configPath); err == nil {
		var fc config.FileConfig
		if err := json.Unmarshal(data, &fc); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing existing config: %v\n", err)
			return
		}
		// Apply file config to defaults without discarding fields the wizard
		// does not actively change.
		applyFileConfig(cfg, &fc)
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error reading config: %v\n", err)
		return
	}

	// If --show flag, just print current config and exit
	if len(args) > 0 && args[0] == "--show" {
		data, _ := json.MarshalIndent(cfg, "", "  ")
		fmt.Println(string(data))
		return
	}

	// If --export flag, export full config with tokens
	if len(args) > 0 && args[0] == "--export" {
		outPath := ""
		if len(args) > 1 {
			outPath = args[1]
		}
		exportFullConfig(cfg, outPath)
		return
	}

	// Interactive config wizard
	fmt.Println("╔═══════════════════════════════════════════════════╗")
	fmt.Println("║           claude-proxy configuration              ║")
	fmt.Println("╚═══════════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Println("Press Enter to keep current value, or type a new one.")
	fmt.Println()

	// Server and built-in OpenCode Zen upstream.
	fmt.Printf("── Server ────────────────────────────────────────\n")
	listenPort := promptString("Listen port", extractPort(cfg.ListenAddr))
	if listenPort != "" {
		cfg.ListenAddr = "0.0.0.0:" + listenPort
	}
	if key := promptSecret("OpenCode Zen API key", cfg.ZenAPIKey != ""); key != "" {
		cfg.ZenAPIKey = key
	}
	fmt.Println()

	// Models
	fmt.Printf("── Models ────────────────────────────────────────\n")
	curModels := cfg.EffectiveModels()
	if len(curModels) > 0 {
		cur := make([]string, len(curModels))
		for i, m := range curModels {
			cur[i] = m.Name + "@" + m.Upstream
		}
		fmt.Printf("  Current ordered models: %s\n", strings.Join(cur, ", "))
	}
	modelsDefault := ""
	if len(curModels) > 0 {
		cur := make([]string, len(curModels))
		for i, m := range curModels {
			cur[i] = m.Name + "@" + m.Upstream
		}
		modelsDefault = strings.Join(cur, ",")
	}
	modelsInput := promptString("Ordered model list (1st=default, format name@upstream, comma-separated)", modelsDefault)
	if modelsInput != "" {
		parsed, err := config.ParseModels(modelsInput)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Invalid models: %v\n", err)
		} else if len(parsed) > 0 {
			cfg.Models = parsed
		}
	}
	cfg.AllowUnlisted = promptBool("Allow unlisted models", cfg.AllowUnlisted)

	// Additional upstreams
	fmt.Printf("── Upstreams ─────────────────────────────────────\n")
	if len(cfg.Upstreams) > 0 {
		names := make([]string, len(cfg.Upstreams))
		for i, u := range cfg.Upstreams {
			names[i] = u.Name
		}
		fmt.Printf("  Current upstreams: %s\n", strings.Join(names, ", "))
	}
	addUpstream := promptBool("Add an additional upstream?", len(cfg.Upstreams) > 0)
	for addUpstream {
		name := promptString("  Upstream name", "")
		if name == "" {
			break
		}
		baseURL := promptString("  Base URL", "")
		apiKey := promptString("  API key", "")
		cfg.Upstreams = append(cfg.Upstreams, config.UpstreamConfig{
			Name:    name,
			BaseURL: baseURL,
			APIKey:  apiKey,
		})
		addUpstream = promptBool("Add another upstream?", false)
	}
	fmt.Println()

	// Codex
	fmt.Printf("── Codex (ChatGPT) ───────────────────────────────\n")
	tokens, _ := codex.LoadTokens()
	if tokens != nil {
		fmt.Printf("  Status: authenticated (account: %s)\n", tokens.AccountID)
		fmt.Printf("  Expires: %s\n", time.UnixMilli(tokens.ExpiresAt).Format(time.RFC3339))
		reLogin := promptBool("Re-login with ChatGPT?", false)
		if reLogin {
			cmdCodexLogin()
		}
	} else {
		fmt.Printf("  Status: not authenticated\n")
		doLogin := promptBool("Login with ChatGPT now?", false)
		if doLogin {
			cmdCodexLogin()
		}
	}
	fmt.Println()

	// Auth
	fmt.Printf("── Authentication ─────────────────────────────────\n")
	cfg.PassthroughAPIKey = promptBool("API key passthrough", cfg.PassthroughAPIKey)
	fmt.Println()

	// Logging
	fmt.Printf("── Logging ───────────────────────────────────────\n")
	cfg.LogLevel = promptString("Log level (debug/info/warn/error)", cfg.LogLevel)
	cfg.LogFormat = promptString("Log format (text/json)", cfg.LogFormat)
	fmt.Println()

	// Save
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating config dir: %v\n", err)
		os.Exit(1)
	}

	if issues := cfg.Validate(); len(issues) > 0 {
		for _, issue := range issues {
			fmt.Fprintf(os.Stderr, "Config error: %s\n", issue)
		}
		return
	}

	fileCfg := configToFileConfig(cfg)
	data, err := json.MarshalIndent(fileCfg, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling config: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(configPath, data, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Configuration saved to %s\n", configPath)
	fmt.Println()
	fmt.Println("Start the proxy with: claude-proxy serve")
	fmt.Println()
}

func promptString(label, current string) string {
	if current != "" {
		fmt.Printf("  %s [%s]: ", label, current)
	} else {
		fmt.Printf("  %s: ", label)
	}
	var input string
	_, _ = fmt.Scanln(&input)
	input = strings.TrimSpace(input)
	if input == "" {
		return current
	}
	return input
}

func promptSecret(label string, configured bool) string {
	if configured {
		fmt.Printf("  %s [(configured), Enter keeps it]: ", label)
	} else {
		fmt.Printf("  %s: ", label)
	}
	var input string
	_, _ = fmt.Scanln(&input)
	return strings.TrimSpace(input)
}

func extractPort(addr string) string {
	if parts := strings.Split(addr, ":"); len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

func promptBool(label string, current bool) bool {
	hint := "y/N"
	if current {
		hint = "Y/n"
	}
	fmt.Printf("  %s [%s]: ", label, hint)
	var input string
	_, _ = fmt.Scanln(&input)
	input = strings.TrimSpace(strings.ToLower(input))
	if input == "" {
		return current
	}
	return input == "y" || input == "yes"
}

func configToFileConfig(cfg *config.Config) *config.FileConfig {
	// Extract port from listen address (e.g. "127.0.0.1:3000" → "3000")
	port := ""
	if parts := strings.Split(cfg.ListenAddr, ":"); len(parts) > 0 {
		port = parts[len(parts)-1]
	}
	fc := &config.FileConfig{
		ListenPort:       port,
		Models:           cfg.Models,
		ReasoningModel:   cfg.ReasoningModel,
		CompletionModel:  cfg.CompletionModel,
		LogLevel:         cfg.LogLevel,
		LogFormat:        cfg.LogFormat,
		WebInterfacePort: cfg.WebInterfacePort,
	}
	// Only write zen_base_url if the user overrode the hardcoded default
	if cfg.ZenBaseURL != config.OpenCodeBaseURL {
		fc.ZenBaseURL = cfg.ZenBaseURL
	}
	if cfg.ZenAPIKey != "" {
		fc.ZenAPIKey = cfg.ZenAPIKey
	}
	if cfg.InboundAPIKey != "" {
		fc.InboundAPIKey = cfg.InboundAPIKey
	}
	if cfg.CodexOAuthToken != "" {
		fc.CodexOAuthToken = cfg.CodexOAuthToken
		fc.CodexAccountID = cfg.CodexAccountID
		fc.CodexRefreshToken = cfg.CodexRefreshToken
	}
	if cfg.WebInterfaceKey != "" {
		fc.WebInterfaceKey = cfg.WebInterfaceKey
	}
	if len(cfg.Upstreams) > 0 {
		fc.Upstreams = cfg.Upstreams
	}
	// `models` is the single ordered list (1st = default, rest = fallbacks);
	// legacy default_model/fallback_models are never written.
	maxBodySize := cfg.MaxBodySize
	fc.MaxBodySize = &maxBodySize
	fc.PassthroughKey = &cfg.PassthroughAPIKey
	fc.AllowUnlisted = &cfg.AllowUnlisted
	fc.ExposeAllModels = &cfg.ExposeAllModels
	fc.OnlyPreferredModels = &cfg.OnlyPreferredModels
	if cfg.RequestTimeout > 0 {
		fc.RequestTimeout = cfg.RequestTimeout.String()
	}
	if cfg.ModelCacheTTL > 0 {
		fc.ModelCacheTTL = cfg.ModelCacheTTL.String()
	}
	return fc
}

func applyFileConfig(cfg *config.Config, fc *config.FileConfig) {
	if fc.ListenAddr != "" {
		cfg.ListenAddr = fc.ListenAddr
	}
	if fc.ZenBaseURL != "" {
		cfg.ZenBaseURL = fc.ZenBaseURL
	}
	if fc.ZenAPIKey != "" {
		cfg.ZenAPIKey = fc.ZenAPIKey
	}
	if fc.InboundAPIKey != "" {
		cfg.InboundAPIKey = fc.InboundAPIKey
	}
	if fc.PassthroughKey != nil {
		cfg.PassthroughAPIKey = *fc.PassthroughKey
	}
	if fc.ReasoningModel != "" {
		cfg.ReasoningModel = fc.ReasoningModel
	}
	if fc.CompletionModel != "" {
		cfg.CompletionModel = fc.CompletionModel
	}
	if len(fc.Models) > 0 {
		cfg.Models = fc.Models
	}
	if len(fc.Upstreams) > 0 {
		cfg.Upstreams = fc.Upstreams
	}
	if fc.AllowUnlisted != nil {
		cfg.AllowUnlisted = *fc.AllowUnlisted
	}
	if fc.ExposeAllModels != nil {
		cfg.ExposeAllModels = *fc.ExposeAllModels
	}
	if fc.RequestTimeout != "" {
		if d, err := time.ParseDuration(fc.RequestTimeout); err == nil {
			cfg.RequestTimeout = d
		}
	}
	if fc.ModelCacheTTL != "" {
		if d, err := time.ParseDuration(fc.ModelCacheTTL); err == nil {
			cfg.ModelCacheTTL = d
		}
	}
	if fc.MaxBodySize != nil {
		cfg.MaxBodySize = *fc.MaxBodySize
	}
	if fc.CodexOAuthToken != "" {
		cfg.CodexOAuthToken = fc.CodexOAuthToken
	}
	if fc.CodexAccountID != "" {
		cfg.CodexAccountID = fc.CodexAccountID
	}
	if fc.CodexRefreshToken != "" {
		cfg.CodexRefreshToken = fc.CodexRefreshToken
	}
	if fc.LogLevel != "" {
		cfg.LogLevel = fc.LogLevel
	}
	if fc.LogFormat != "" {
		cfg.LogFormat = fc.LogFormat
	}
	if fc.WebInterfacePort != "" {
		cfg.WebInterfacePort = fc.WebInterfacePort
	}
	if fc.WebInterfaceKey != "" {
		cfg.WebInterfaceKey = fc.WebInterfaceKey
	}
}

func exportFullConfig(cfg *config.Config, outPath string) {
	maxBodySize := cfg.MaxBodySize
	exp := ExportConfig{
		ListenPort:          extractPort(cfg.ListenAddr),
		ZenBaseURL:          cfg.ZenBaseURL,
		ZenAPIKey:           cfg.ZenAPIKey,
		InboundAPIKey:       cfg.InboundAPIKey,
		PassthroughAPIKey:   cfg.PassthroughAPIKey,
		ReasoningModel:      cfg.ReasoningModel,
		CompletionModel:     cfg.CompletionModel,
		AllowUnlisted:       cfg.AllowUnlisted,
		ExposeAllModels:     cfg.ExposeAllModels,
		OnlyPreferredModels: cfg.OnlyPreferredModels,
		MaxBodySize:         &maxBodySize,
		RequestTimeout:      cfg.RequestTimeout.String(),
		WebInterfacePort:    cfg.WebInterfacePort,
		WebInterfaceKey:     cfg.WebInterfaceKey,
	}

	// `models` is the single ordered list (1st = default, rest = fallbacks).
	exp.Models = cfg.Models
	exp.Upstreams = cfg.Upstreams

	exp.ModelCacheTTL = cfg.ModelCacheTTL.String()
	exp.LogLevel = cfg.LogLevel
	exp.LogFormat = cfg.LogFormat

	// Include Codex tokens if available
	if tokens, err := codex.LoadTokens(); err == nil {
		exp.CodexOAuthToken = tokens.AccessToken
		exp.CodexRefreshToken = tokens.RefreshToken
		exp.CodexAccountID = tokens.AccountID
	}

	data, err := json.MarshalIndent(exp, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling config: %v\n", err)
		os.Exit(1)
	}

	if outPath != "" {
		if err := os.WriteFile(outPath, data, 0600); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing file: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Config exported to %s\n", outPath)
	} else {
		fmt.Println(string(data))
	}
}

// ExportConfig is the full configuration exported to JSON.
type ExportConfig struct {
	// Server
	ListenPort string `json:"listen_port"`

	// Upstream
	ZenBaseURL string `json:"zen_base_url,omitempty"`
	ZenAPIKey  string `json:"zen_api_key,omitempty"`

	// Auth
	InboundAPIKey     string `json:"inbound_api_key,omitempty"`
	PassthroughAPIKey bool   `json:"passthrough_api_key,omitempty"`

	// Models
	ReasoningModel      string                  `json:"reasoning_model,omitempty"`
	CompletionModel     string                  `json:"completion_model,omitempty"`
	Models              []config.ModelSpec      `json:"models,omitempty"`
	Upstreams           []config.UpstreamConfig `json:"upstreams,omitempty"`
	AllowUnlisted       bool                    `json:"allow_unlisted_models,omitempty"`
	ExposeAllModels     bool                    `json:"expose_all_models,omitempty"`
	OnlyPreferredModels bool                    `json:"only_preferred_models,omitempty"`

	// Body
	MaxBodySize *int64 `json:"max_body_size,omitempty"`

	// Timeouts
	RequestTimeout string `json:"request_timeout,omitempty"`
	ModelCacheTTL  string `json:"model_cache_ttl,omitempty"`

	// Logging
	LogLevel  string `json:"log_level,omitempty"`
	LogFormat string `json:"log_format,omitempty"`

	// Web interface
	WebInterfacePort string `json:"web_interface_port,omitempty"`
	WebInterfaceKey  string `json:"web_interface_key,omitempty"`

	// Codex
	CodexOAuthToken   string `json:"codex_oauth_token,omitempty"`
	CodexRefreshToken string `json:"codex_refresh_token,omitempty"`
	CodexAccountID    string `json:"codex_account_id,omitempty"`
}
