# Tests d'intégration pour Custom Upstreams

Ce dossier contient des tests réutilisables pour valider la compatibilité de vos upstreams OpenAI-compatibles personnalisés (OpenRouter, OpenAI, Azure, LM Studio, etc.) avec le proxy.

## Structure

| Fichier | Description |
|---------|-------------|
| `custom_upstream_integration_test.go` | Suite de tests générique `RunCustomUpstreamTests()` |
| `custom_upstream_example_test.go` | Exemples prêts à l'emploi pour providers populaires |

## Utilisation rapide

### 1. Test avec OpenRouter
```bash
export OPENROUTER_API_KEY="sk-or-v1-..."
go test -v -run TestOpenRouterIntegration ./internal/providers/zen/
```

### 2. Test avec OpenAI officiel
```bash
export OPENAI_API_KEY="sk-..."
go test -v -run TestOpenAIIntegration ./internal/providers/zen/
```

### 3. Test avec LM Studio (local)
```bash
# LM Studio doit tourner sur localhost:1234 avec un modèle chargé
go test -v -run TestLMStudioIntegration ./internal/providers/zen/
# Ou avec URL custom:
LMSTUDIO_BASE_URL="http://192.168.1.50:1234/v1" go test -v -run TestLMStudioIntegration ./internal/providers/zen/
```

### 4. Test avec Azure OpenAI
```bash
export AZURE_OPENAI_ENDPOINT="https://my-resource.openai.azure.com"
export AZURE_OPENAI_API_KEY="..."
export AZURE_OPENAI_DEPLOYMENT="gpt-4o-deployment"
go test -v -run TestAzureOpenAIIntegration ./internal/providers/zen/
```

## Créer votre propre test

Pour un provider non listé, créez un fichier `*_test.go` dans votre package de test :

```go
package myprovider_test

import (
    "os"
    "testing"
    
    "github.com/claude-code-opencode/claude-proxy/internal/config"
    "github.com/claude-code-opencode/claude-proxy/internal/providers/zen"
)

func TestMonProviderIntegration(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping integration test")
    }
    
    zen.RunCustomUpstreamTests(t, zen.CustomUpstreamTestCase{
        Name:        "mon-provider",
        BaseURL:     "https://api.monprovider.com/v1",
        APIKey:      os.Getenv("MON_PROVIDER_API_KEY"),
        Models: []config.ModelSpec{
            {Name: "model-id-1", Upstream: "mon-provider"},
            {Name: "model-id-2", Upstream: "mon-provider"},
        },
        // Headers custom si nécessaire (ex: OpenRouter)
        RequestHeaders: map[string]string{
            "X-Custom-Header": "value",
        },
        SupportsStream: true,
        SupportsTools:  true,
        SkipIfNoKey:    true,
    })
}
```

## Tests inclus dans la suite

| Test | Description |
|------|-------------|
| `NonStreaming_BasicText` | Requête simple non-streaming |
| `Streaming_BasicText` | Streaming SSE (si `SupportsStream=true`) |
| `Tools_FunctionCalling` | Appel de fonctions (si `SupportsTools=true`) |
| `SystemPrompt` | Transmission du system prompt |
| `FallbackToNextModel` | Fallback sur erreur 5xx/transport |
| `RateLimitHandling` | Gestion 429 avec Retry-After |
| `ModelUnavailableHandling` | Détection modèle indisponible (400 model_not_available) |
| `RequestHeadersForwarded` | Vérification headers custom transmis |

## Configuration requise dans config.json

Pour que l'upstream soit utilisable en production, ajoutez-le à votre `config.json` :

```json
{
  "upstreams": [
    {
      "name": "openrouter",
      "base_url": "https://openrouter.ai/api/v1",
      "api_key": "${OPENROUTER_API_KEY}"
    }
  ],
  "models": [
    {"name": "anthropic/claude-3.5-sonnet", "upstream": "openrouter"},
    {"name": "google/gemini-pro-1.5", "upstream": "openrouter"}
  ]
}
```

## Exécution de tous les tests d'intégration

```bash
# Tous les tests d'intégration (nécessite les clés API)
go test -v ./internal/providers/zen/ -run Integration

# Ou explicitement
go test -v ./internal/providers/zen/ -run "OpenRouter|OpenAI|LMStudio|Azure"
```

## Notes importantes

1. **Mode short** : Les tests d'intégration sont ignorés avec `go test -short` (par défaut dans CI)
2. **Variables d'env** : Les clés API sont lues depuis l'environnement, jamais en dur
3. **Nettoyage** : Chaque test crée son propre `httptest.Server`, pas de pollution entre tests
4. **Timeout** : Les tests ont un timeout de 30s par défaut (configurable via `-timeout`)

## Dépannage

| Erreur | Cause probable |
|--------|----------------|
| `401 Unauthorized` | Clé API invalide ou expirée |
| `404 Not Found` | Mauvaise `BaseURL` ou modèle inexistant |
| `429 Rate Limited` | Quota dépassé - le test rate limit s'attend à ça |
| `connection refused` | Upstream non accessible (LM Studio pas démarré, mauvais URL) |
| `tools not supported` | Le modèle ne supporte pas function calling |