package codex

import "testing"

func TestNormalizeModel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"gpt-5.6-sol", "gpt-5.6-sol"},
		{"openai/gpt-5.7-new", "gpt-5.7-new"},
		{"gpt-5.6-sol-high", "gpt-5.6-sol"},
		{"openai/future-codex-xhigh", "future-codex"},
		{"gpt-5.2-codex", "gpt-5.2-codex"},
		{"random-model", "random-model"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := NormalizeModel(tt.input); got != tt.want {
				t.Fatalf("NormalizeModel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestVisibleModels(t *testing.T) {
	models := VisibleModels([]ModelInfo{
		{Slug: "hidden", Visibility: "hide", Priority: 0},
		{Slug: "second", Visibility: "list", Priority: 2},
		{Slug: "first", Visibility: "list", Priority: 1},
	})
	if len(models) != 2 || models[0].Slug != "first" || models[1].Slug != "second" {
		t.Fatalf("VisibleModels = %+v", models)
	}
}
