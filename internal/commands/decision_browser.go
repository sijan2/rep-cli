package commands

import (
	"github.com/spf13/cobra"
)

func addDecisionBrowserFlag(command *cobra.Command) {
	command.Flags().String("browser", defaultBrowser, "Browser bridge: dia, arc, chrome, any, or task-owned headless")
}

func decisionBrowserName(command *cobra.Command) string {
	name, _ := command.Flags().GetString("browser")
	return name
}
