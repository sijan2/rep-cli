package cmd

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

var workspaceCmd = &cobra.Command{
	Use: "workspace", Short: "Bind a project to an isolated data namespace",
}

var workspaceInitCmd = &cobra.Command{
	Use: "init <name>", Short: "Create .rep/workspace.json in the current project",
	Long: `Bind the current directory and its descendants to a named workspace.
Primary domains, notes, saved sessions and captures stay within the selected
workspace and task. Give each concurrent agent a different --task or REP_TASK.
This creates no global current-workspace setting and copies no legacy data.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := scope.InitProject("", args[0])
		if err != nil {
			return output.EmitAgentError(cmd.OutOrStdout(), output.NewAgentError(output.ErrCodeStoreWrite, "workspace init", err.Error(), "rep scope"), getOutputMode() == "json")
		}
		if getOutputMode() == "json" {
			data, _ := sonic.MarshalIndent(map[string]interface{}{"workspace": args[0], "binding_path": path, "task_required": true}, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(data))
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Bound project to workspace %s (%s).\nA distinct --task <name> or REP_TASK is required for each concurrent agent.\n", args[0], path)
		}
		return nil
	},
}

func init() {
	workspaceCmd.AddCommand(workspaceInitCmd)
	rootCmd.AddCommand(workspaceCmd)
}
