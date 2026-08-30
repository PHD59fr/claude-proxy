package codex

import "strings"

// NormalizeModel removes client-side provider and effort decorations while
// preserving the server-provided Codex model slug. The remote account catalog,
// not a local allow-list, is the source of truth for model names.
func NormalizeModel(model string) string {
	// Strip provider prefix (e.g. "openai/gpt-5.6-sol" -> "gpt-5.6-sol")
	if idx := strings.IndexByte(model, '/'); idx >= 0 {
		model = model[idx+1:]
	}
	// Strip effort suffix (e.g. "gpt-5.6-sol-high" -> "gpt-5.6-sol")
	model = stripEffortSuffix(model)
	return model
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
