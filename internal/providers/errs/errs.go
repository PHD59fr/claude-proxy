// Package errs carries the sentinel errors shared between the request path,
// the Provider implementations and this package. It is a leaf package so
// provider implementations (zen, codex) can return these values without
// importing the providers package (which would create an import cycle through
// the registry).
package errs

import (
	"errors"
	"strings"
)

// Sentinel errors returned by Provider methods so the request path can tell a
// broken client request from an upstream failure.
var (
	// ErrInvalidRequest marks a payload conversion failure: the client (or
	// model) request could not be encoded for the provider.
	ErrInvalidRequest = errors.New("invalid request for provider")

	// ErrMarshal marks a payload serialization failure.
	ErrMarshal = errors.New("failed to marshal provider request")

	// ErrNoContent marks a provider response carrying no usable output (e.g.
	// an empty Codex SSE stream). The fallback loop treats it as "try the next
	// model" instead of a hard error.
	ErrNoContent = errors.New("no usable content in provider response")

	// ErrFailed marks a provider response that reported a failed/incomplete
	// status. The fallback loop disables the model briefly and tries the next
	// one.
	ErrFailed = errors.New("provider response reported a failure")

	// ErrModelUnavailable marks an upstream answer indicating the model no
	// longer exists on the provider (400 "Model is unavailable",
	// model_not_available, model_not_found). Such a model cannot be retried
	// until it reappears upstream: the proxy skips it and hides it.
	ErrModelUnavailable = errors.New("model is unavailable")

	// ErrRateLimited marks an upstream 429 response during a health check.
	// The model should be pre-disabled for the circuit breaker cooldown.
	ErrRateLimited = errors.New("model rate limited during health check")

	// ErrGlobalRateLimited marks an upstream 429 response indicating the
	// entire upstream's free quota is exhausted. The upstream should be
	// disabled globally for the short cooldown.
	ErrGlobalRateLimited = errors.New("upstream globally rate limited during health check")
)

// IsModelUnavailable reports whether an upstream error body means the model
// does not exist on the upstream (as opposed to a transient 4xx such as an
// invalid request). Markers are the OpenAI/Zen and Codex phrasing seen in the
// wild; matching is deliberately conservative to avoid false positives.
func IsModelUnavailable(body []byte) bool {
	s := strings.ToLower(string(body))
	for _, marker := range []string{
		"model is unavailable",
		"model not available",
		"model_unavailable",
		"model_not_available",
		"model_not_found",
		"does not exist",
		"was not found",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// IsGlobalRateLimit reports whether the provider rejected the request because
// the account/upstream free quota is exhausted. Retrying another model on the
// same upstream cannot help and only creates a fallback cascade.
//
// Note: Zen's FreeUsageLimitError is PER MODEL, not global. Only Codex's
// usage_limit_reached is truly account-wide.
func IsGlobalRateLimit(body []byte) bool {
	return strings.Contains(strings.ToLower(string(body)), "usage_limit_reached")
}
