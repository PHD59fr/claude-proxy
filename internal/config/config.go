package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// OpenCodeBaseURL is the default upstream: OpenCode's free model endpoint.
// Models there are free and can be extended with others, so it is the default.
// The upstream remains overridable (flag/env/file) for OpenRouter, OpenAI, etc.
const OpenCodeBaseURL = "https://opencode.ai/zen/v1"

// DefaultUpstreamName is the canonical name for the built-in OpenCode upstream.
const DefaultUpstreamName = "opencode"

// CodexUpstreamName is the canonical name for the ChatGPT Codex backend.
const CodexUpstreamName = "codex"

// UpstreamConfig describes an additional OpenAI-compatible upstream endpoint.
type UpstreamConfig struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

// ModelSpec is a single entry in the ordered model preference list.
// Name is the upstream model name; Upstream is the upstream that serves it
// ("opencode" for the built-in default, "codex" for the ChatGPT backend,
// or the Name of an entry in Upstreams).
type ModelSpec struct {
	Name     string `json:"name"`
	Upstream string `json:"upstream"`
}

// ModelSpecs is a []ModelSpec that also accepts a legacy JSON form where the
// models are a plain array of strings (e.g. ["big-pickle","gpt-5.6-terra"]).
// In that case each string is treated as ModelSpec{Name: s, Upstream: "opencode"}.
type ModelSpecs []ModelSpec

// UnmarshalJSON supports both [{"name":..,"upstream":..}, ...] and ["name", ...].
func (m *ModelSpecs) UnmarshalJSON(data []byte) error {
	// Try the legacy []string form first.
	var strs []string
	if err := json.Unmarshal(data, &strs); err == nil {
		out := make(ModelSpecs, 0, len(strs))
		for _, s := range strs {
			out = append(out, ModelSpec{Name: strings.TrimSpace(s), Upstream: DefaultUpstreamName})
		}
		*m = out
		return nil
	}
	// Try the canonical []ModelSpec form.
	var specs []ModelSpec
	if err := json.Unmarshal(data, &specs); err != nil {
		return fmt.Errorf("models must be an array of strings or objects: %w", err)
	}
	*m = specs
	return nil
}

type Config struct {
	// Server
	ListenAddr string
	ConfigFile string // Path to config file (--config flag)

	// Upstream
	UpstreamBaseURL string
	UpstreamAPIKey  string
	Upstreams       []UpstreamConfig // Additional OpenAI-compatible upstreams

	// Auth
	InboundAPIKey     string
	PassthroughAPIKey bool

	// Models
	DefaultModel    string // Derived in Precompute from Models[0].Name (1st = default).
	ReasoningModel  string
	CompletionModel string
	Models          []ModelSpec // Single ordered preference list. 1st = default, rest = ordered fallbacks.
	AllowUnlisted   bool
	ExposeAllModels bool

	// Timeouts
	RequestTimeout time.Duration
	ModelCacheTTL  time.Duration

	// Body
	MaxBodySize int64

	// Logging
	LogLevel  string
	LogFormat string
	Verbose   bool

	// Codex backend (ChatGPT subscription)
	CodexOAuthToken   string
	CodexAccountID    string
	CodexRefreshToken string

	// Web interface (empty = disabled)
	WebInterfacePort string
	WebInterfaceKey  string

	// Precomputed
	PrecomputedFallbacks []ModelSpec     // Precomputed fallback model list (set by Precompute)
	allowedModels        map[string]bool // Precomputed set of allowed models when AllowUnlisted is false

	// Source tracking for the web UI (maps field name to source description)
	Sources map[string]string
}

