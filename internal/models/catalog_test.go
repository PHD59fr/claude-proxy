package models

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCatalog_FetchSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "gpt-4", "object": "model", "owned_by": "openai"},
				{"id": "gpt-3.5-turbo", "object": "model", "owned_by": "openai"}
			]
		}`))
	}))
	defer ts.Close()

	catalog := NewCatalog(ts.URL, "test-key", 5*time.Minute)
	err := catalog.Fetch()
	if err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}

	models := catalog.GetModels(false)
	if len(models) != 2 {
		t.Fatalf("models = %d, want 2", len(models))
	}
	if models[0].ID != "gpt-4" {
		t.Errorf("model[0].ID = %q, want gpt-4", models[0].ID)
	}
	if models[1].ID != "gpt-3.5-turbo" {
		t.Errorf("model[1].ID = %q, want gpt-3.5-turbo", models[1].ID)
	}
}

func TestCatalog_CacheHit(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "cached-model", "object": "model", "owned_by": "test"}
			]
		}`))
	}))
	defer ts.Close()

	// Use a long TTL so the cache is always hit
	catalog := NewCatalog(ts.URL, "test-key", 1*time.Hour)

	// First call fetches from upstream
	models := catalog.GetModels(false)
	if len(models) != 1 {
		t.Fatalf("first GetModels: got %d models, want 1", len(models))
	}

	// Second call should use cache (no additional fetch)
	models2 := catalog.GetModels(false)
	if len(models2) != 1 {
		t.Fatalf("second GetModels: got %d models, want 1", len(models2))
	}

	if callCount != 1 {
		t.Errorf("upstream called %d times, want 1 (cache should be used)", callCount)
	}
}

func TestCatalog_EmptyResponsePreservesCache(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			// First call returns valid data
			_, _ = w.Write([]byte(`{
				"data": [
					{"id": "real-model", "object": "model", "owned_by": "test"}
				]
			}`))
		} else {
			// Subsequent calls return empty
			_, _ = w.Write([]byte(`{"data": []}`))
		}
	}))
	defer ts.Close()

	// Use a short TTL so the cache expires quickly
	catalog := NewCatalog(ts.URL, "test-key", 1*time.Millisecond)

	// First fetch populates the cache
	err := catalog.Fetch()
	if err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}

	// Wait for cache to expire
	time.Sleep(5 * time.Millisecond)

	// GetModels triggers a refresh, but upstream returns empty
	models := catalog.GetModels(false)

	// The original cache should be preserved
	if len(models) != 1 {
		t.Fatalf("expected cache to be preserved with 1 model, got %d", len(models))
	}
	if models[0].ID != "real-model" {
		t.Errorf("model.ID = %q, want real-model", models[0].ID)
	}
}

func TestCatalog_FetchError(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			// First call succeeds
			_, _ = w.Write([]byte(`{
				"data": [
					{"id": "good-model", "object": "model", "owned_by": "test"}
				]
			}`))
		} else {
			// Subsequent calls return an error
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"internal error"}`))
		}
	}))
	defer ts.Close()

	catalog := NewCatalog(ts.URL, "test-key", 1*time.Millisecond)

	// First fetch populates the cache
	err := catalog.Fetch()
	if err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}

	// Wait for cache to expire
	time.Sleep(5 * time.Millisecond)

	// GetModels triggers a refresh that fails, but cache should be preserved
	models := catalog.GetModels(false)

	if len(models) != 1 {
		t.Fatalf("expected cache to be preserved with 1 model, got %d", len(models))
	}
	if models[0].ID != "good-model" {
		t.Errorf("model.ID = %q, want good-model", models[0].ID)
	}
}

func TestFilteredModels(t *testing.T) {
	allModels := []ModelEntry{
		{ID: "big-pickle", Object: "model", OwnedBy: "opencode"},
		{ID: "deepseek-v4-flash-free", Object: "model", OwnedBy: "opencode"},
		{ID: "paid-premium-model", Object: "model", OwnedBy: "openai"},
		{ID: "another-paid-model", Object: "model", OwnedBy: "openai"},
		{ID: "hy3-free", Object: "model", OwnedBy: "opencode"},
		{ID: "standard-model", Object: "model", OwnedBy: "opencode"},
	}

	filtered := FilteredModels(allModels)

	// Should include big-pickle and all -free models, exclude paid ones
	expectedIDs := map[string]bool{
		"big-pickle":             true,
		"deepseek-v4-flash-free": true,
		"hy3-free":               true,
	}

	if len(filtered) != 3 {
		t.Fatalf("filtered = %d models, want 3", len(filtered))
	}

	for _, m := range filtered {
		if !expectedIDs[m.ID] {
			t.Errorf("unexpected model %q in filtered list", m.ID)
		}
	}
}

func TestFilteredModels_Deduplication(t *testing.T) {
	allModels := []ModelEntry{
		{ID: "big-pickle", Object: "model", OwnedBy: "opencode"},
		{ID: "big-pickle", Object: "model", OwnedBy: "opencode"}, // duplicate
		{ID: "fast-model-free", Object: "model", OwnedBy: "opencode"},
	}

	filtered := FilteredModels(allModels)

	if len(filtered) != 2 {
		t.Fatalf("filtered = %d models, want 2 (deduplication)", len(filtered))
	}
}

func TestCatalog_AuthHeader(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data": [{"id": "test-model", "object": "model"}]}`))
	}))
	defer ts.Close()

	catalog := NewCatalog(ts.URL, "my-secret-key", 5*time.Minute)
	_ = catalog.Fetch()

	if gotAuth != "Bearer my-secret-key" {
		t.Errorf("Authorization = %q, want 'Bearer my-secret-key'", gotAuth)
	}
}

func TestCatalog_NoAuthWhenEmpty(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data": []}`))
	}))
	defer ts.Close()

	catalog := NewCatalog(ts.URL, "", 5*time.Minute)
	_ = catalog.Fetch()

	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty (no auth)", gotAuth)
	}
}

func TestToResponse(t *testing.T) {
	models := []ModelEntry{
		{ID: "m1", Object: "model", OwnedBy: "org1"},
		{ID: "m2", Object: "model", OwnedBy: "org2"},
	}

	resp := ToResponse(models, "http://example.com")

	obj, ok := resp["object"].(string)
	if !ok || obj != "list" {
		t.Errorf("object = %v, want list", resp["object"])
	}

	data, ok := resp["data"].([]interface{})
	if !ok {
		t.Fatal("data is not an array")
	}
	if len(data) != 2 {
		t.Errorf("data length = %d, want 2", len(data))
	}

	// Verify the structure can be marshaled to JSON
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}
	if len(b) == 0 {
		t.Error("marshaled response is empty")
	}
}
