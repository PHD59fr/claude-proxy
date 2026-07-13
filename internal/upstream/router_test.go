package upstream

import (
	"testing"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/config"
)

func TestRouter_ClientForModel(t *testing.T) {
	router := NewRouter([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: "https://opencode/v1", APIKey: "pub"},
		{Name: "custom", BaseURL: "https://custom/v1", APIKey: "sk-x"},
	}, nil, 30*time.Second)

	// Known upstream.
	c, err := router.ClientForModel("my-model", "custom")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.BaseURL() != "https://custom/v1" {
		t.Errorf("BaseURL = %q, want https://custom/v1", c.BaseURL())
	}

	// Default upstream.
	c, err = router.ClientForModel("big-pickle", config.DefaultUpstreamName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.BaseURL() != "https://opencode/v1" {
		t.Errorf("BaseURL = %q, want https://opencode/v1", c.BaseURL())
	}

	// Unknown upstream.
	if _, err := router.ClientForModel("x", "nope"); err == nil {
		t.Error("expected error for unknown upstream")
	}

	// Codex upstream is not an OpenAI client.
	if _, err := router.ClientForModel("gpt-5.6-sol", config.CodexUpstreamName); err == nil {
		t.Error("expected error routing codex upstream to ClientForModel")
	}
}

func TestRouter_Codex(t *testing.T) {
	codexClient := &CodexClient{}
	router := NewRouter([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: "https://opencode/v1", APIKey: "pub"},
	}, codexClient, 30*time.Second)

	if router.Codex() != codexClient {
		t.Error("Codex() should return the configured codex client")
	}

	// Without codex configured.
	router2 := NewRouter(nil, nil, 30*time.Second)
	if router2.Codex() != nil {
		t.Error("Codex() should be nil when not configured")
	}
}

func TestRouter_DefaultClient(t *testing.T) {
	router := NewRouter([]config.UpstreamConfig{
		{Name: config.DefaultUpstreamName, BaseURL: "https://opencode/v1", APIKey: "pub"},
	}, nil, 30*time.Second)
	if router.DefaultClient() == nil {
		t.Error("DefaultClient() should not be nil")
	}

	empty := NewRouter(nil, nil, 30*time.Second)
	if empty.DefaultClient() != nil {
		t.Error("DefaultClient() should be nil when opencode upstream absent")
	}
}
