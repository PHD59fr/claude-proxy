package proxy

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

type contextKey string

const (
	RequestIDKey   contextKey = "request_id"
	PassthroughKey contextKey = "passthrough_api_key"
)

func GetRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(RequestIDKey).(string); ok {
		return id
	}
	return ""
}

func generateRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "req_" + hex.EncodeToString(b)
}

// requestID adds a unique request ID to each request context.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := generateRequestID()
		ctx := context.WithValue(r.Context(), RequestIDKey, id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// auth returns middleware that checks inbound authentication.
func auth(apiKey string, passthrough bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractToken(r)
			if apiKey == "" && !passthrough {
				next.ServeHTTP(w, r)
				return
			}
			if passthrough {
				// Passthrough is an authentication mode: never fall back to a
				// configured upstream credential when the caller omitted its key.
				if token == "" {
					writeAuthenticationError(w)
					return
				}
				ctx := context.WithValue(r.Context(), PassthroughKey, token)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) != 1 {
				writeAuthenticationError(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeAuthenticationError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"Invalid or missing API key"}}`))
}

func extractToken(r *http.Request) string {
	// Check x-api-key header (Anthropic style)
	if tok := r.Header.Get("X-Api-Key"); tok != "" {
		return tok
	}
	// Check Authorization: Bearer <token>
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			return strings.TrimPrefix(auth, "Bearer ")
		}
	}
	return ""
}

// GetPassthroughKey retrieves the inbound API key from the request context (passthrough mode).
func GetPassthroughKey(r *http.Request) string {
	if key, ok := r.Context().Value(PassthroughKey).(string); ok {
		return key
	}
	return ""
}

// bodySizeLimit wraps a handler to limit request body size.
func bodySizeLimit(maxSize int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if maxSize > 0 {
				r.Body = http.MaxBytesReader(w, r.Body, maxSize)
			}
			next.ServeHTTP(w, r)
		})
	}
}
