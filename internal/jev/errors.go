package jev

import (
	"encoding/json"
	"errors"
	"regexp"
)

// Stable failure codes for agents and scripts. Add to this list; never rename.
const (
	CodeNotConfigured   = "jev_not_configured"
	CodeInvalidRequest  = "jev_invalid_request"
	CodeUnauthorized    = "jev_unauthorized"
	CodeRateLimited     = "jev_rate_limited"
	CodeOverloaded      = "jev_overloaded"
	CodeServerError     = "jev_server_error"
	CodeContextExceeded = "jev_context_exceeded"
	CodeRejected        = "jev_request_rejected"
	CodeTimeout         = "jev_timeout"
	CodeConnection      = "jev_connection_failed"
	CodeInvalidResponse = "jev_invalid_response"
	CodeUnclassified    = "jev_evaluation_failed"
)

// Error is a classified Jev failure. Messages are safe to show to a calling
// agent: they never contain API response bodies, request state, or credentials.
// ProviderType is TypeSafe's machine-readable error_type when it has a safe form.
type Error struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	Status       int    `json:"status,omitempty"`
	ProviderType string `json:"provider_error_type,omitempty"`
	Retryable    bool   `json:"retryable,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// CodeOf returns the stable code of a classified Jev failure, or "".
func CodeOf(err error) string {
	var classified *Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

var safeProviderType = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// providerErrorType extracts only detail.error_type from an error response.
// Free-form detail strings and messages may echo request content and are ignored.
func providerErrorType(body []byte) string {
	var wire struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &wire) != nil || len(wire.Detail) == 0 || wire.Detail[0] != '{' {
		return ""
	}
	var detail struct {
		ErrorType string `json:"error_type"`
	}
	if json.Unmarshal(wire.Detail, &detail) != nil || !safeProviderType.MatchString(detail.ErrorType) {
		return ""
	}
	return detail.ErrorType
}
