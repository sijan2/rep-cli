package cmd

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// The visible flag surface is a budget, not an accident. Advanced tuning flags
// stay parseable but hidden (see advancedFlags) and documented by `describe`.
// A command may exceed the default budget only when listed here with a reason,
// so growth has to be argued for in a review instead of accumulating silently.
const visibleFlagBudget = 10

var visibleFlagExceptions = map[string]int{
	"rep list":    14, // selection presets and filters; consolidation tracked in descriptions/cli.md
	"rep body":    12, // byte/pointer/format views of one body
	"rep stream":  11, // stream record views
	"rep android": 11, // device-shared filters
}

func visibleLocalFlags(command *cobra.Command) []string {
	names := []string{}
	command.LocalFlags().VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden || flag.Name == "help" {
			return
		}
		names = append(names, flag.Name)
	})
	sort.Strings(names)
	return names
}

func TestVisibleFlagSurfaceStaysWithinBudget(t *testing.T) {
	configureCommandGroups()
	var violations []string
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		if command.Hidden {
			return
		}
		names := visibleLocalFlags(command)
		budget := visibleFlagBudget
		if exception, ok := visibleFlagExceptions[command.CommandPath()]; ok {
			budget = exception
		}
		if len(names) > budget {
			violations = append(violations, fmt.Sprintf("%s has %d visible flags (budget %d): %s", command.CommandPath(), len(names), budget, strings.Join(names, ",")))
		}
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(rootCmd)
	if len(violations) > 0 {
		t.Fatalf("visible flag budget exceeded; hide tuning flags with advancedFlags and document them in descriptions, or justify an exception:\n%s", strings.Join(violations, "\n"))
	}
}

func TestNewFastCommandsKeepMinimalSurface(t *testing.T) {
	for _, path := range []string{"rep browser locate", "rep browser capture fanout", "rep browser page", "rep browser do"} {
		command, _, err := rootCmd.Find(strings.Fields(path)[1:])
		if err != nil || command == nil {
			t.Fatalf("%s not registered: %v", path, err)
		}
		if names := visibleLocalFlags(command); len(names) > 6 {
			t.Fatalf("%s exposes %d flags; fast commands stay at or below 6: %v", path, len(names), names)
		}
	}
}
