package commands

import (
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

func newCoreScopeCommand() *cobra.Command {
	return &cobra.Command{Use: "scope", Short: "Show offline task, browser, storage and journal", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			selected, err := scope.Current()
			if err != nil {
				return coreFailure(cmd, err)
			}
			inventory, err := readCoreScope(selected)
			if err != nil {
				return coreFailure(cmd, err)
			}
			return output.WriteJSON(cmd.OutOrStdout(), struct {
				Task       string `json:"task"`
				TaskSource string `json:"task_source"`
				coreScopeInventory
			}{selected.Identity(), selected.TaskSource, inventory})
		},
	}
}

func init() { rootCmd.AddCommand(newCoreScopeCommand()) }
