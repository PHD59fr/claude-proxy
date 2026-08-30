package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.ListenAddr != "127.0.0.1:3000" {
		t.Errorf("listen_addr = %q, want 127.0.0.1:3000", cfg.ListenAddr)
	}
	if cfg.ZenBaseURL != "https://opencode.ai/zen/v1" {
		t.Errorf("zen_base_url = %q", cfg.ZenBaseURL)
	}
	if cfg.ZenAPIKey != "" {
		t.Errorf("zen_api_key = %q, want discovery mode", cfg.ZenAPIKey)
	}
	if cfg.DefaultModel != "" {
		t.Errorf("default_model = %q, want discovery mode", cfg.DefaultModel)
	}
	if cfg.RequestTimeout != 300*time.Second {
		t.Errorf("request_timeout = %v", cfg.RequestTimeout)
	}
}

func TestLoad_EnvironmentVariables(t *testing.T) {
	t.Setenv("LISTEN_ADDR", "0.0.0.0:8080")
	t.Setenv("ZEN_BASE_URL", "https://custom.api/v1")
	t.Setenv("ZEN_API_KEY", "custom-key")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load([]string{"serve"})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.ListenAddr != "0.0.0.0:8080" {
		t.Errorf("listen_addr = %q, want 0.0.0.0:8080", cfg.ListenAddr)
	}
	if cfg.ZenBaseURL != "https://custom.api/v1" {
		t.Errorf("zen_base_url = %q", cfg.ZenBaseURL)
	}
	if cfg.ZenAPIKey != "custom-key" {
		t.Errorf("zen_api_key = %q", cfg.ZenAPIKey)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("log_level = %q", cfg.LogLevel)
	}
}