func DefaultConfig() *Config {
	cfg := &Config{
		ListenAddr:      "127.0.0.1:3000",
		UpstreamBaseURL: OpenCodeBaseURL,
		UpstreamAPIKey:  "public",
		InboundAPIKey:   "",
		AllowUnlisted:   false,
		ExposeAllModels: false,
		RequestTimeout:  300 * time.Second,
		ModelCacheTTL:   5 * time.Minute,
		MaxBodySize:     10 * 1024 * 1024, // 10MB
		LogLevel:        "info",
		LogFormat:       "text",

		CodexOAuthToken:   "",
		CodexAccountID:    "",
		CodexRefreshToken: "",
		Sources:           make(map[string]string),
	}
	// `Models` is the single ordered preference list (1st = default, the
	// rest are fallbacks in priority order). DefaultModel is derived from
	// Models[0] in Precompute(); there is no separate fallback field.
	cfg.Models = []ModelSpec{
		{Name: "big-pickle", Upstream: DefaultUpstreamName},
		{Name: "deepseek-v4-flash-free", Upstream: DefaultUpstreamName},
		{Name: "hy3-free", Upstream: DefaultUpstreamName},
		{Name: "mimo-v2.5-free", Upstream: DefaultUpstreamName},
		{Name: "nemotron-3-ultra-free", Upstream: DefaultUpstreamName},
		{Name: "north-mini-code-free", Upstream: DefaultUpstreamName},
	}
	cfg.Precompute()
	return cfg
}

type FileConfig struct {
	ListenPort      string `json:"listen_port"`
	ListenAddr      string `json:"listen_addr,omitempty"`
	UpstreamBaseURL string `json:"upstream_base_url"`
	UpstreamAPIKey  string `json:"upstream_api_key"`
	InboundAPIKey   string `json:"inbound_api_key"`
	PassthroughKey  *bool  `json:"passthrough_api_key"`
	// Deprecated, read-only for migration of legacy config files. The
	// ordered `models` list below is the single source of truth: its first
	// entry is the default and the rest are fallbacks in priority order.
	DefaultModel      string           `json:"default_model,omitempty"`
	ReasoningModel    string           `json:"reasoning_model"`
	CompletionModel   string           `json:"completion_model"`
	FallbackModels    []string         `json:"fallback_models,omitempty"`
	Models            ModelSpecs       `json:"models"`
	Upstreams         []UpstreamConfig `json:"upstreams"`
	AllowUnlisted     *bool            `json:"allow_unlisted_models"`
	ExposeAllModels   *bool            `json:"expose_all_models"`
	RequestTimeout    string           `json:"request_timeout"`
	ModelCacheTTL     string           `json:"model_cache_ttl"`
	MaxBodySize       *int64           `json:"max_body_size"`
	LogLevel          string           `json:"log_level"`
	LogFormat         string           `json:"log_format"`
	CodexOAuthToken   string           `json:"codex_oauth_token"`
	CodexAccountID    string           `json:"codex_account_id"`
	CodexRefreshToken string           `json:"codex_refresh_token"`
	WebInterfacePort  string           `json:"web_interface_port"`
	WebInterfaceKey   string           `json:"web_interface_key"`
}

