package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// labCmd groups the specialized tools: forensic capture, mobile, research,
// vision and identity experiments. They share the rep binary and its task and
// browser context; only the root contract stays compact.
var labCmd = &cobra.Command{
	Use:   "lab COMMAND",
	Short: "Specialized forensic, mobile, research and vision tools",
	RunE: func(command *cobra.Command, args []string) error {
		if len(args) == 0 {
			return command.Help()
		}
		return coreFailure(command, coreArgument("unknown lab command "+args[0]+"; see rep lab --help"))
	},
}

func init() { rootCmd.AddCommand(labCmd) }

func inLabTree(command *cobra.Command) bool {
	for current := command; current != nil; current = current.Parent() {
		if current == labCmd {
			return true
		}
	}
	return false
}

// labHelp keeps the full Cobra discovery tree for specialized tools, which are
// not bound by the compact verb contract.
func labHelp(command *cobra.Command) string {
	var out strings.Builder
	if command == labCmd {
		out.WriteString("Specialized forensic, mobile, research and vision tools. Task and browser context work as in core verbs.\n")
	} else if command.Long != "" {
		out.WriteString(command.Long + "\n")
	} else {
		out.WriteString(command.Short + "\n")
	}
	fmt.Fprintf(&out, "\nUsage:\n  %s\n", command.UseLine())
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
