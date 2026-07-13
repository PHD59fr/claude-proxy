package config

import (
	"strings"

	"github.com/claude-code-opencode/claude-proxy/internal/models"
)

// UpstreamForModel returns the upstream name that serves the given model name.
// It first looks up the model in the configured Models list; if not found it
// defaults to the built-in "opencode" upstream.
func (c *Config) UpstreamForModel(model string) string {
	for _, m := range c.Models {
		if m.Name == model {
			if m.Upstream != "" {
				return m.Upstream
			}
			return DefaultUpstreamName
		}
	}
	return DefaultUpstreamName
}

// EffectiveModels returns the ordered ModelSpecs the proxy will actually use.
// `Models` is the single ordered preference list: its first entry is the
// default model and the rest are fallbacks in priority order.
func (c *Config) EffectiveModels() []ModelSpec {
	return dedupeModels(c.Models)
}

// ResolvedConfig returns a copy of the config. `Models` is already the
// single source of truth (1st = default, rest = ordered fallbacks), so no
// legacy folding is required.
func (c *Config) ResolvedConfig() *Config {
	clone := *c
	return &clone
}

// IsModelAllowed reports whether the given model may be served without
// AllowUnlisted. The result is O(1), backed by the set precomputed in
// Precompute.
func (c *Config) IsModelAllowed(model string) bool {
	if model == "" {
		return false
	}
	return c.allowedModels[model]
}

// Precompute computes cached derived values like the default model, the
// effective preference list and the allowed-model set. `Models` is the single
// source of truth: its first entry is the default model and the rest are
// ordered fallbacks.
func (c *Config) Precompute() {
	// Derive DefaultModel (the first entry) from the unified Models list.
	if len(c.Models) > 0 {
		deduped := dedupeModels(c.Models)
		if len(deduped) > 0 {
			c.DefaultModel = deduped[0].Name
		} else {
			c.DefaultModel = ""
		}
	} else {
		c.DefaultModel = ""
	}

	// The effective preference list is the deduplicated Models list (1st = default,
	// rest = ordered fallbacks).
	c.PrecomputedFallbacks = dedupeModels(c.Models)

	// Precompute the set of models allowed when AllowUnlisted is false, so the
	// per-request check in the proxy is O(1) instead of O(default models).
	c.allowedModels = make(map[string]bool, len(models.DefaultModels)+1)
	if c.DefaultModel != "" {
		c.allowedModels[c.DefaultModel] = true
	}
	for _, m := range models.DefaultModels {
		c.allowedModels[m.ID] = true
	}
	if strings.HasSuffix(c.DefaultModel, "-free") {
		c.allowedModels[c.DefaultModel] = true
	}
	// Reasoning/completion models are injected by the proxy before the
	// allow-list check, so they must always be permitted.
	if c.ReasoningModel != "" {
		c.allowedModels[c.ReasoningModel] = true
	}
	if c.CompletionModel != "" {
		c.allowedModels[c.CompletionModel] = true
	}
	// All models in the effective list should also be allowed directly.
	for _, m := range c.PrecomputedFallbacks {
		c.allowedModels[m.Name] = true
	}
}

// dedupeModels removes empty/dup names while preserving order. A spec is a
// duplicate if its Name already appeared (its Upstream is taken from the first
// occurrence).
func dedupeModels(in []ModelSpec) []ModelSpec {
	seen := make(map[string]bool)
	var out []ModelSpec
	for _, m := range in {
		m.Name = strings.TrimSpace(m.Name)
		if m.Name == "" {
			continue
		}
		if m.Upstream == "" {
			m.Upstream = DefaultUpstreamName
		}
		if seen[m.Name] {
			continue
		}
		seen[m.Name] = true
		out = append(out, m)
	}
	return out
}
