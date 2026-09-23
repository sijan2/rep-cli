package cmd

import (
	"fmt"
	"os"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var scopeCmd = &cobra.Command{
	Use: "scope", Short: "Show the workspace, task, and capture data paths for this invocation",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		selected, err := scope.Current()
		if err != nil {
			return err
		}
		selected.LivePath, err = store.GetLiveFilePath()
		if err != nil {
			return output.EmitAgentError(cmd.OutOrStdout(), output.NewAgentError(output.ErrCodeInvalidArgument, "scope", err.Error()), getOutputMode() == "json")
		}
		status := "not_captured"
		if _, err := os.Stat(selected.LivePath); err == nil {
			status = "available"
		} else if !os.IsNotExist(err) {
			status = "unreadable"
		}
		result := struct {
			scope.Scope
			CaptureStatus  string `json:"capture_status"`
			SharedBrowser  bool   `json:"shared_browser_profile"`
			NeedsSelection bool   `json:"needs_selection"`
		}{selected, status, true, (!selected.Scoped && !selected.ExplicitGlobal) || (selected.Scoped && selected.TaskSource == "default")}
		if getOutputMode() == "json" {
			data, _ := sonic.MarshalIndent(result, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(data))
		} else {
			if selected.Scoped {
				fmt.Fprintf(cmd.OutOrStdout(), "workspace: %s\ntask: %s\n", selected.Workspace, selected.Task)
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "workspace: shared legacy data")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "selection: %s\ndata: %s\ncapture: %s\n", selected.Source, selected.DataDir, status)
			if result.NeedsSelection {
				fmt.Fprintln(cmd.OutOrStdout(), "Select --workspace <project> --task <task> (or REP_TASK within a bound project), or use --global for legacy data.")
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Browser profiles and tabs remain shared; use a task-owned tab.")
		}
		return nil
	},
}

func init() { rootCmd.AddCommand(scopeCmd) }
