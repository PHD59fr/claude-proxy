package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/claude-code-opencode/claude-proxy/internal/config"
)

// TestConfigToFileConfigRoundTrip verifies that the config produced by
// configToFileConfig (used by the wizard and web save) can be written to disk
// and re-loaded by config.Load without losing settings. In particular it guards
// fields that were previously dropped, like only_preferred_models.
func TestConfigToFileConfigRoundTrip(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ListenAddr = "0.0.0.0:7777"
	cfg.ZenBaseURL = "https://custom.zen/v1"
	cfg.ZenAPIKey = "zen-key"
	cfg.InboundAPIKey = "inbound-key"
	cfg.PassthroughAPIKey = true
	cfg.AllowUnlisted = true
	cfg.ExposeAllModels = true
	cfg.OnlyPreferredModels = true
	cfg.ReasoningModel = "reason-model"
	cfg.CompletionModel = "completion-model"
	cfg.Models = []config.ModelSpec{
		{Name: "model-a", Upstream: config.DefaultUpstreamName},
		{Name: "model-b", Upstream: "custom"},
	}
	cfg.Upstreams = []config.UpstreamConfig{{Name: "custom", BaseURL: "https://custom/v1", APIKey: "custom-key"}}
	cfg.MaxBodySize = 5
	cfg.WebInterfacePort = "9999"
	cfg.WebInterfaceKey = "web-key"
	cfg.Precompute()

	fc := configToFileConfig(cfg)
	data, err := json.Marshal(fc)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	loaded, err := config.Load([]string{"serve", "--config", path})
	if err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"listen_addr", loaded.ListenAddr, "0.0.0.0:7777"},
		{"zen_base_url", loaded.ZenBaseURL, "https://custom.zen/v1"},
		{"zen_api_key", loaded.ZenAPIKey, "zen-key"},
		{"inbound_api_key", loaded.InboundAPIKey, "inbound-key"},
		{"passthrough", loaded.PassthroughAPIKey, true},
		{"allow_unlisted", loaded.AllowUnlisted, true},
		{"expose_all", loaded.ExposeAllModels, true},
		{"only_preferred", loaded.OnlyPreferredModels, true},
		{"reasoning_model", loaded.ReasoningModel, "reason-model"},
		{"completion_model", loaded.CompletionModel, "completion-model"},
		{"web_interface_port", loaded.WebInterfacePort, "9999"},
		{"web_interface_key", loaded.WebInterfaceKey, "web-key"},
		{"max_body_size", loaded.MaxBodySize, int64(5)},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %#v, want %#v", c.name, c.got, c.want)
		}
	}

	if len(loaded.Models) != 2 || loaded.Models[0].Name != "model-a" || loaded.Models[1].Name != "model-b" {
		t.Errorf("models = %+v, want [model-a model-b]", loaded.Models)
	}
	if loaded.Models[0].Upstream != config.DefaultUpstreamName || loaded.Models[1].Upstream != "custom" {
		t.Errorf("models upstreams = %+v, want [zen custom]", loaded.Models)
	}
	if len(loaded.Upstreams) != 1 || loaded.Upstreams[0].Name != "custom" || loaded.Upstreams[0].BaseURL != "https://custom/v1" {
		t.Errorf("upstreams = %+v, want [custom]", loaded.Upstreams)
	}
}
