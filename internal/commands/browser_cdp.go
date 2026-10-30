package commands

import (
	"time"

	"github.com/spf13/cobra"
)

func newBrowserTargetCommand(name, short string, timeout time.Duration, run browserCommandRunner) *cobra.Command {
	flags := browserDebuggeeFlags{}
	command := &cobra.Command{Use: name, Short: short, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		params, err := debuggeeParams(flags)
		if err != nil {
			return emitBrowserArgumentError("browser "+name, err)
		}
		return run(cmd, "browser "+name, "browser."+name, params, timeout)
	}}
	bindDebuggeeFlags(command, &flags)
	return command
}
func newBrowserCDPCommand(run browserCommandRunner) *cobra.Command {
	flags := browserDebuggeeFlags{}
	var raw string
	var keep bool
	var timeout time.Duration
	command := &cobra.Command{Use: "cdp <Domain.command>", Short: "Send a raw Chrome DevTools Protocol command", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		params, err := debuggeeParams(flags)
		if err != nil {
			return emitBrowserArgumentError("browser cdp", err)
		}
		values, err := readJSONObject(raw)
		if err != nil {
			return emitBrowserArgumentError("browser cdp", err)
		}
		params["method"], params["command_params"], params["keep_attached"] = args[0], values, keep
		return run(cmd, "browser cdp", "browser.cdp", params, timeout)
	}}
	bindDebuggeeFlags(command, &flags)
	command.Flags().StringVar(&raw, "params", "{}", "CDP parameter JSON or @path")
	command.Flags().BoolVar(&keep, "keep-attached", false, "Keep the target attached across low-latency commands")
	command.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Maximum RPC duration")
	return command
}

func init() {
	browserCmd.AddCommand(
		&cobra.Command{Use: "targets", Short: "List Chromium debugger targets", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			return callBrowserRPC(cmd, "browser targets", "browser.targets", nil, 5*time.Second)
		}},
		newBrowserTargetCommand("attach", "Keep a debugger target attached", 10*time.Second, callBrowserRPC),
		newBrowserTargetCommand("detach", "Release a persistent debugger attachment", 10*time.Second, callBrowserRPC),
		newBrowserTargetCommand("probe", "Inspect browser identity and page lifecycle state", 15*time.Second, callBrowserRPC),
		newBrowserCDPCommand(callBrowserRPC), newBrowserEvaluationCommand(false, callBrowserRPC),
	)
}
