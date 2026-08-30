package config

import (
	"strings"
)

// UpstreamForModel returns the upstream name that serves the given model name.
// It first looks up the model in the configured Models list; if not found it
// defaults to the built-in "zen" upstream.
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

// UsesOpenCodeResponsesAPI reports whether a route needs Zen's Responses API.
// The upstream check prevents custom providers with the same model ID from
// being rerouted away from their OpenAI-compatible endpoint.
func UsesOpenCodeResponsesAPI(model, upstreamName string) bool {
	if upstreamName != DefaultUpstreamName {
		return false
	}
	return strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "grok-") || strings.HasPrefix(model, "muse-")
}

// IsOpenCodeModelSupported reports whether the proxy has a converter for the
// protocol family used by a Zen model. Anthropic/Qwen Messages and Gemini
// generateContent models are omitted until those transports are implemented.
func IsOpenCodeModelSupported(model string) bool {
	return !strings.HasPrefix(model, "claude-") &&
		!strings.HasPrefix(model, "qwen") &&
		!strings.HasPrefix(model, "gemini-")
}

// IsOpenCodeContributorFree reports whether a Zen model belongs to a
// contributor's personal quota. These models are excluded from automatic
// discovery because they may surface quota-bound routes; they remain usable
// when listed explicitly.
func IsOpenCodeContributorFree(model string) bool {
	return strings.HasSuffix(model, "-contributor-free")
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
	// Normalize the canonical preference list in place (dedupe + legacy
	// "opencode" -> "zen") so every consumer sees the same upstream names.
	c.Models = dedupeModels(c.Models)

	// Derive DefaultModel (the first entry) from the unified Models list.
	// Only set if DefaultModel was not explicitly configured (via env/flag/file).
	if _, explicit := c.Sources["default_model"]; !explicit {
		if len(c.Models) > 0 {
			c.DefaultModel = c.Models[0].Name
		} else {
			c.DefaultModel = ""
		}
	}

	// The effective preference list is the deduplicated Models list (1st = default,
	// rest = ordered fallbacks).
	c.PrecomputedFallbacks = c.Models

	// Precompute the set of models allowed when AllowUnlisted is false, so the
	// per-request check in the proxy is O(1) instead of O(default models).
	c.allowedModels = make(map[string]bool, len(c.PrecomputedFallbacks)+3)
	if c.DefaultModel != "" {
		c.allowedModels[c.DefaultModel] = true
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
// occurrence). It also normalizes the legacy upstream name "opencode" to the
// canonical "zen" so older model lists keep working.
func dedupeModels(in []ModelSpec) []ModelSpec {
	seen := make(map[string]bool)
	var out []ModelSpec
	for _, m := range in {
		m.Name = strings.TrimSpace(m.Name)
		if m.Name == "" {
			continue
		}
		if m.Upstream == "" || m.Upstream == LegacyOpenCodeUpstreamName {
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
