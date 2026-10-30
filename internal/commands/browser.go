package commands

import (
	"context"
	"encoding/json"

	"fmt"
	"os"

	"strings"

	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/output"

	"github.com/spf13/cobra"
)

const defaultBrowser = "dia"

var (
	browserSelector string
	closeTabID      int
)

var browserCmd = &cobra.Command{
	Use:   "browser start [arc|chrome|dia] | stop",
	Short: "Manage browser choice",
	Args:  cobra.NoArgs,
	RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
}

func init() { rootCmd.AddCommand(browserCmd) }

func selectBrowserBridge(ctx context.Context, selector, command string) (*bridge.Client, error) {
	client, err := connectBrowser(ctx, selector)
	if err != nil && strings.EqualFold(strings.TrimSpace(selector), "headless") {
		return nil, output.EmitAgentError(os.Stdout, output.NewAgentError("headless_unavailable", command, err.Error(), "rep browser start"), getOutputMode() == "json")
	}
	if err == nil {
		return client, nil
	}
	return nil, output.EmitAgentError(os.Stdout, output.NewAgentError(
		output.ErrCodeBrowserUnavailable,
		command,
		err.Error(),
		"reload rep+ in dia://extensions or the selected browser's extension manager",
		"verify com.repplus.host.json points to the installed rep-host",
		"rep tabs",
	), getOutputMode() == "json")
}

func emitBrowserCallError(command string, err error) error {
	return emitBrowserCallErrorWithPrivacy(command, err, false)
}

func emitBrowserCallErrorWithPrivacy(command string, err error, redact bool) error {
	code := output.ErrCodeBrowserRPC
	var rpcError *bridge.RPCError
	if errorsAs(err, &rpcError) && rpcError.Code != "" {
		code = rpcError.Code
	}
	if redact {
		return output.EmitAgentError(os.Stdout, output.NewAgentError(
			code,
			command,
			"request for a URL loaded from a private file failed; sensitive details were omitted",
			"rep tabs",
			"rep tabs",
		), getOutputMode() == "json")
	}
	return output.EmitAgentError(os.Stdout, output.WrapError(err, code, command, "rep tabs", "rep tabs"), getOutputMode() == "json")
}

func emitBrowserResult(value interface{}, printText func()) error {
	if getOutputMode() == "json" {
		data, err := output.Marshal(output.Result(value))
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	printText()
	return nil
}

// Avoid importing errors only for one type assertion helper; this also works
// for wrapped RPC errors.
func errorsAs(err error, target interface{}) bool {
	switch typed := target.(type) {
	case **bridge.RPCError:
		for err != nil {
			if value, ok := err.(*bridge.RPCError); ok {
				*typed = value
				return true
			}
			type unwrapper interface{ Unwrap() error }
			wrapped, ok := err.(unwrapper)
			if !ok {
				break
			}
			err = wrapped.Unwrap()
		}
	}
	return false
}

var _ = json.RawMessage{}
