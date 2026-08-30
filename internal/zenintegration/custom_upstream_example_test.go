package zenintegration

import (
	"os"
	"testing"

	"github.com/claude-code-opencode/claude-proxy/internal/config"
)

// TestOpenRouterIntegration teste l'intégration avec OpenRouter.
// Exécutez avec : go test -v -run TestOpenRouterIntegration ./internal/providers/zen/
// Nécessite la variable d'environnement OPENROUTER_API_KEY.
func TestOpenRouterIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	RunCustomUpstreamTests(t, CustomUpstreamTestCase{
		Name:    "openrouter",
		BaseURL: "https://openrouter.ai/api/v1",
		APIKey:  os.Getenv("OPENROUTER_API_KEY"),
		Models: []config.ModelSpec{
			{Name: "anthropic/claude-3.5-sonnet", Upstream: "openrouter"},
			{Name: "google/gemini-pro-1.5", Upstream: "openrouter"},
		},
		RequestHeaders: map[string]string{
			"HTTP-Referer": "https://github.com/claude-proxy",
			"X-Title":      "claude-proxy",
		},
		SupportsStream: true,
		SupportsTools:  true,
		SkipIfNoKey:    true,
	})
}

// TestOpenAIIntegration teste l'intégration avec l'API OpenAI officielle.
// Exécutez avec : go test -v -run TestOpenAIIntegration ./internal/providers/zen/
// Nécessite la variable d'environnement OPENAI_API_KEY.
func TestOpenAIIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	RunCustomUpstreamTests(t, CustomUpstreamTestCase{
		Name:    "openai",
		BaseURL: "https://api.openai.com/v1",
		APIKey:  os.Getenv("OPENAI_API_KEY"),
		Models: []config.ModelSpec{
			{Name: "gpt-4o", Upstream: "openai"},
			{Name: "gpt-4o-mini", Upstream: "openai"},
		},
		SupportsStream: true,
		SupportsTools:  true,
		SkipIfNoKey:    true,
	})
}

// TestLMStudioIntegration teste l'intégration avec LM Studio (local).
// Exécutez avec : go test -v -run TestLMStudioIntegration ./internal/providers/zen/
// Nécessite LM Studio en cours d'exécution sur localhost:1234 avec un modèle chargé.
func TestLMStudioIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	baseURL := os.Getenv("LMSTUDIO_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:1234/v1"
	}

	RunCustomUpstreamTests(t, CustomUpstreamTestCase{
		Name:    "lmstudio",
		BaseURL: baseURL,
		APIKey:  "lm-studio", // LM Studio ignore souvent la clé
		Models: []config.ModelSpec{
			{Name: "local-model", Upstream: "lmstudio"},
		},
		SupportsStream: true,
		SupportsTools:  false, // Dépend du modèle chargé
		SkipIfNoKey:    false,
	})
}

// TestAzureOpenAIIntegration teste l'intégration avec Azure OpenAI.
// Exécutez avec : go test -v -run TestAzureOpenAIIntegration ./internal/providers/zen/
// Nécessite les variables d'environnement AZURE_OPENAI_ENDPOINT et AZURE_OPENAI_API_KEY.
func TestAzureOpenAIIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	endpoint := os.Getenv("AZURE_OPENAI_ENDPOINT")
	apiKey := os.Getenv("AZURE_OPENAI_API_KEY")
	deployment := os.Getenv("AZURE_OPENAI_DEPLOYMENT")
	if endpoint == "" || apiKey == "" || deployment == "" {
		t.Skip("AZURE_OPENAI_ENDPOINT, AZURE_OPENAI_API_KEY, AZURE_OPENAI_DEPLOYMENT required")
	}

	RunCustomUpstreamTests(t, CustomUpstreamTestCase{
		Name:    "azure",
		BaseURL: endpoint + "/openai/deployments/" + deployment,
		APIKey:  apiKey,
		Models: []config.ModelSpec{
			{Name: deployment, Upstream: "azure"},
		},
		RequestHeaders: map[string]string{
			"api-key": apiKey, // Azure utilise api-key header
		},
		SupportsStream: true,
		SupportsTools:  true,
		SkipIfNoKey:    true,
	})
}
