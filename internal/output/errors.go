package output

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/bytedance/sonic"
)

// AgentError is the structured error shape emitted in `--json` mode. Keeping
// a stable schema here (code / message / suggest / chain) so tool harnesses
// can pattern-match on `code` without brittle message parsing.
type AgentError struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Suggest []string `json:"suggest,omitempty"`
	Chain   []string `json:"chain,omitempty"`
	Command string   `json:"command,omitempty"`
}

// ErrorCode* are the stable machine identifiers agents may pattern-match on.
// Add to this list; never rename an existing one.
const (
	ErrCodeRequestNotFound    = "request_not_found"
	ErrCodeSessionNotFound    = "session_not_found"
	ErrCodeLiveUnavailable    = "live_unavailable"
	ErrCodeBrowserUnavailable = "browser_unavailable"
	ErrCodeBrowserRPC         = "browser_rpc_failed"
	ErrCodeInvalidArgument    = "invalid_argument"
	ErrCodeInvalidRegex       = "invalid_regex"
	ErrCodeStoreRead          = "store_read_failed"
	ErrCodeStoreWrite         = "store_write_failed"
	ErrCodeTransferFailed     = "transfer_failed"
	ErrCodeNotImplemented     = "not_implemented"
	ErrCodeInternal           = "internal_error"
)

// errorChain walks err.Unwrap() / errors.Unwrap() collecting the message
// chain, so agents see the full cause without brittle parsing of "caused by".
func errorChain(err error) []string {
	if err == nil {
		return nil
	}
	var chain []string
	cur := err
	for cur != nil {
		chain = append(chain, cur.Error())
		cur = errors.Unwrap(cur)
	}
	return chain
}

// NewAgentError constructs an AgentError with a code, user message, and any
// number of concrete "try this next" suggestions.
func NewAgentError(code, command, message string, suggest ...string) AgentError {
	return AgentError{
		Code:    code,
		Message: message,
		Command: command,
		Suggest: suggest,
	}
}

// WrapError promotes a Go error into an AgentError, preserving the unwrap
// chain. Prefer NewAgentError when you already know the code; use this for
// unexpected errors at the boundary.
func WrapError(err error, code, command string, suggest ...string) AgentError {
	if err == nil {
		return AgentError{Code: code, Command: command, Suggest: suggest}
	}
	return AgentError{
		Code:    code,
		Message: err.Error(),
		Command: command,
		Suggest: suggest,
		Chain:   errorChain(err),
	}
}

// Error implements the error interface so RunE can `return ae` directly
// and cobra will propagate a non-zero exit. Keep the string short; the
// full structure goes to stdout via EmitAgentError.
func (ae AgentError) Error() string { return ae.Message }

// EmitAgentError writes ae to w in the right shape for the current output
// format (JSON envelope vs reflection-style text) and returns ae itself so
// callers can `return output.EmitAgentError(...)` for non-zero exit.
func EmitAgentError(w io.Writer, ae AgentError, jsonMode bool) error {
	if jsonMode {
		payload := map[string]interface{}{"error": ae}
		b, _ := sonic.MarshalIndent(payload, "", "  ")
		fmt.Fprintln(w, string(b))
	} else {
		fmt.Fprint(w, FormatErrorText(ae))
	}
	return ae
}

// FormatErrorText renders the AgentError for plain-text output with a
// reflection trailer that nudges the agent toward diagnosis — matching
// forgecode's tool-error-reflection pattern.
func FormatErrorText(ae AgentError) string {
	var b strings.Builder
	fmt.Fprintf(&b, "error: %s\n", ae.Message)
	if ae.Code != "" {
		fmt.Fprintf(&b, "code: %s\n", ae.Code)
	}
	if ae.Command != "" {
		fmt.Fprintf(&b, "command: %s\n", ae.Command)
	}
	if len(ae.Chain) > 1 {
		b.WriteString("caused_by:\n")
		for i, c := range ae.Chain[1:] {
			fmt.Fprintf(&b, "  %d: %s\n", i, c)
		}
	}
	if len(ae.Suggest) > 0 {
		b.WriteString("suggest:\n")
		for _, s := range ae.Suggest {
			fmt.Fprintf(&b, "  %s\n", s)
		}
	}
	return b.String()
}
