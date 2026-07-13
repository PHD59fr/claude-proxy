package anthropic

import (
	"encoding/json"
	"net/http"
	"strconv"
)

type APIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type ErrorResponse struct {
	Type       string   `json:"type"`
	Error      APIError `json:"error"`
	RetryAfter *int     `json:"retry_after,omitempty"`
}

func NewErrorResponse(statusCode int, errType, message string) ([]byte, int) {
	resp := ErrorResponse{
		Type: "error",
		Error: APIError{
			Type:    errType,
			Message: message,
		},
	}
	body, _ := json.Marshal(resp)
	return body, statusCode
}

func NewRateLimitResponse(statusCode int, message string, retryAfter *int) ([]byte, int) {
	resp := ErrorResponse{
		Type: "error",
		Error: APIError{
			Type:    ErrRateLimit,
			Message: message,
		},
		RetryAfter: retryAfter,
	}
	body, _ := json.Marshal(resp)
	return body, statusCode
}

func ParseRetryAfter(headers http.Header) *int {
	v := headers.Get("Retry-After")
	if v == "" {
		return nil
	}
	seconds, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &seconds
}

var (
	ErrInvalidRequest   = "invalid_request_error"
	ErrAuthentication   = "authentication_error"
	ErrPermissionDenied = "permission_error"
	ErrNotFound         = "not_found_error"
	ErrRateLimit        = "rate_limit_error"
	ErrInternalError    = "api_error"
	ErrOverloaded       = "overloaded_error"
)

func UpstreamErrorToAnthropic(statusCode int, upstreamBody string) ([]byte, int) {
	// Try to parse upstream error
	var oaiErr struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(upstreamBody), &oaiErr); err == nil && oaiErr.Error.Message != "" {
		msg := oaiErr.Error.Message
		var errType string
		switch statusCode {
		case 400:
			errType = ErrInvalidRequest
		case 401:
			errType = ErrAuthentication
		case 403:
			errType = ErrPermissionDenied
		case 404:
			errType = ErrNotFound
		case 429:
			errType = ErrRateLimit
		case 500:
			errType = ErrInternalError
		case 502, 503, 504:
			errType = ErrOverloaded
		default:
			errType = ErrInternalError
		}
		return NewErrorResponse(statusCode, errType, msg)
	}
	// Fallback
	errType := ErrInternalError
	if statusCode == 429 {
		errType = ErrRateLimit
	} else if statusCode == 401 {
		errType = ErrAuthentication
	} else if statusCode == 403 {
		errType = ErrPermissionDenied
	} else if statusCode == 502 || statusCode == 503 || statusCode == 504 {
		errType = ErrOverloaded
	} else if statusCode >= 500 {
		errType = ErrInternalError
	}
	msg := http.StatusText(statusCode)
	if msg == "" {
		msg = "upstream error"
	}
	return NewErrorResponse(statusCode, errType, msg)
}