func Load(args []string) (*Config, error) {
	cfg := DefaultConfig()

	// Flags
	fs := flag.NewFlagSet("claude-proxy", flag.ContinueOnError)
	var (
		configFile      string
		listenAddr      string
		upstreamURL     string
		upstreamKey     string
		inboundKey      string
		passthroughKey  bool
		reasoningModel  string
		completionModel string
		modelsFlag      string
		upstreamsFlag   string
		allowUnlisted   bool
		exposeAll       bool
		requestTO       string
		modelCacheTTL   string
		logLevel        string
		logFormat       string
		debug           bool
		verbose         bool
		codexToken      string
		codexAccountID  string
		webPort         string
	)

	fs.StringVar(&configFile, "config", "", "path to config file (JSON)")
	fs.StringVar(&listenAddr, "listen", "", "listen address")
	fs.StringVar(&upstreamURL, "upstream", "", "upstream base URL")
	fs.StringVar(&upstreamKey, "upstream-key", "", "upstream API key")
	fs.StringVar(&inboundKey, "inbound-key", "", "inbound API key for auth")
	fs.BoolVar(&passthroughKey, "passthrough-key", false, "forward inbound API key to upstream")
	fs.StringVar(&reasoningModel, "reasoning-model", "", "model for extended thinking requests")
	fs.StringVar(&completionModel, "completion-model", "", "model for standard completion requests")
	fs.StringVar(&modelsFlag, "models", "", "ordered model preference list: 'name@upstream' entries or JSON (object/array). 1st = default, rest = ordered fallbacks.")
	fs.StringVar(&upstreamsFlag, "upstreams", "", "JSON array of upstreams: [{\"name\":\"custom\",\"base_url\":\"https://host/v1\",\"api_key\":\"sk-...\"}]")
	fs.BoolVar(&allowUnlisted, "allow-unlisted", false, "allow unlisted models")
	fs.BoolVar(&exposeAll, "expose-all", false, "expose all upstream models")
	fs.StringVar(&requestTO, "request-timeout", "", "request timeout")
	fs.StringVar(&modelCacheTTL, "model-cache-ttl", "", "model cache TTL")
	fs.StringVar(&logLevel, "log-level", "", "log level")
	fs.StringVar(&logFormat, "log-format", "", "log format")
	fs.BoolVar(&debug, "debug", false, "enable debug logging")
	fs.BoolVar(&verbose, "verbose", false, "enable verbose logging (full request/response bodies)")
	fs.StringVar(&codexToken, "codex-token", "", "OAuth token for Codex backend")
	fs.StringVar(&codexAccountID, "codex-account-id", "", "ChatGPT account ID for Codex")
	fs.StringVar(&webPort, "web-port", "", "web interface port (empty = disabled)")

	// Parse flags from remaining args (skip subcommand)
	subArgs := args
	if len(args) > 0 && args[0] == "serve" {
		subArgs = args[1:]
	}
	if err := fs.Parse(subArgs); err != nil {
		return nil, fmt.Errorf("parse flags: %w", err)
	}

	// Collect which flags were explicitly set
	changedFlags := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		changedFlags[f.Name] = true
	})

	// 1. Config file (--config flag or auto-detect)
	if configFile == "" {
		// Auto-detect: check for config.json in current directory
		if _, err := os.Stat("config.json"); err == nil {
			configFile = "config.json"
		}
	}
	if configFile != "" {
		if err := loadFile(configFile, cfg); err != nil {
			return nil, err
		}
		cfg.ConfigFile = configFile
	}

	// 2. Environment variables
	loadEnv(cfg)

	// 3. Flags (highest precedence)
	if listenAddr != "" {
		cfg.ListenAddr = listenAddr
		cfg.setSource("listen_addr", "--listen")
	}
	if upstreamURL != "" {
		cfg.UpstreamBaseURL = upstreamURL
		cfg.setSource("upstream_base_url", "--upstream")
	}
	if upstreamKey != "" {
		cfg.UpstreamAPIKey = upstreamKey
		cfg.setSource("upstream_api_key", "--upstream-key")
	}
	if inboundKey != "" {
		cfg.InboundAPIKey = inboundKey
		cfg.setSource("inbound_api_key", "--inbound-key")
	}
	if changedFlags["passthrough-key"] {
		cfg.PassthroughAPIKey = passthroughKey
		cfg.setSource("passthrough_api_key", "--passthrough-key")
	}
	if reasoningModel != "" {
		cfg.ReasoningModel = reasoningModel
		cfg.setSource("reasoning_model", "--reasoning-model")
	}
	if completionModel != "" {
		cfg.CompletionModel = completionModel
		cfg.setSource("completion_model", "--completion-model")
	}
	if modelsFlag != "" {
		parsed, err := ParseModels(modelsFlag)
		if err != nil {
			return nil, fmt.Errorf("parse --models: %w", err)
		}
		cfg.Models = parsed
		cfg.setSource("models", "--models")
	}
	if upstreamsFlag != "" {
		parsed, err := ParseUpstreams(upstreamsFlag)
		if err != nil {
			return nil, fmt.Errorf("parse --upstreams: %w", err)
		}
		cfg.Upstreams = parsed
		cfg.setSource("upstreams", "--upstreams")
	}
	if changedFlags["allow-unlisted"] {
		cfg.AllowUnlisted = allowUnlisted
		cfg.setSource("allow_unlisted_models", "--allow-unlisted")
	}
	if changedFlags["expose-all"] {
		cfg.ExposeAllModels = exposeAll
		cfg.setSource("expose_all_models", "--expose-all")
	}
	if requestTO != "" {
		d, err := time.ParseDuration(requestTO)
		if err != nil {
			return nil, fmt.Errorf("invalid request-timeout: %w", err)
		}
		cfg.RequestTimeout = d
		cfg.setSource("request_timeout", "--request-timeout")
	}
	if modelCacheTTL != "" {
		d, err := time.ParseDuration(modelCacheTTL)
		if err != nil {
			return nil, fmt.Errorf("invalid model-cache-ttl: %w", err)
		}
		cfg.ModelCacheTTL = d
		cfg.setSource("model_cache_ttl", "--model-cache-ttl")
	}
	if logLevel != "" {
		cfg.LogLevel = logLevel
		cfg.setSource("log_level", "--log-level")
	}
	if logFormat != "" {
		cfg.LogFormat = logFormat
		cfg.setSource("log_format", "--log-format")
	}
	if debug {
		cfg.LogLevel = "debug"
		cfg.setSource("log_level", "--debug")
	}
	if verbose {
		cfg.LogLevel = "debug"
		cfg.Verbose = true
		cfg.setSource("log_level", "--verbose")
	}
	if codexToken != "" {
		cfg.CodexOAuthToken = codexToken
		cfg.setSource("codex_oauth_token", "--codex-token")
	}
	if codexAccountID != "" {
		cfg.CodexAccountID = codexAccountID
		cfg.setSource("codex_account_id", "--codex-account-id")
	}
	if webPort != "" {
		cfg.WebInterfacePort = webPort
		cfg.setSource("web_interface_port", "--web-port")
	}

	cfg.TrimSpace()
	cfg.Precompute()
	return cfg, nil
}

