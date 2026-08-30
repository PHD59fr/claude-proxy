package codex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCodexModelsUsesAccountScopedCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/models" || r.URL.Query().Get("client_version") != CodexClientVersion {
			t.Fatalf("unexpected models URL: %s", r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer oauth" || r.Header.Get("chatgpt-account-id") != "account" {
			t.Fatalf("missing account authentication headers")
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Fatalf("Accept = %q", r.Header.Get("Accept"))
		}
		_, _ = w.Write([]byte(`{"models":[{"slug":"second","visibility":"list","priority":2},{"slug":"hidden","visibility":"hide","priority":0},{"slug":"first","visibility":"list","priority":1}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "oauth", "account", time.Second)
	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].Slug != "first" || models[1].Slug != "second" {
		t.Fatalf("models = %+v", models)
	}
}
