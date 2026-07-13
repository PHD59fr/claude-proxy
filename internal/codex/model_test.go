package codex

import "testing"

func TestNormalizeModel_ExactNames(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{ModelGPT54, ModelGPT54},
		{ModelGPT54Mini, ModelGPT54Mini},
		{ModelGPT56, ModelGPT56},
		{ModelGPT56Sol, ModelGPT56Sol},
		{ModelGPT56Terra, ModelGPT56Terra},
		{ModelGPT56Luna, ModelGPT56Luna},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeModel(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeModel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeModel_WithPrefix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"openai/gpt-5.6-sol", ModelGPT56Sol},
		{"openai/gpt-5.6-terra", ModelGPT56Terra},
		{"anthropic/gpt-5.4", ModelGPT54},
		{"openai/gpt-5.6-luna-high", ModelGPT56Luna},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeModel(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeModel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeModel_WithEffortSuffix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"gpt-5.6-sol-high", ModelGPT56Sol},
		{"gpt-5.6-sol-low", ModelGPT56Sol},
		{"gpt-5.6-terra-medium", ModelGPT56Terra},
		{"gpt-5.6-luna-xhigh", ModelGPT56Luna},
		{"gpt-5.4-mini-high", ModelGPT54Mini},
		{"gpt-5.4-none", ModelGPT54},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeModel(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeModel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeModel_LegacyModels(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"gpt-5", ModelGPT56Sol},
		{"gpt-5-high", ModelGPT56Sol},
		{"GPT-5", ModelGPT56Sol},
		{"gpt-5.1", ModelGPT56Sol},
		{"gpt-5.2", ModelGPT56Sol},
		{"gpt-5.2-codex", ModelGPT56Sol},
		{"codex-mini-latest", ModelGPT56Sol},
		{"gpt-5.3-codex", ModelGPT56Sol},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeModel(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeModel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeModel_Contains(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"some-codex-model", ModelGPT56Sol},
		{"my-gpt-5.1-codex-variant", ModelGPT56Sol},
		{"gpt-5.2-codex-custom", ModelGPT56Sol},
		{"random-string", "random-string"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeModel(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeModel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsCodexModelName(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{ModelGPT56Sol, true},
		{ModelGPT56Terra, true},
		{ModelGPT56Luna, true},
		{ModelGPT54, true},
		{ModelGPT54Mini, true},
		{"gpt-5.6-sol-high", true},
		{"openai/gpt-5.6-luna", true},
		{"gpt-5.1", true},
		{"gpt-5.2-codex", true},
		{"codex-mini-latest", true},
		{"gpt-5", true},
		{"gpt-5-high", true},
		{"random-model", false},
		{"claude-3-opus", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := IsCodexModelName(tt.input)
			if got != tt.want {
				t.Errorf("IsCodexModelName(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