func loadFile(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}
	var fc FileConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		return fmt.Errorf("parse config file: %w", err)
	}
	if fc.ListenPort != "" {
		cfg.ListenAddr = "0.0.0.0:" + fc.ListenPort
		cfg.setSource("listen_addr", path)
	} else if fc.ListenAddr != "" {
		cfg.ListenAddr = fc.ListenAddr
		cfg.setSource("listen_addr", path)
	}
	if fc.UpstreamBaseURL != "" {
		cfg.UpstreamBaseURL = fc.UpstreamBaseURL
		cfg.setSource("upstream_base_url", path)
	}
	if fc.UpstreamAPIKey != "" {
		cfg.UpstreamAPIKey = fc.UpstreamAPIKey
		cfg.setSource("upstream_api_key", path)
	}
	if fc.InboundAPIKey != "" {
		cfg.InboundAPIKey = fc.InboundAPIKey
		cfg.setSource("inbound_api_key", path)
	}
	if fc.PassthroughKey != nil {
		cfg.PassthroughAPIKey = *fc.PassthroughKey
		cfg.setSource("passthrough_api_key", path)
	}
	if fc.ReasoningModel != "" {
		cfg.ReasoningModel = fc.ReasoningModel
		cfg.setSource("reasoning_model", path)
	}
	if fc.CompletionModel != "" {
		cfg.CompletionModel = fc.CompletionModel
		cfg.setSource("completion_model", path)
	}
	if len(fc.Models) > 0 {
		cfg.Models = fc.Models
		cfg.setSource("models", path)
	} else if fc.DefaultModel != "" || len(fc.FallbackModels) > 0 {
		// Migration: fold legacy default_model + fallback_models into the
		// unified ordered `models` list (1st = default, rest = fallbacks).
		var migrated []ModelSpec
		if fc.DefaultModel != "" {
			migrated = append(migrated, ModelSpec{Name: fc.DefaultModel, Upstream: DefaultUpstreamName})
		}
		for _, f := range fc.FallbackModels {
			migrated = append(migrated, ModelSpec{Name: f, Upstream: DefaultUpstreamName})
		}
		cfg.Models = migrated
		cfg.setSource("models", path)
	}
	if len(fc.Upstreams) > 0 {
		cfg.Upstreams = fc.Upstreams
		cfg.setSource("upstreams", path)
	}
	if fc.AllowUnlisted != nil {
		cfg.AllowUnlisted = *fc.AllowUnlisted
		cfg.setSource("allow_unlisted_models", path)
	}
	if fc.ExposeAllModels != nil {
		cfg.ExposeAllModels = *fc.ExposeAllModels
		cfg.setSource("expose_all_models", path)
	}
	if fc.RequestTimeout != "" {
		d, err := time.ParseDuration(fc.RequestTimeout)
		if err != nil {
			return fmt.Errorf("invalid request_timeout %q: %w", fc.RequestTimeout, err)
		}
		cfg.RequestTimeout = d
		cfg.setSource("request_timeout", path)
	}
	if fc.ModelCacheTTL != "" {
		d, err := time.ParseDuration(fc.ModelCacheTTL)
		if err != nil {
			return fmt.Errorf("invalid model_cache_ttl %q: %w", fc.ModelCacheTTL, err)
		}
		cfg.ModelCacheTTL = d
		cfg.setSource("model_cache_ttl", path)
	}
	if fc.MaxBodySize != nil {
		cfg.MaxBodySize = *fc.MaxBodySize
		cfg.setSource("max_body_size", path)
	}
	if fc.LogLevel != "" {
		cfg.LogLevel = fc.LogLevel
		cfg.setSource("log_level", path)
	}
	if fc.LogFormat != "" {
		cfg.LogFormat = fc.LogFormat
		cfg.setSource("log_format", path)
	}
	if fc.CodexOAuthToken != "" {
		cfg.CodexOAuthToken = fc.CodexOAuthToken
		cfg.setSource("codex_oauth_token", path)
	}
	if fc.CodexAccountID != "" {
		cfg.CodexAccountID = fc.CodexAccountID
		cfg.setSource("codex_account_id", path)
	}
	if fc.CodexRefreshToken != "" {
		cfg.CodexRefreshToken = fc.CodexRefreshToken
		cfg.setSource("codex_refresh_token", path)
	}
	if fc.WebInterfacePort != "" {
		cfg.WebInterfacePort = fc.WebInterfacePort
		cfg.setSource("web_interface_port", path)
	}
	if fc.WebInterfaceKey != "" {
		cfg.WebInterfaceKey = fc.WebInterfaceKey
		cfg.setSource("web_interface_key", path)
	}
	return nil
}

