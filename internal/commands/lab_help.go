//go:build replab

package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// The experimental binary has a larger explicit surface than the core
// contract. Keep it discoverable even when the core prints its compact index.
func labHelp(command *cobra.Command) string {
	var out strings.Builder
	usage := "rep-lab" + strings.TrimPrefix(command.UseLine(), command.Root().Name())
	// Match Cobra's boundary: prose precedes Usage, so field/example headings
	// cannot be mistaken for generated command groups by inventory readers.
	if command.Parent() == nil {
		out.WriteString("Experimental forensic, mobile, research and vision tools. Core verbs are also available.\n")
	} else if command.Long != "" {
		out.WriteString(command.Long + "\n")
	} else {
		out.WriteString(command.Short + "\n")
	}
	fmt.Fprintf(&out, "\nUsage:\n  %s\n", usage)
	if command.HasAvailableSubCommands() {
		out.WriteString("\nCommands:\n")
		for _, child := range command.Commands() {
			if child.IsAvailableCommand() {
				fmt.Fprintf(&out, "  %s  %s\n", child.Name(), child.Short)
			}
		}
	}
	if command.LocalFlags().HasAvailableFlags() {
		out.WriteString("\nFlags:\n" + command.LocalFlags().FlagUsagesWrapped(100))
	}
	if command.InheritedFlags().HasAvailableFlags() {
		out.WriteString("\nGlobal Flags:\n" + command.InheritedFlags().FlagUsagesWrapped(100))
	}
	return out.String()
}

func configureBuildHelp(root *cobra.Command) {
	root.SetHelpFunc(func(command *cobra.Command, _ []string) {
		fmt.Fprint(command.OutOrStdout(), labHelp(command))
	})
	root.SetUsageFunc(func(command *cobra.Command) error {
		_, err := fmt.Fprint(command.OutOrStderr(), labHelp(command))
		return err
	})
}
