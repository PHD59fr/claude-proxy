package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/claude-code-opencode/claude-proxy/internal/config"
)

func TestConfigEndpointReturnsCurrentConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ListenAddr = "0.0.0.0:4242"
	cfg.WebInterfacePort = "8080"
	cfg.ZenBaseURL = "https://example.test/v1"
	cfg.Models = []config.ModelSpec{{Name: "current-model", Upstream: config.DefaultUpstreamName}}
	cfg.Precompute()

	provider := NewDefaultProvider(cfg, "test", nil, nil, nil, nil, nil)
	server := NewServer("127.0.0.1:0", "secret", provider)
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("user:secret")))
	res := httptest.NewRecorder()
	server.mux.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Code, res.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	assertFieldValue(t, body, "listen_port", "4242")
	assertFieldValue(t, body, "web_interface_port", "8080")
	assertFieldValue(t, body, "zen_base_url", "https://example.test/v1")

	var modelsField struct {
		Value []config.ModelSpec `json:"value"`
	}
	if err := json.Unmarshal(body["models"], &modelsField); err != nil {
		t.Fatal(err)
	}
	if len(modelsField.Value) != 1 || modelsField.Value[0].Name != "current-model" {
		t.Fatalf("models = %+v, want current-model", modelsField.Value)
	}
}

func TestReorderModelsDelegatesToCallback(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models = []config.ModelSpec{
		{Name: "b", Upstream: config.DefaultUpstreamName},
		{Name: "a", Upstream: config.DefaultUpstreamName},
	}
	cfg.Precompute()

	provider := NewDefaultProvider(cfg, "test", nil, nil, nil, nil, nil)

	var got []string
	called := false
	provider.SetReorderModels(func(names []string) error {
		called = true
		got = names
		return nil
	})

	provider.ReorderModels([]string{"a", "b"})
	if !called {
		t.Fatal("expected the reorder callback to be invoked when set")
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("callback received %v, want [a b]", got)
	}
}

func TestDashboardEscaperIsValidAndAttributeSafe(t *testing.T) {
	want := "replace(/\"/g,'&quot;').replace(/'/g,'&#39;')"
	if !strings.Contains(pageHTML, want) {
		t.Fatalf("dashboard HTML escaper does not contain %q", want)
	}
	if strings.Contains(pageHTML, "replace(/'/g,''')") {
		t.Fatal("dashboard HTML contains the malformed JavaScript escaper")
	}
}

func assertFieldValue(t *testing.T, body map[string]json.RawMessage, field string, want interface{}) {
	t.Helper()
	var got struct {
		Value interface{} `json:"value"`
	}
	if err := json.Unmarshal(body[field], &got); err != nil {
		t.Fatalf("decode %s: %v", field, err)
	}
	if got.Value != want {
		t.Fatalf("%s = %#v, want %#v", field, got.Value, want)
	}
}