// ParseModels parses a --models / MODELS value. It accepts:
//   - JSON: an array of objects [{"name","upstream"}] or an array of strings ["a","b"]
//   - a comma-separated list of "name@upstream" entries (bare "name" => upstream "opencode")
func ParseModels(s string) ([]ModelSpec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	// JSON form.
	if strings.HasPrefix(s, "[") {
		var ms ModelSpecs
		if err := json.Unmarshal([]byte(s), &ms); err != nil {
			return nil, fmt.Errorf("invalid JSON models: %w", err)
		}
		return ms, nil
	}
	// Comma-separated "name@upstream" form.
	parts := strings.Split(s, ",")
	out := make([]ModelSpec, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if at := strings.LastIndex(p, "@"); at > 0 {
			out = append(out, ModelSpec{
				Name:     strings.TrimSpace(p[:at]),
				Upstream: strings.TrimSpace(p[at+1:]),
			})
		} else {
			out = append(out, ModelSpec{Name: p, Upstream: DefaultUpstreamName})
		}
	}
	return out, nil
}

// ParseUpstreams parses a --upstreams / UPSTREAMS JSON value:
// [{"name","base_url","api_key"}, ...].
func ParseUpstreams(s string) ([]UpstreamConfig, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if !strings.HasPrefix(s, "[") {
		return nil, fmt.Errorf("upstreams must be a JSON array")
	}
	var ups []UpstreamConfig
	if err := json.Unmarshal([]byte(s), &ups); err != nil {
		return nil, fmt.Errorf("invalid JSON upstreams: %w", err)
	}
	return ups, nil
}

