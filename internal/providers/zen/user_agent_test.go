package zen

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientSendsAppUserAgent(t *testing.T) {
	var got string
	if !strings.Contains(defaultUserAgent, "opencode") {
		t.Fatalf("defaultUserAgent %q must contain %q so Zen's free tier recognizes it", defaultUserAgent, "opencode")
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := NewClient("zen", ts.URL, "test-key", time.Second)
	ctx := t.Context()
	if err := client.CheckChatCompletion(ctx, "big-pickle"); err != nil {
		t.Fatalf("chat request failed: %v", err)
	}
	if got != defaultUserAgent {
		t.Errorf("User-Agent = %q, want %q", got, defaultUserAgent)
	}
	if !strings.Contains(got, "opencode") {
		t.Errorf("User-Agent %q does not contain opencode; Zen free tier will 429 us", got)
	}
}

func TestUserAgentTransportRespectsExplicitHeader(t *testing.T) {
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	tr := &userAgentTransport{base: http.DefaultTransport, ua: defaultUserAgent}
	req, err := http.NewRequest("GET", ts.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "custom-agent")
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got != "custom-agent" {
		t.Errorf("User-Agent = %q, want custom-agent (explicit header wins)", got)
	}
}