func TestLoad_ZenBaseURLLegacyEnvAlias(t *testing.T) {
	t.Setenv("UPSTREAM_BASE_URL", "https://legacy.api/v1")
	t.Setenv("ZEN_BASE_URL", "")
	cfg, err := Load([]string{"serve"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ZenBaseURL != "https://legacy.api/v1" {
		t.Errorf("zen_base_url via deprecated UPSTREAM_BASE_URL = %q, want https://legacy.api/v1", cfg.ZenBaseURL)
	}
}

func TestLoad_ZenAPIKeyLegacyEnvAlias(t *testing.T) {
	t.Setenv("UPSTREAM_API_KEY", "legacy-key")
	t.Setenv("ZEN_API_KEY", "")
	cfg, err := Load([]string{"serve"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ZenAPIKey != "legacy-key" {
		t.Errorf("zen_api_key via deprecated UPSTREAM_API_KEY = %q, want legacy-key", cfg.ZenAPIKey)
	}
}

func TestLoad_Flags(t *testing.T) {
	args := []string{"serve", "--listen", "0.0.0.0:9090", "--zen-base-url", "https://flag.api/v1", "--models", "flag-model"}

	cfg, err := Load(args)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.ListenAddr != "0.0.0.0:9090" {
		t.Errorf("listen_addr = %q, want 0.0.0.0:9090", cfg.ListenAddr)
	}
	if cfg.ZenBaseURL != "https://flag.api/v1" {
		t.Errorf("zen_base_url = %q", cfg.ZenBaseURL)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].Name != "flag-model" {
		t.Errorf("models = %v, want [flag-model]", cfg.Models)
	}
	if cfg.DefaultModel != "flag-model" {
		t.Errorf("default_model = %q, want flag-model (derived from models[0])", cfg.DefaultModel)
	}
}

func TestLoad_FlagZenBaseURLAliases(t *testing.T) {
	cfg, err := Load([]string{"serve", "--upstream", "https://legacy.api/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ZenBaseURL != "https://legacy.api/v1" {
		t.Errorf("--upstream alias = %q, want https://legacy.api/v1", cfg.ZenBaseURL)
	}
	cfg, err = Load([]string{"serve", "--zen-base-url", "https://canonical.api/v1", "--upstream", "https://legacy.api/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ZenBaseURL != "https://canonical.api/v1" {
		t.Errorf("--zen-base-url should win over --upstream, got %q", cfg.ZenBaseURL)
	}
}

func TestLoad_FlagPrecedence(t *testing.T) {
	t.Setenv("LISTEN_ADDR", "0.0.0.0:8080")

	args := []string{"serve", "--listen", "0.0.0.0:9090"}

	cfg, err := Load(args)
	if err != nil {
		t.Fatal(err)
	}

	// Flag should override env
	if cfg.ListenAddr != "0.0.0.0:9090" {
		t.Errorf("listen_addr = %q, want 0.0.0.0:9090 (flag should override env)", cfg.ListenAddr)
	}
}

func TestLoad_ConfigFile(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.json")

	content := `{
		"listen_addr": "0.0.0.0:7070",
		"zen_base_url": "https://file.api/v1",
		"models": ["file-model"]
	}`
	if err := os.WriteFile(configFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	args := []string{"serve", "--config", configFile}
	cfg, err := Load(args)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.ListenAddr != "0.0.0.0:7070" {
		t.Errorf("listen_addr = %q, want 0.0.0.0:7070", cfg.ListenAddr)
	}
	if cfg.ZenBaseURL != "https://file.api/v1" {
		t.Errorf("zen_base_url = %q", cfg.ZenBaseURL)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].Name != "file-model" {
		t.Errorf("models = %v, want [file-model]", cfg.Models)
	}
	if cfg.DefaultModel != "file-model" {
		t.Errorf("default_model = %q, want file-model (derived from models[0])", cfg.DefaultModel)
	}
}

func TestLoad_BoolEnvVars(t *testing.T) {
	t.Setenv("ALLOW_UNLISTED_MODELS", "true")
	t.Setenv("EXPOSE_ALL_MODELS", "1")

	cfg, err := Load([]string{"serve"})
	if err != nil {
		t.Fatal(err)
	}

	if !cfg.AllowUnlisted {
		t.Error("allow_unlisted = false, want true")
	}
	if !cfg.ExposeAllModels {
		t.Error("expose_all_models = false, want true")
	}
}

func TestLoad_DurationEnvVars(t *testing.T) {
	t.Setenv("REQUEST_TIMEOUT", "60s")
	t.Setenv("MODEL_CACHE_TTL", "10m")

	cfg, err := Load([]string{"serve"})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.RequestTimeout != 60*time.Second {
		t.Errorf("request_timeout = %v, want 60s", cfg.RequestTimeout)
	}
	if cfg.ModelCacheTTL != 10*time.Minute {
		t.Errorf("model_cache_ttl = %v, want 10m", cfg.ModelCacheTTL)
	}
}

func TestValidate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ZenAPIKey = "zen-key"
	issues := cfg.Validate()
	if len(issues) != 0 {
		t.Errorf("default config has issues: %v", issues)
	}

	cfg.ZenBaseURL = ""
	issues = cfg.Validate()
	if len(issues) == 0 {
		t.Error("expected validation issues for empty upstream URL")
	}
}

func TestValidate_UnknownUpstream(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Models = []ModelSpec{
		{Name: "my-model", Upstream: "does-not-exist"},
	}
	issues := cfg.Validate()
	found := false
	for _, i := range issues {
		if strings.Contains(i, "does-not-exist") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected validation issue for unknown upstream, got %v", issues)
	}
}

func TestValidate_ReservedUpstreamName(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Upstreams = []UpstreamConfig{{Name: "zen", BaseURL: "https://x/v1", APIKey: "k"}}
	issues := cfg.Validate()
	found := false
	for _, i := range issues {
		if strings.Contains(i, "reserved") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected validation issue for reserved upstream name, got %v", issues)
	}
}

func TestValidate_KnownUpstreamOK(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Models = []ModelSpec{
		{Name: "my-model", Upstream: "custom"},
		{Name: "gpt-5.6-terra", Upstream: CodexUpstreamName},
	}
	cfg.Upstreams = []UpstreamConfig{{Name: "custom", BaseURL: "https://x/v1", APIKey: "k"}}
	if issues := cfg.Validate(); len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

func TestParseModels(t *testing.T) {
	// Comma list with explicit upstream.
	ms, err := ParseModels("a@opencode,b@custom,c")
	if err != nil {
		t.Fatal(err)
	}
	want := []ModelSpec{
		{Name: "a", Upstream: "opencode"},
		{Name: "b", Upstream: "custom"},
		{Name: "c", Upstream: DefaultUpstreamName},
	}
	if !reflect.DeepEqual(ms, want) {
		t.Errorf("ParseModels = %+v, want %+v", ms, want)
	}

	// JSON array of objects.
	ms, err = ParseModels(`[{"name":"x","upstream":"custom"},{"name":"y"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].Name != "x" || ms[0].Upstream != "custom" || ms[1].Name != "y" {
		t.Errorf("ParseModels JSON objects = %+v", ms)
	}

	// JSON legacy array of strings.
	ms, err = ParseModels(`["p","q"]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].Name != "p" || ms[0].Upstream != DefaultUpstreamName {
		t.Errorf("ParseModels legacy strings = %+v", ms)
	}
}

func TestMaskedKey(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.MaskedKey() != "" {
		t.Errorf("masked key = %q, want empty", cfg.MaskedKey())
	}

	cfg.ZenAPIKey = ""
	if cfg.MaskedKey() != "" {
		t.Error("expected empty for empty key")
	}

	cfg.ZenAPIKey = "short"
	if cfg.MaskedKey() != "***" {
		t.Errorf("masked key = %q, want ***", cfg.MaskedKey())
	}

	cfg.ZenAPIKey = "1234567890"
	masked := cfg.MaskedKey()
	if masked != "1234...7890" {
		t.Errorf("masked key = %q, want 1234...7890", masked)
	}
}

func TestString(t *testing.T) {
	cfg := DefaultConfig()
	s := cfg.String()
	if s == "" {
		t.Error("String() returned empty")
	}
}

func TestPrecompute_DefaultDiscoveryMode(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.DefaultModel != "" || len(cfg.PrecomputedFallbacks) != 0 {
		t.Fatalf("default config should wait for Zen discovery: %+v", cfg.PrecomputedFallbacks)
	}
}

func TestValidate_OpenCodeZenRequiresKey(t *testing.T) {
	cfg := DefaultConfig()
	if issues := cfg.Validate(); len(issues) == 0 {
		t.Fatal("expected missing Zen API key to fail validation")
	}
	cfg.ZenAPIKey = "public"
	if issues := cfg.Validate(); len(issues) == 0 {
		t.Fatal("expected legacy public key to fail validation")
	}
	cfg.ZenAPIKey = "zen-key"
	if issues := cfg.Validate(); len(issues) != 0 {
		t.Fatalf("real Zen key should validate: %v", issues)
	}
}

func TestValidate_KeyNotRequiredWithoutZenRoute(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
	}{
		{
			name: "custom only",
			cfg: func() *Config {
				c := DefaultConfig()
				c.Models = []ModelSpec{{Name: "model", Upstream: "custom"}}
				c.Upstreams = []UpstreamConfig{{Name: "custom", BaseURL: "https://example.com/v1"}}
				c.Precompute()
				return c
			}(),
		},
		{
			name: "codex only",
			cfg: func() *Config {
				c := DefaultConfig()
				c.Models = []ModelSpec{{Name: "gpt-5.6-sol", Upstream: CodexUpstreamName}}
				c.Precompute()
				return c
			}(),
		},
		{
			name: "passthrough with service key",
			cfg: func() *Config {
				c := DefaultConfig()
				c.PassthroughAPIKey = true
				c.ZenAPIKey = "zen-key"
				return c
			}(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if issues := tt.cfg.Validate(); len(issues) != 0 {
				t.Fatalf("unexpected validation issues: %v", issues)
			}
		})
	}
}

func TestPrecompute_EmptyModels(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Models = []ModelSpec{{Name: "my-model", Upstream: DefaultUpstreamName}}
	cfg.Precompute()

	if len(cfg.PrecomputedFallbacks) != 1 {
		t.Fatalf("PrecomputedFallbacks = %d, want 1 (only default model)", len(cfg.PrecomputedFallbacks))
	}
	if cfg.PrecomputedFallbacks[0].Name != "my-model" {
		t.Errorf("PrecomputedFallbacks[0] = %q, want my-model", cfg.PrecomputedFallbacks[0].Name)
	}
	if cfg.DefaultModel != "my-model" {
		t.Errorf("DefaultModel = %q, want my-model (derived from models[0])", cfg.DefaultModel)
	}
}

func TestPrecompute_Deduplication(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Models = []ModelSpec{
		{Name: "model-a", Upstream: DefaultUpstreamName},
		{Name: "model-a", Upstream: DefaultUpstreamName},
		{Name: "model-b", Upstream: DefaultUpstreamName},
		{Name: "model-c", Upstream: DefaultUpstreamName},
	}
	cfg.Precompute()

	expected := []ModelSpec{
		{Name: "model-a", Upstream: DefaultUpstreamName},
		{Name: "model-b", Upstream: DefaultUpstreamName},
		{Name: "model-c", Upstream: DefaultUpstreamName},
	}
	if len(cfg.PrecomputedFallbacks) != len(expected) {
		t.Fatalf("PrecomputedFallbacks = %v, want %v", cfg.PrecomputedFallbacks, expected)
	}
	for i, m := range expected {
		if cfg.PrecomputedFallbacks[i] != m {
			t.Errorf("PrecomputedFallbacks[%d] = %+v, want %+v", i, cfg.PrecomputedFallbacks[i], m)
		}
	}
}

func TestPrecompute_UnifiedModelsList(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Models = []ModelSpec{
		{Name: "big-pickle", Upstream: DefaultUpstreamName},
		{Name: "gpt-5.6-terra", Upstream: CodexUpstreamName},
		{Name: "deepseek-v4-flash-free", Upstream: DefaultUpstreamName},
	}
	cfg.Precompute()

	// DefaultModel should be derived from Models[0].
	if cfg.DefaultModel != "big-pickle" {
		t.Errorf("DefaultModel = %q, want big-pickle (derived from Models[0])", cfg.DefaultModel)
	}

	// PrecomputedFallbacks should follow the Models order exactly.
	expected := []ModelSpec{
		{Name: "big-pickle", Upstream: DefaultUpstreamName},
		{Name: "gpt-5.6-terra", Upstream: CodexUpstreamName},
		{Name: "deepseek-v4-flash-free", Upstream: DefaultUpstreamName},
	}
	if len(cfg.PrecomputedFallbacks) != len(expected) {
		t.Fatalf("PrecomputedFallbacks = %v, want %v", cfg.PrecomputedFallbacks, expected)
	}
	for i, m := range expected {
		if cfg.PrecomputedFallbacks[i] != m {
			t.Errorf("PrecomputedFallbacks[%d] = %+v, want %+v", i, cfg.PrecomputedFallbacks[i], m)
		}
	}
}

func TestPrecompute_UnifiedModelsDedup(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Models = []ModelSpec{
		{Name: "a", Upstream: DefaultUpstreamName},
		{Name: "", Upstream: DefaultUpstreamName},
		{Name: "a", Upstream: DefaultUpstreamName},
		{Name: "b", Upstream: DefaultUpstreamName},
		{Name: "b", Upstream: DefaultUpstreamName},
		{Name: "c", Upstream: DefaultUpstreamName},
	}

	cfg.Precompute()

	expected := []ModelSpec{
		{Name: "a", Upstream: DefaultUpstreamName},
		{Name: "b", Upstream: DefaultUpstreamName},
		{Name: "c", Upstream: DefaultUpstreamName},
	}
	if len(cfg.PrecomputedFallbacks) != len(expected) {
		t.Fatalf("PrecomputedFallbacks = %v, want %v", cfg.PrecomputedFallbacks, expected)
	}
	for i, m := range expected {
		if cfg.PrecomputedFallbacks[i] != m {
			t.Errorf("PrecomputedFallbacks[%d] = %+v, want %+v", i, cfg.PrecomputedFallbacks[i], m)
		}
	}
}

func TestLoad_UnifiedModelsFlag(t *testing.T) {
	args := []string{"serve", "--models", "m1,m2,m3"}
	cfg, err := Load(args)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 3 || cfg.Models[0].Name != "m1" {
		t.Fatalf("Models = %v, want [m1 m2 m3]", cfg.Models)
	}
	if cfg.DefaultModel != "m1" {
		t.Errorf("DefaultModel = %q, want m1", cfg.DefaultModel)
	}
}

func TestLoad_UnifiedModelsEnv(t *testing.T) {
	t.Setenv("MODELS", "env1, env2 , env3")
	cfg, err := Load([]string{"serve"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 3 || cfg.Models[0].Name != "env1" || cfg.Models[2].Name != "env3" {
		t.Fatalf("Models = %v, want [env1 env2 env3]", cfg.Models)
	}
}

func TestLoad_UnifiedModelsFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.json")
	content := `{
		"models": ["file1", "file2", "file3"]
	}`
	if err := os.WriteFile(configFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load([]string{"serve", "--config", configFile})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 3 || cfg.Models[0].Name != "file1" {
		t.Fatalf("Models = %v, want [file1 file2 file3]", cfg.Models)
	}
	if cfg.DefaultModel != "file1" {
		t.Errorf("DefaultModel = %q, want file1", cfg.DefaultModel)
	}
}

func TestLoad_ZenAPIKeyFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(configFile, []byte(`{"zen_api_key":"from-zen-key"}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load([]string{"serve", "--config", configFile})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ZenAPIKey != "from-zen-key" {
		t.Errorf("zen_api_key from file = %q, want from-zen-key", cfg.ZenAPIKey)
	}
}

func TestLoad_ZenAPIKeyLegacyFileAlias(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(configFile, []byte(`{"upstream_api_key":"legacy-file-key"}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load([]string{"serve", "--config", configFile})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ZenAPIKey != "legacy-file-key" {
		t.Errorf("zen_api_key via deprecated upstream_api_key = %q, want legacy-file-key", cfg.ZenAPIKey)
	}
}

func TestLoad_ZenBaseURLLegacyFileAlias(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(configFile, []byte(`{"upstream_base_url":"https://legacy-file.api/v1"}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load([]string{"serve", "--config", configFile})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ZenBaseURL != "https://legacy-file.api/v1" {
		t.Errorf("zen_base_url via deprecated upstream_base_url = %q, want https://legacy-file.api/v1", cfg.ZenBaseURL)
	}
}

func TestLoad_ZenKeyFlagAndAlias(t *testing.T) {
	cfg, err := Load([]string{"serve", "--zen-key", "flag-key"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ZenAPIKey != "flag-key" {
		t.Errorf("--zen-key = %q, want flag-key", cfg.ZenAPIKey)
	}
	cfg, err = Load([]string{"serve", "--upstream-key", "legacy-flag-key"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ZenAPIKey != "legacy-flag-key" {
		t.Errorf("--upstream-key alias = %q, want legacy-flag-key", cfg.ZenAPIKey)
	}
}

func TestLoad_ModelsWithUpstreamFlag(t *testing.T) {
	// "@opencode" is a deprecated alias and must be normalized to "zen".
	args := []string{"serve", "--models", "a@opencode,b@custom,c"}
	cfg, err := Load(args)
	if err != nil {
		t.Fatal(err)
	}
	want := []ModelSpec{
		{Name: "a", Upstream: "zen"},
		{Name: "b", Upstream: "custom"},
		{Name: "c", Upstream: "zen"},
	}
	if len(cfg.Models) != len(want) {
		t.Fatalf("Models = %v, want %v", cfg.Models, want)
	}
	for i, m := range want {
		if cfg.Models[i] != m {
			t.Errorf("Models[%d] = %+v, want %+v", i, cfg.Models[i], m)
		}
	}
}

func TestLoad_ModelsJSONFlag(t *testing.T) {
	args := []string{"serve", "--models", `[{"name":"x","upstream":"custom"},{"name":"y","upstream":"opencode"}]`}
	cfg, err := Load(args)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 2 || cfg.Models[0].Name != "x" || cfg.Models[0].Upstream != "custom" {
		t.Fatalf("Models = %v, want [{x custom} {y zen}]", cfg.Models)
	}
	if cfg.Models[1].Name != "y" || cfg.Models[1].Upstream != "zen" {
		t.Errorf("Models[1] = %+v, want {y zen} (legacy upstream_api_key alias normalized)", cfg.Models[1])
	}
}

func TestLoad_UpstreamsFlag(t *testing.T) {
	args := []string{"serve", "--upstreams", `[{"name":"custom","base_url":"https://h/v1","api_key":"sk-x"}]`}
	cfg, err := Load(args)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Upstreams) != 1 || cfg.Upstreams[0].Name != "custom" {
		t.Fatalf("Upstreams = %v, want [custom]", cfg.Upstreams)
	}
	if cfg.Upstreams[0].BaseURL != "https://h/v1" {
		t.Errorf("BaseURL = %q", cfg.Upstreams[0].BaseURL)
	}
}

func TestPrecompute_ModelsFirstIsDefault(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Models = []ModelSpec{
		{Name: "primary", Upstream: DefaultUpstreamName},
		{Name: "fallback-a", Upstream: DefaultUpstreamName},
		{Name: "fallback-b", Upstream: DefaultUpstreamName},
	}
	cfg.Precompute()

	// Models[0] is the default and leads the effective list.
	if cfg.DefaultModel != "primary" {
		t.Errorf("DefaultModel = %q, want primary (models[0])", cfg.DefaultModel)
	}
	if cfg.PrecomputedFallbacks[0].Name != "primary" {
		t.Errorf("PrecomputedFallbacks[0] = %q, want primary", cfg.PrecomputedFallbacks[0].Name)
	}
	// Fallbacks follow in order
	if len(cfg.PrecomputedFallbacks) != 3 {
		t.Fatalf("PrecomputedFallbacks length = %d, want 3", len(cfg.PrecomputedFallbacks))
	}
	if cfg.PrecomputedFallbacks[1].Name != "fallback-a" {
		t.Errorf("PrecomputedFallbacks[1] = %q, want fallback-a", cfg.PrecomputedFallbacks[1].Name)
	}
	if cfg.PrecomputedFallbacks[2].Name != "fallback-b" {
		t.Errorf("PrecomputedFallbacks[2] = %q, want fallback-b", cfg.PrecomputedFallbacks[2].Name)
	}
}

func TestIsOpenCodeModelSupported(t *testing.T) {
	for _, supported := range []string{"big-pickle", "future-free", "muse-spark-1.2-contributor-free"} {
		if !IsOpenCodeModelSupported(supported) {
			t.Errorf("model %q should be supported", supported)
		}
	}
	for _, unsupported := range []string{"claude-sonnet", "qwen3", "gemini-2.5"} {
		if IsOpenCodeModelSupported(unsupported) {
			t.Errorf("model %q should be unsupported", unsupported)
		}
	}
}

func TestIsOpenCodeContributorFree(t *testing.T) {
	for _, free := range []string{"muse-spark-1.2-contributor-free", "future-contributor-free"} {
		if !IsOpenCodeContributorFree(free) {
			t.Errorf("model %q should be contributor-free", free)
		}
	}
	for _, notFree := range []string{"big-pickle", "future-free", "gpt-5.6-terra"} {
		if IsOpenCodeContributorFree(notFree) {
			t.Errorf("model %q should not be contributor-free", notFree)
		}
	}
}