func loadEnv(cfg *Config) {
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
		cfg.setSource("listen_addr", "LISTEN_ADDR")
	}
	if v := os.Getenv("UPSTREAM_BASE_URL"); v != "" {
		cfg.UpstreamBaseURL = v
		cfg.setSource("upstream_base_url", "UPSTREAM_BASE_URL")
	}
	if v := os.Getenv("UPSTREAM_API_KEY"); v != "" {
		cfg.UpstreamAPIKey = v
		cfg.setSource("upstream_api_key", "UPSTREAM_API_KEY")
	}
	if v := os.Getenv("INBOUND_API_KEY"); v != "" {
		cfg.InboundAPIKey = v
		cfg.setSource("inbound_api_key", "INBOUND_API_KEY")
	}
	if v := os.Getenv("UPSTREAM_API_KEY_PASSTHROUGH"); v != "" {
		cfg.PassthroughAPIKey = parseBool(v)
		cfg.setSource("passthrough_api_key", "UPSTREAM_API_KEY_PASSTHROUGH")
	}
	if v := os.Getenv("DEFAULT_MODEL"); v != "" {
		cfg.DefaultModel = v
		cfg.setSource("default_model", "DEFAULT_MODEL")
	}
	if v := os.Getenv("REASONING_MODEL"); v != "" {
		cfg.ReasoningModel = v
		cfg.setSource("reasoning_model", "REASONING_MODEL")
	}
	if v := os.Getenv("COMPLETION_MODEL"); v != "" {
		cfg.CompletionModel = v
		cfg.setSource("completion_model", "COMPLETION_MODEL")
	}
	if v := os.Getenv("MODELS"); v != "" {
		parsed, err := ParseModels(v)
		if err == nil {
			cfg.Models = parsed
			cfg.setSource("models", "MODELS")
		}
	}
	if v := os.Getenv("UPSTREAMS"); v != "" {
		parsed, err := ParseUpstreams(v)
		if err == nil {
			cfg.Upstreams = parsed
			cfg.setSource("upstreams", "UPSTREAMS")
		}
	}
	if v := os.Getenv("ALLOW_UNLISTED_MODELS"); v != "" {
		cfg.AllowUnlisted = parseBool(v)
		cfg.setSource("allow_unlisted_models", "ALLOW_UNLISTED_MODELS")
	}
	if v := os.Getenv("EXPOSE_ALL_MODELS"); v != "" {
		cfg.ExposeAllModels = parseBool(v)
		cfg.setSource("expose_all_models", "EXPOSE_ALL_MODELS")
	}
	if v := os.Getenv("REQUEST_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.RequestTimeout = d
			cfg.setSource("request_timeout", "REQUEST_TIMEOUT")
		}
	}
	if v := os.Getenv("MODEL_CACHE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.ModelCacheTTL = d
			cfg.setSource("model_cache_ttl", "MODEL_CACHE_TTL")
		}
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
		cfg.setSource("log_level", "LOG_LEVEL")
	}
	if v := os.Getenv("LOG_FORMAT"); v != "" {
		cfg.LogFormat = v
		cfg.setSource("log_format", "LOG_FORMAT")
	}
	if v := os.Getenv("DEBUG"); v != "" {
		if parseBool(v) {
			cfg.LogLevel = "debug"
			cfg.setSource("log_level", "DEBUG")
		}
	}
	if v := os.Getenv("VERBOSE"); v != "" {
		if parseBool(v) {
			cfg.LogLevel = "debug"
			cfg.Verbose = true
			cfg.setSource("log_level", "VERBOSE")
		}
	}
	if v := os.Getenv("CODEX_OAUTH_TOKEN"); v != "" {
		cfg.CodexOAuthToken = v
		cfg.setSource("codex_oauth_token", "CODEX_OAUTH_TOKEN")
	}
	if v := os.Getenv("CODEX_ACCOUNT_ID"); v != "" {
		cfg.CodexAccountID = v
		cfg.setSource("codex_account_id", "CODEX_ACCOUNT_ID")
	}
	if v := os.Getenv("WEBINTERFACE"); v != "" {
		cfg.WebInterfacePort = strings.TrimSpace(v)
		cfg.setSource("web_interface_port", "WEBINTERFACE")
	}
	if v := os.Getenv("WEBINTERFACE_KEY"); v != "" {
		cfg.WebInterfaceKey = v
		cfg.setSource("web_interface_key", "WEBINTERFACE_KEY")
	}
}

func (c *Config) setSource(field, source string) {
	if c.Sources != nil {
		c.Sources[field] = source
	}
}

func parseBool(s string) bool {
	b, _ := strconv.ParseBool(s)
	return b
}

func (c *Config) TrimSpace() {
	c.ListenAddr = strings.TrimSpace(c.ListenAddr)
	c.UpstreamBaseURL = strings.TrimSpace(c.UpstreamBaseURL)
	c.UpstreamAPIKey = strings.TrimSpace(c.UpstreamAPIKey)
	c.InboundAPIKey = strings.TrimSpace(c.InboundAPIKey)
	c.LogLevel = strings.TrimSpace(c.LogLevel)
	c.LogFormat = strings.TrimSpace(c.LogFormat)
	c.WebInterfacePort = strings.TrimSpace(c.WebInterfacePort)
	c.WebInterfaceKey = strings.TrimSpace(c.WebInterfaceKey)
}
