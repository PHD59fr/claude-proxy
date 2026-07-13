package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
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

	"github.com/claude-code-opencode/claude-proxy/internal/codex"
	"github.com/claude-code-opencode/claude-proxy/internal/config"
	"github.com/claude-code-opencode/claude-proxy/internal/log"
	"github.com/claude-code-opencode/claude-proxy/internal/models"
	"github.com/claude-code-opencode/claude-proxy/internal/proxy"
	"github.com/claude-code-opencode/claude-proxy/internal/upstream"
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
	return os.WriteFile(path, data, 0600)
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
	cfg, _ := config.Load(args)
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

func printBanner(cfg *config.Config, v string, modelStatuses []modelStatus) {
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

	// Check model availability and render the banner before normal runtime logs,
	// keeping startup output easy to scan. Server.Start refreshes the catalog in
	// the background after it has bound the listener.
	modelStatuses := checkModelsAtStartup(cfg.ResolvedConfig(), srv)
	printBanner(cfg, version, modelStatuses)

	srv.Setup()
	srv.StartTokenRefresh()
	srv.StartConfigWatcher()

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
			if next.UpstreamAPIKey == "" {
				next.UpstreamAPIKey = cfg.UpstreamAPIKey
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
				cfg.CodexOAuthToken = tokens.AccessToken
				cfg.CodexAccountID = tokens.AccountID
				if saveErr := persistConfig(cfg); saveErr != nil {
					webProvider.SetLoginError(saveErr.Error())
					return
				}
				webProvider.SetLoginComplete()
			}()

			return authURL, nil
		})
		webProvider.SetPersistConfig(func(c *config.Config) error {
			return persistConfig(c)
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

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start()
	}()

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
	add := func(name string, check func(context.Context) error) bool {
		if seen[name] {
			return true
		}
		seen[name] = true
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		err := check(ctx)
		cancel()
		statuses = append(statuses, modelStatus{name: name, ok: err == nil})
		srv.Handler().RecordModelProbe(name, err)
		return err == nil
	}

	// Check configured routes first, then the complete discovered free catalog
	// and known Codex catalog so the banner is a useful availability overview.
	for _, spec := range cfg.EffectiveModels() {
		if spec.Upstream == config.CodexUpstreamName {
			if cfg.CodexOAuthToken == "" {
				add(spec.Name, func(context.Context) error { return fmt.Errorf("codex is not authenticated") })
				continue
			}
			client := upstream.NewCodexClient(codex.CodexBackendURL, cfg.CodexOAuthToken, cfg.CodexAccountID, probeTimeout)
			add(spec.Name, func(ctx context.Context) error { return client.CheckModel(ctx, spec.Name) })
			continue
		}
		client, err := upstreamClientForSpec(cfg, spec)
		if err != nil {
			add(spec.Name, func(context.Context) error { return err })
			continue
		}
		add(spec.Name, func(ctx context.Context) error { return client.CheckChatCompletion(ctx, spec.Name) })
	}

	// OpenCode catalog models that are not explicitly in models[] are still
	// useful to display (notably newly available -free models).
	catalog := srv.Catalog().GetModels(true)
	freeClient := upstream.NewClient(cfg.UpstreamBaseURL, cfg.UpstreamAPIKey, probeTimeout)
	for _, model := range models.FilteredModels(catalog) {
		model := model
		add(model.ID, func(ctx context.Context) error { return freeClient.CheckChatCompletion(ctx, model.ID) })
	}

	// Always show the complete known Codex catalog. Unauthenticated Codex models
	// intentionally appear red rather than disappearing from the overview.
	codexClient := upstream.NewCodexClient(codex.CodexBackendURL, cfg.CodexOAuthToken, cfg.CodexAccountID, probeTimeout)
	for _, model := range []string{
		codex.ModelGPT56,
		codex.ModelGPT56Sol,
		codex.ModelGPT56Terra,
		codex.ModelGPT56Luna,
		codex.ModelGPT54,
		codex.ModelGPT54Mini,
	} {
		model := model
		if cfg.CodexOAuthToken == "" {
			add(model, func(context.Context) error { return fmt.Errorf("codex is not authenticated") })
			continue
		}
		add(model, func(ctx context.Context) error { return codexClient.CheckModel(ctx, model) })
	}

	return statuses
}

