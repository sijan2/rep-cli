package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/repplus/rep-cli/internal/headless"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

func headlessTaskDir() (string, error) {
	selected, err := scope.Current()
	if err != nil {
		return "", err
	}
	if !selected.Scoped || selected.TaskSource == "default" {
		return "", fmt.Errorf("headless browser requires --workspace and --task (or a project binding with REP_TASK)")
	}
	return selected.DataDir, nil
}

func init() {
	parent := &cobra.Command{Use: "headless", Short: "Own a persistent full Chromium browser in this task's private profile",
		Long: "Start, inspect, or stop this task's isolated Chrome for Testing / Chromium profile.\nUses full new headless mode with the Rep extension and existing capture/Jev APIs.\nUse start --headed for manual login in the same private profile, then stop and start\nwithout --headed to resume headless. Cookies and logins are independent of your\nnormal browser. No browser download is performed.\nUse --browser headless on subsequent browser commands and Jev select."}
	options := headless.Options{}
	timeout := 20 * time.Second
	start := &cobra.Command{Use: "start", Short: "Start or reuse this task's headless browser", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := headlessTaskDir()
			if err != nil {
				return emitHeadlessError(cmd, err)
			}
			if timeout < time.Second || timeout > time.Minute {
				return emitHeadlessError(cmd, fmt.Errorf("timeout must be between 1s and 1m"))
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			status, err := headless.Start(ctx, dir, options)
			if err != nil {
				return emitHeadlessError(cmd, err)
			}
			return emitBrowserResult(status, func() { printBrowserJSON(status) })
		}}
	start.Flags().StringVar(&options.Binary, "binary", "", "Full Chrome for Testing or Chromium executable (or REP_HEADLESS_BINARY)")
	start.Flags().StringVar(&options.Extension, "extension", "", "Rep extension source directory (or REP_EXTENSION_PATH)")
	start.Flags().StringVar(&options.Host, "host", "", "Native host executable (default rep-host next to rep)")
	start.Flags().BoolVar(&options.Headed, "headed", false, "Open this task's isolated browser window for manual login; default is headless")
	start.Flags().DurationVar(&timeout, "timeout", 20*time.Second, "Startup deadline")
	parent.AddCommand(start)
	for _, operation := range []string{"status", "stop"} {
		operation := operation
		parent.AddCommand(&cobra.Command{Use: operation, Short: operation + " this task's headless browser", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				dir, err := headlessTaskDir()
				if err != nil {
					return emitHeadlessError(cmd, err)
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
				defer cancel()
				var status headless.Status
				if operation == "stop" {
					status, err = headless.Stop(ctx, dir)
				} else {
					status, err = headless.GetStatus(ctx, dir)
				}
				if err != nil {
					return emitHeadlessError(cmd, err)
				}
				return emitBrowserResult(status, func() { printBrowserJSON(status) })
			}})
	}
	browserCmd.AddCommand(parent)
}

func emitHeadlessError(cmd *cobra.Command, err error) error {
	return output.EmitAgentError(cmd.OutOrStdout(), output.NewAgentError("headless_unavailable", cmd.CommandPath(), err.Error(), "rep scope", "rep browser headless start --help"), getOutputMode() == "json")
}
