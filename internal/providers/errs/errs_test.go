package errs

import (
	"testing"
)

func TestIsModelUnavailable(t *testing.T) {
	available := []string{
		`{"error":{"message":"Invalid request","type":"invalid_request_error"}}`,
		`{"error":{"message":"Rate limit exceeded","type":"rate_limit_error"}}`,
		`{"error":{"message":"internal error","type":"server_error"}}`,
	}
	for _, body := range available {
		if IsModelUnavailable([]byte(body)) {
			t.Errorf("unexpected match: %s", body)
		}
	}

	unavailable := []string{
		`{"error":{"code":"model_not_available"}}`,
		`{"error":{"code":"model_not_found"}}`,
		`{"error":{"message":"Upstream request failed: Model is unavailable.","type":"server_error"}}`,
		`{"error":{"message":"Invalid request: model unavailable","code":"model_not_available"}}`,
		`{"error":{"message":"The model foo does not exist."}}`,
		`model_unavailable`,
		`Invalid request: model not available`,
	}
	for _, body := range unavailable {
		if !IsModelUnavailable([]byte(body)) {
			t.Errorf("expected match: %s", body)
		}
	}
}

func TestIsGlobalRateLimit(t *testing.T) {
	// FreeUsageLimitError is PER MODEL, not global
	if IsGlobalRateLimit([]byte(`{"error":{"type":"FreeUsageLimitError","message":"Rate limit exceeded."}}`)) {
		t.Fatal("FreeUsageLimitError must remain model-scoped, not global")
	}
	// Only Codex usage_limit_reached is truly account-wide
	if !IsGlobalRateLimit([]byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`)) {
		t.Fatal("expected usage_limit_reached to be provider-wide")
	}
	if IsGlobalRateLimit([]byte(`{"error":{"type":"rate_limit_error","message":"model busy"}}`)) {
		t.Fatal("a generic model rate limit must remain model-scoped")
	}
}