func upstreamClientForSpec(cfg *config.Config, spec config.ModelSpec) (*upstream.Client, error) {
	if spec.Upstream == config.DefaultUpstreamName {
		return upstream.NewClient(cfg.UpstreamBaseURL, cfg.UpstreamAPIKey, 10*time.Second), nil
	}
	for _, candidate := range cfg.Upstreams {
		if candidate.Name == spec.Upstream {
			return upstream.NewClient(candidate.BaseURL, candidate.APIKey, 10*time.Second), nil
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

	catalog := models.NewCatalog(cfg.UpstreamBaseURL, cfg.UpstreamAPIKey, cfg.ModelCacheTTL)
	upstreamModels := catalog.GetModels(true)

	// Codex models (ChatGPT subscription)
	if tokens, err := codex.LoadTokens(); err == nil {
		codexClient := upstream.NewCodexClient(codex.CodexBackendURL, tokens.AccessToken, tokens.AccountID, cfg.RequestTimeout)
		ctx := context.Background()

		// Check each model
		fmt.Println("Codex models (ChatGPT subscription):")
		for _, id := range []string{
			codex.ModelGPT56Sol,
			codex.ModelGPT56Terra,
			codex.ModelGPT56Luna,
			codex.ModelGPT54,
			codex.ModelGPT54Mini,
		} {
			suffix := ""
			if id == cfg.DefaultModel {
				suffix = " (default)"
			}
			if err := codexClient.CheckModel(ctx, id); err != nil {
				fmt.Printf("  %s ❌ %v%s\n", id, err, suffix)
			} else {
				fmt.Printf("  %s ✅%s\n", id, suffix)
			}
		}
		fmt.Println()
	}

	// Free models (bundled) — test each one
	freeModels := models.FilteredModels(upstreamModels)
	fmt.Println("Free models (included by default):")
	client := upstream.NewClient(cfg.UpstreamBaseURL, cfg.UpstreamAPIKey, cfg.RequestTimeout)
	ctx := context.Background()
	for _, m := range freeModels {
		suffix := ""
		if m.ID == cfg.DefaultModel {
			suffix = " (default)"
		}
		if err := client.CheckChatCompletion(ctx, m.ID); err != nil {
			fmt.Printf("  %s ❌ %v%s\n", m.ID, err, suffix)
		} else {
			fmt.Printf("  %s ✅%s\n", m.ID, suffix)
		}
	}

	fmt.Printf("\nTotal: %d free, %d upstream\n",
		len(freeModels), len(upstreamModels))
}

func cmdCheck(args []string) {
	cfg, err := config.Load(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== Configuration ===")
	fmt.Printf("Listen:      %s\n", cfg.ListenAddr)
	fmt.Printf("Upstream:    %s\n", cfg.UpstreamBaseURL)
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
	client := upstream.NewClient(cfg.UpstreamBaseURL, cfg.UpstreamAPIKey, cfg.RequestTimeout)

	fmt.Println("\n=== Upstream Connectivity ===")
	if err := client.Check(ctx); err != nil {
		fmt.Printf("❌ Models endpoint: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ Models endpoint: OK")

	if err := client.CheckChatCompletion(ctx, cfg.DefaultModel); err != nil {
		fmt.Printf("❌ Chat completions endpoint: %v\n", err)
	} else {
		fmt.Println("✅ Chat completions endpoint: OK")
	}

	fmt.Println("\n=== Model Validation ===")
	catalog := models.NewCatalog(cfg.UpstreamBaseURL, cfg.UpstreamAPIKey, cfg.ModelCacheTTL)
	upstreamModels := catalog.GetModels(true)

	defaultFound := false
	for _, m := range upstreamModels {
		if m.ID == cfg.DefaultModel {
			defaultFound = true
			break
		}
	}
	if defaultFound {
		fmt.Printf("✅ Default model '%s' found upstream\n", cfg.DefaultModel)
	} else {
		fmt.Printf("❌ Default model '%s' NOT found upstream\n", cfg.DefaultModel)
		os.Exit(1)
	}

	// Codex models
	if cfg.CodexOAuthToken != "" {
		codexClient := upstream.NewCodexClient(codex.CodexBackendURL, cfg.CodexOAuthToken, cfg.CodexAccountID, cfg.RequestTimeout)
		fmt.Println("\n=== Codex Models ===")
		for _, id := range []string{
			codex.ModelGPT56Sol,
			codex.ModelGPT56Terra,
			codex.ModelGPT56Luna,
			codex.ModelGPT54,
			codex.ModelGPT54Mini,
		} {
			suffix := ""
			if id == cfg.DefaultModel {
				suffix = " (default)"
			}
			if err := codexClient.CheckModel(ctx, id); err != nil {
				fmt.Printf("  %s ❌ %v%s\n", id, err, suffix)
			} else {
				fmt.Printf("  %s ✅%s\n", id, suffix)
			}
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

	// Server (OpenCode is the hardcoded default upstream — no need to repoint it)
	fmt.Printf("── Server ────────────────────────────────────────\n")
	listenPort := promptString("Listen port", extractPort(cfg.ListenAddr))
	if listenPort != "" {
		cfg.ListenAddr = "0.0.0.0:" + listenPort
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
	// Only write upstream_base_url if the user overrode the hardcoded default
	if cfg.UpstreamBaseURL != config.OpenCodeBaseURL {
		fc.UpstreamBaseURL = cfg.UpstreamBaseURL
	}
	if cfg.UpstreamAPIKey != "" && cfg.UpstreamAPIKey != "public" {
		fc.UpstreamAPIKey = cfg.UpstreamAPIKey
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
	if fc.UpstreamBaseURL != "" {
		cfg.UpstreamBaseURL = fc.UpstreamBaseURL
	}
	if fc.UpstreamAPIKey != "" {
		cfg.UpstreamAPIKey = fc.UpstreamAPIKey
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
	exp := ExportConfig{
		ListenPort:        extractPort(cfg.ListenAddr),
		UpstreamBaseURL:   cfg.UpstreamBaseURL,
		UpstreamAPIKey:    cfg.UpstreamAPIKey,
		PassthroughAPIKey: cfg.PassthroughAPIKey,
		ReasoningModel:    cfg.ReasoningModel,
		CompletionModel:   cfg.CompletionModel,
		AllowUnlisted:     cfg.AllowUnlisted,
		ExposeAllModels:   cfg.ExposeAllModels,
		RequestTimeout:    cfg.RequestTimeout.String(),
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
	UpstreamBaseURL string `json:"upstream_base_url,omitempty"`
	UpstreamAPIKey  string `json:"upstream_api_key,omitempty"`

	// Auth
	PassthroughAPIKey bool `json:"passthrough_api_key,omitempty"`

	// Models
	ReasoningModel  string                  `json:"reasoning_model,omitempty"`
	CompletionModel string                  `json:"completion_model,omitempty"`
	Models          []config.ModelSpec      `json:"models,omitempty"`
	Upstreams       []config.UpstreamConfig `json:"upstreams,omitempty"`
	AllowUnlisted   bool                    `json:"allow_unlisted_models,omitempty"`
	ExposeAllModels bool                    `json:"expose_all_models,omitempty"`

	// Timeouts
	RequestTimeout string `json:"request_timeout,omitempty"`
	ModelCacheTTL  string `json:"model_cache_ttl,omitempty"`

	// Logging
	LogLevel  string `json:"log_level,omitempty"`
	LogFormat string `json:"log_format,omitempty"`

	// Codex
	CodexOAuthToken   string `json:"codex_oauth_token,omitempty"`
	CodexRefreshToken string `json:"codex_refresh_token,omitempty"`
	CodexAccountID    string `json:"codex_account_id,omitempty"`
}
