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
	if cfg.UpstreamBaseURL != "https://opencode.ai/zen/v1" {
		t.Errorf("upstream_base_url = %q", cfg.UpstreamBaseURL)
	}
	if cfg.UpstreamAPIKey != "public" {
		t.Errorf("upstream_api_key = %q", cfg.UpstreamAPIKey)
	}
	if cfg.DefaultModel != "big-pickle" {
		t.Errorf("default_model = %q", cfg.DefaultModel)
	}
	if cfg.RequestTimeout != 300*time.Second {
		t.Errorf("request_timeout = %v", cfg.RequestTimeout)
	}
}

func TestLoad_EnvironmentVariables(t *testing.T) {
	t.Setenv("LISTEN_ADDR", "0.0.0.0:8080")
	t.Setenv("UPSTREAM_BASE_URL", "https://custom.api/v1")
	t.Setenv("UPSTREAM_API_KEY", "custom-key")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load([]string{"serve"})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.ListenAddr != "0.0.0.0:8080" {
		t.Errorf("listen_addr = %q, want 0.0.0.0:8080", cfg.ListenAddr)
	}
	if cfg.UpstreamBaseURL != "https://custom.api/v1" {
		t.Errorf("upstream_base_url = %q", cfg.UpstreamBaseURL)
	}
	if cfg.UpstreamAPIKey != "custom-key" {
		t.Errorf("upstream_api_key = %q", cfg.UpstreamAPIKey)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("log_level = %q", cfg.LogLevel)
	}
}

func TestLoad_Flags(t *testing.T) {
	args := []string{"serve", "--listen", "0.0.0.0:9090", "--upstream", "https://flag.api/v1", "--models", "flag-model"}

	cfg, err := Load(args)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.ListenAddr != "0.0.0.0:9090" {
		t.Errorf("listen_addr = %q, want 0.0.0.0:9090", cfg.ListenAddr)
	}
	if cfg.UpstreamBaseURL != "https://flag.api/v1" {
		t.Errorf("upstream_base_url = %q", cfg.UpstreamBaseURL)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].Name != "flag-model" {
		t.Errorf("models = %v, want [flag-model]", cfg.Models)
	}
	if cfg.DefaultModel != "flag-model" {
		t.Errorf("default_model = %q, want flag-model (derived from models[0])", cfg.DefaultModel)
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
		"upstream_base_url": "https://file.api/v1",
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
	if cfg.UpstreamBaseURL != "https://file.api/v1" {
		t.Errorf("upstream_base_url = %q", cfg.UpstreamBaseURL)
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
	issues := cfg.Validate()
	if len(issues) != 0 {
		t.Errorf("default config has issues: %v", issues)
	}

	cfg.UpstreamBaseURL = ""
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
	cfg.Upstreams = []UpstreamConfig{{Name: "opencode", BaseURL: "https://x/v1", APIKey: "k"}}
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

	// Default config has "public" as key
	if cfg.MaskedKey() != "***" {
		t.Errorf("masked key = %q, want ***", cfg.MaskedKey())
	}

	cfg.UpstreamAPIKey = ""
	if cfg.MaskedKey() != "" {
		t.Error("expected empty for empty key")
	}

	cfg.UpstreamAPIKey = "short"
	if cfg.MaskedKey() != "***" {
		t.Errorf("masked key = %q, want ***", cfg.MaskedKey())
	}

	cfg.UpstreamAPIKey = "1234567890"
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

func TestPrecompute_DefaultModels(t *testing.T) {
	cfg := DefaultConfig()

	// DefaultConfig calls Precompute, so PrecomputedFallbacks should be set
	if len(cfg.PrecomputedFallbacks) == 0 {
		t.Fatal("PrecomputedFallbacks is empty")
	}

	// DefaultModel should always be the first entry
	if cfg.PrecomputedFallbacks[0].Name != "big-pickle" {
		t.Errorf("PrecomputedFallbacks[0] = %q, want big-pickle", cfg.PrecomputedFallbacks[0].Name)
	}

	// Should include all unique fallback models
	seen := make(map[string]bool)
	for _, m := range cfg.PrecomputedFallbacks {
		if seen[m.Name] {
			t.Errorf("duplicate model in PrecomputedFallbacks: %q", m.Name)
		}
		seen[m.Name] = true
	}

	// Default fallback models should be present
	for _, expected := range []string{"big-pickle", "deepseek-v4-flash-free", "hy3-free"} {
		if !seen[expected] {
			t.Errorf("missing expected model %q in PrecomputedFallbacks", expected)
		}
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

func TestLoad_ModelsWithUpstreamFlag(t *testing.T) {
	args := []string{"serve", "--models", "a@opencode,b@custom,c"}
	cfg, err := Load(args)
	if err != nil {
		t.Fatal(err)
	}
	want := []ModelSpec{
		{Name: "a", Upstream: "opencode"},
		{Name: "b", Upstream: "custom"},
		{Name: "c", Upstream: "opencode"},
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
		t.Fatalf("Models = %v, want [{x custom} {y opencode}]", cfg.Models)
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
