package codex

import "strings"

// NormalizeModel converts any user-provided model name to a canonical Codex model name.
// It handles provider prefixes (e.g. "openai/gpt-5.6-sol"), effort suffixes
// (e.g. "gpt-5.6-sol-high"), and legacy model names.
func NormalizeModel(model string) string {
	// Strip provider prefix (e.g. "openai/gpt-5.6-sol" -> "gpt-5.6-sol")
	if idx := strings.IndexByte(model, '/'); idx >= 0 {
		model = model[idx+1:]
	}
	// Strip effort suffix (e.g. "gpt-5.6-sol-high" -> "gpt-5.6-sol")
	model = stripEffortSuffix(model)
	// Lowercase for matching
	lower := strings.ToLower(model)

	// Priority cascade matching (most specific first)
	switch {
	case strings.Contains(lower, "gpt-5.6-sol"):
		return ModelGPT56Sol
	case strings.Contains(lower, "gpt-5.6-terra"):
		return ModelGPT56Terra
	case strings.Contains(lower, "gpt-5.6-luna"):
		return ModelGPT56Luna
	case strings.Contains(lower, "gpt-5.6"):
		return ModelGPT56
	case strings.Contains(lower, "gpt-5.4-mini"):
		return ModelGPT54Mini
	case strings.Contains(lower, "gpt-5.4"):
		return ModelGPT54
	case strings.Contains(lower, "gpt-5.3-codex") || strings.Contains(lower, "codex"):
		// Legacy codex models -> gpt-5.6-sol (new default)
		return ModelGPT56Sol
	case strings.Contains(lower, "gpt-5"):
		// Legacy gpt-5.x models -> gpt-5.6-sol
		return ModelGPT56Sol
	default:
		return model
	}
}

// stripEffortSuffix removes effort level suffixes like -low, -medium, -high, -xhigh, -none.
func stripEffortSuffix(model string) string {
	suffixes := []string{"-xhigh", "-high", "-medium", "-low", "-none", "-chat-latest"}
	for _, suffix := range suffixes {
		if strings.HasSuffix(model, suffix) {
			return model[:len(model)-len(suffix)]
		}
	}
	return model
}

// IsCodexModelName checks if a model name (after normalization) is a Codex model.
func IsCodexModelName(model string) bool {
	normalized := NormalizeModel(model)
	return KnownCodexModels[normalized]
}
