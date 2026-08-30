package codex

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestGeneratePKCE(t *testing.T) {
	pkce, err := GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	if pkce.Verifier == "" {
		t.Error("empty verifier")
	}
	if pkce.Challenge == "" {
		t.Error("empty challenge")
	}
	if pkce.Verifier == pkce.Challenge {
		t.Error("verifier and challenge should be different")
	}
}

func TestGenerateState(t *testing.T) {
	s1, err := GenerateState()
	if err != nil {
		t.Fatal(err)
	}
	s2, err := GenerateState()
	if err != nil {
		t.Fatal(err)
	}
	if s1 == s2 {
		t.Error("two states should be different")
	}
	if len(s1) != 32 {
		t.Errorf("state length = %d, want 32", len(s1))
	}
}

func TestBuildAuthorizeURL(t *testing.T) {
	pkce := PKCEPair{Verifier: "test-verifier", Challenge: "test-challenge"}
	state := "test-state"
	u := BuildAuthorizeURL(pkce, state)

	if !strings.Contains(u, "client_id=app_EMoamEEZ73f0CkXaXp7hrann") {
		t.Error("missing client_id")
	}
	if !strings.Contains(u, "code_challenge=test-challenge") {
		t.Error("missing code_challenge")
	}
	if !strings.Contains(u, "state=test-state") {
		t.Error("missing state")
	}
	if !strings.Contains(u, "code_challenge_method=S256") {
		t.Error("missing code_challenge_method")
	}
	if !strings.Contains(u, "originator=codex_cli_rs") {
		t.Error("missing originator")
	}
}

func TestExtractAccountID(t *testing.T) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct_123"}}`))
	token := header + "." + payload + ".fake-sig"

	accountID := ExtractAccountID(token)
	if accountID != "acct_123" {
		t.Errorf("accountID = %q, want acct_123", accountID)
	}
}

func TestExtractAccountID_Invalid(t *testing.T) {
	if id := ExtractAccountID("not-a-jwt"); id != "" {
		t.Errorf("expected empty, got %q", id)
	}
}

func TestShouldRefreshToken(t *testing.T) {
	if !ShouldRefreshToken(nil) {
		t.Error("nil tokens should need refresh")
	}

	expired := &OAuthTokens{AccessToken: "tok", ExpiresAt: time.Now().Add(-time.Hour).UnixMilli()}
	if !ShouldRefreshToken(expired) {
		t.Error("expired token should need refresh")
	}

	valid := &OAuthTokens{AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	if ShouldRefreshToken(valid) {
		t.Error("valid token should not need refresh")
	}

	nearExpiry := &OAuthTokens{AccessToken: "tok", ExpiresAt: time.Now().Add(3 * time.Minute).UnixMilli()}
	if !ShouldRefreshToken(nearExpiry) {
		t.Error("near-expiry token should need refresh")
	}
}

func TestOAuthServer_Timeout(t *testing.T) {
	s := StartOAuthServer("no-match-state")
	s.Timeout = 500 * time.Millisecond
	defer s.Close()

	_, err := s.WaitForCode()
	if err == nil {
		t.Error("expected timeout error")
	}
}
