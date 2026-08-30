package proxy

import (
	"testing"
	"time"

	"github.com/claude-code-opencode/claude-proxy/internal/config"
)

func TestDisableModelCooldownByReason(t *testing.T) {
	handler, _ := newTestHandler("http://127.0.0.1:1")

	handler.DisableModelUpstream("zen", "big-pickle", "rate_limit")
	handler.disabledMu.Lock()
	info := handler.disabledUntil["zen/big-pickle"]
	handler.disabledMu.Unlock()
	duration := time.Until(info.until)
	if duration > rateLimitCooldown+time.Second {
		t.Errorf("rate_limit cooldown = %v, want <= %v (bursty free quota must retry fast)", duration, rateLimitCooldown)
	}

	handler.DisableModelUpstream("zen", "big-pickle", "error")
	handler.disabledMu.Lock()
	info = handler.disabledUntil["zen/big-pickle"]
	handler.disabledMu.Unlock()
	duration = time.Until(info.until)
	if duration < time.Minute {
		t.Errorf("transport cooldown = %v, want >= 1m", duration)
	}
	if duration > circuitBreakerCooldown+time.Second {
		t.Errorf("transport cooldown = %v, want <= %v", duration, circuitBreakerCooldown)
	}
	if info.reason != "error" {
		t.Errorf("reason = %q, want error", info.reason)
	}
}

func TestEscalatedRateLimitUsesLongCooldown(t *testing.T) {
	handler, _ := newTestHandler("http://127.0.0.1:1")

	handler.DisableModelUpstream("zen", "big-pickle", "rate_limit_escalated")
	handler.disabledMu.Lock()
	info := handler.disabledUntil["zen/big-pickle"]
	handler.disabledMu.Unlock()

	duration := time.Until(info.until)
	if duration < circuitBreakerCooldown-time.Second {
		t.Errorf("escalated cooldown = %v, want ~ %v (the 2nd-429 blacklist must back off long)", duration, circuitBreakerCooldown)
	}
	if info.reason != "rate_limit_escalated" {
		t.Errorf("reason = %q, want rate_limit_escalated", info.reason)
	}
}

func TestAllModelsDisabledByRateLimitClassifiesEscalation(t *testing.T) {
	handler, cfg := newTestHandler("http://127.0.0.1:1")

	handler.DisableModelUpstream("zen", "big-pickle", "rate_limit_escalated")
	var specs []config.ModelSpec
	for _, m := range cfg.Models {
		specs = append(specs, config.ModelSpec{Name: m.Name, Upstream: m.Upstream})
	}
	if !handler.allModelsDisabledByRateLimit(specs) {
		t.Error("expected allModelsDisabledByRateLimit to classify rate_limit_escalated as rate limiting")
	}
}

func TestClearRateLimitRetryResetsCounter(t *testing.T) {
	handler, _ := newTestHandler("http://127.0.0.1:1")

	modelKey := "zen/big-pickle"
	handler.disabledMu.Lock()
	handler.rateLimitRetries[modelKey] = 5
	handler.disabledMu.Unlock()

	handler.clearRateLimitRetry(modelKey)

	handler.disabledMu.Lock()
	_, ok := handler.rateLimitRetries[modelKey]
	handler.disabledMu.Unlock()
	if ok {
		t.Error("expected rateLimitRetries entry to be cleared after a successful response")
	}
}

func TestDisableModelDoesNotClobberLongerDisable(t *testing.T) {
	handler, _ := newTestHandler("http://127.0.0.1:1")

	// A transport error disables for the long cooldown.
	handler.DisableModelUpstream("zen", "big-pickle", "error")
	// A later single 429 (short cooldown) must NOT shorten it.
	handler.DisableModelUpstream("zen", "big-pickle", "rate_limit")

	handler.disabledMu.Lock()
	info := handler.disabledUntil["zen/big-pickle"]
	handler.disabledMu.Unlock()

	if info.reason != "error" {
		t.Errorf("reason = %q, want error (a short rate_limit must not clobber a longer transport disable)", info.reason)
	}
	if duration := time.Until(info.until); duration < time.Minute {
		t.Errorf("cooldown = %v, want to keep the longer transport disable", duration)
	}
}

func TestRateLimitedModelReenabledAfterShortCooldown(t *testing.T) {
	handler, _ := newTestHandler("http://127.0.0.1:1")

	handler.DisableModelUpstream("zen", "big-pickle", "rate_limit")
	if !handler.isModelUpstreamDisabled("zen", "big-pickle") {
		t.Fatal("expected model disabled right after rate_limit")
	}

	handler.disabledMu.Lock()
	handler.disabledUntil["zen/big-pickle"] = disableInfo{until: time.Now().Add(-time.Millisecond), reason: "rate_limit"}
	handler.disabledMu.Unlock()

	if handler.isModelUpstreamDisabled("zen", "big-pickle") {
		t.Error("expected model re-enabled after cooldown expired")
	}
}
