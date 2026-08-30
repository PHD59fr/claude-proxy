package providers

import (
	"sync"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/config"
	"github.com/claude-code-opencode/claude-proxy/internal/providers/zen"
)

// Registry resolves provider names (from the model preference list) to
// Provider instances. The built-in "zen" upstream and any configured custom
// upstreams are OpenAI-compatible, served by *zen.Client; the optional "codex"
// backend is served by *codex.Client.
type Registry struct {
	providers map[string]Provider

	codexMu sync.RWMutex
	codex   Provider
}

// NewRegistry builds a Registry from a list of upstreams plus the optional
// Codex backend. The list should include the implicit "zen" upstream (built by
// the caller from Config.ZenBaseURL/ZenAPIKey) so models referencing it
// resolve to a real provider.
func NewRegistry(upstreams []config.UpstreamConfig, codexClient Provider, timeout time.Duration) *Registry {
	r := &Registry{
		providers: make(map[string]Provider, len(upstreams)),
		codex:     codexClient,
	}
	for _, u := range upstreams {
		if u.Name == "" {
			continue
		}
		if _, exists := r.providers[u.Name]; exists {
			continue
		}
		r.providers[u.Name] = zen.NewClient(u.Name, u.BaseURL, u.APIKey, timeout)
	}
	return r
}

// Provider returns the provider registered under the given name, or nil.
func (r *Registry) Provider(name string) Provider {
	return r.providers[name]
}

// Default returns the built-in "zen" provider (nil if not configured).
func (r *Registry) Default() Provider {
	return r.providers[config.DefaultUpstreamName]
}

// Codex returns the Codex backend provider (nil if not configured).
func (r *Registry) Codex() Provider {
	r.codexMu.RLock()
	defer r.codexMu.RUnlock()
	return r.codex
}

// SetCodex replaces the Codex backend provider (used on config reload and
// token refresh).
func (r *Registry) SetCodex(c Provider) {
	r.codexMu.Lock()
	r.codex = c
	r.codexMu.Unlock()
}
