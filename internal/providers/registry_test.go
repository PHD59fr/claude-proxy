package providers

import (
	"sync"
	"testing"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/config"
	"github.com/claude-code-opencode/claude-proxy/internal/providers/codex"
	"github.com/claude-code-opencode/claude-proxy/internal/providers/zen"
)

func TestRegistry_Provider(t *testing.T) {
	reg := NewRegistry([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: "https://opencode/v1", APIKey: "pub"},
		{Name: "custom", BaseURL: "https://custom/v1", APIKey: "sk-x"},
	}, nil, 30*time.Second)

	// Known upstream.
	p := reg.Provider("custom")
	if p == nil {
		t.Fatalf("Provider(custom) = nil")
	}
	zc, ok := p.(*zen.Client)
	if !ok {
		t.Fatalf("Provider(custom) = %T, want *zen.Client", p)
	}
	if zc.BaseURL() != "https://custom/v1" {
		t.Errorf("BaseURL = %q, want https://custom/v1", zc.BaseURL())
	}

	// Unknown upstream.
	if reg.Provider("nope") != nil {
		t.Error("expected nil for unknown provider")
	}

	// Codex is not registered as an OpenAI provider.
	if reg.Provider(config.CodexUpstreamName) != nil {
		t.Error("expected nil for codex provider name")
	}
}

func TestRegistry_Codex(t *testing.T) {
	codexClient := codex.NewClient("https://example.com", "tok", "acct", 30*time.Second)
	reg := NewRegistry([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: "https://opencode/v1", APIKey: "pub"},
	}, codexClient, 30*time.Second)

	if reg.Codex() != codexClient {
		t.Error("Codex() should return the configured codex client")
	}

	// Without codex configured.
	reg2 := NewRegistry(nil, nil, 30*time.Second)
	if reg2.Codex() != nil {
		t.Error("Codex() should be nil when not configured")
	}
}

func TestRegistry_Default(t *testing.T) {
	reg := NewRegistry([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: "https://opencode/v1", APIKey: "pub"},
	}, nil, 30*time.Second)
	if reg.Default() == nil {
		t.Error("Default() should not be nil")
	}

	empty := NewRegistry(nil, nil, 30*time.Second)
	if empty.Default() != nil {
		t.Error("Default() should be nil when zen upstream absent")
	}
}

func TestRegistryConcurrentCodexPublication(t *testing.T) {
	reg := NewRegistry(nil, nil, time.Second)
	const updates = 100

	var wg sync.WaitGroup
	for i := 0; i < updates; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = reg.Codex()
		}()
	}
	for i := 0; i < updates; i++ {
		reg.SetCodex(codex.NewClient("https://example.com", "tok", "acct", time.Second))
	}
	wg.Wait()
}
