package commands

import (
	"encoding/json"
	"errors"

	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/output"

	"github.com/spf13/cobra"
)

func emitJev(cmd *cobra.Command, command string, data any) error {
	if useEnvelope() {
		data = output.WrapData(command, "jev", data)
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(data)
}

// reportJevError writes a Jev, selection, or browser failure once, with a
// stable code and next steps. JSON callers receive the error object on stdout,
// like other commands; text callers receive it on stderr.
func reportJevError(cmd *cobra.Command, err error) error {
	ae := jevAgentError(cmd.CommandPath(), err)
	if getOutputMode() == "json" {
		return output.EmitAgentError(cmd.OutOrStdout(), ae, true)
	}
	return output.EmitAgentError(cmd.ErrOrStderr(), ae, false)
}

// jevAgentError keeps the classified cause: a Jev failure code from this
// process, the code a host relayed for its own Jev failure, or a browser code.
func jevAgentError(command string, err error) output.AgentError {
	var existing output.AgentError
	if errors.As(err, &existing) {
		if existing.Command == "" {
			existing.Command = command
		}
		return existing
	}
	code, message := jev.CodeOf(err), err.Error()
	var rpc *bridge.RPCError
	if code == "" && errors.As(err, &rpc) && rpc.Code != "" {
		code, message = rpc.Code, rpc.Message
	}
	if code == "" {
		code = output.ErrCodeCommandFailed
	}
	return output.NewAgentError(code, command, message, jevSuggestions(code)...)
}

func jevSuggestions(code string) []string {
	switch code {
	case jev.CodeNotConfigured:
		return []string{"rep jev config --env-file /absolute/path/to/.env", "rep jev status -j"}
	case jev.CodeUnauthorized:
		return []string{"rep jev status -j", "rep jev doctor -j"}
	case jev.CodeRateLimited, jev.CodeOverloaded, jev.CodeServerError, jev.CodeTimeout, jev.CodeConnection, jev.CodeUnclassified:
		return []string{"retry after a short wait; the lookup did not act on the page", "rep jev doctor -j"}
	case jev.CodeContextExceeded, jev.CodeInvalidRequest:
		return []string{"narrow the lookup with --frame and --root-node, or lower --limit", "rep browser observe --tab ID --kind KIND  # inspect candidates without a model call"}
	case jev.CodeInvalidResponse:
		return []string{"retry; if it repeats, pin JEV_MODEL (for example jev-1.13.0) and keep this message", "rep jev doctor -j"}
	case output.ErrCodeBrowserUnavailable:
		return []string{"rep browser status", "rep browser tabs"}
	}
	return nil
}
