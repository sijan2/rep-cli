package commands

import (
	"context"

	"errors"

	"time"

	"github.com/spf13/cobra"
)

func newBrowserRuntimeCommand(deps jevDOMDependencies) *cobra.Command {
	command := newInheritedBrowserRuntimeCommand(deps)
	addDecisionBrowserFlag(command)
	return command
}

func newInheritedBrowserRuntimeCommand(deps jevDOMDependencies) *cobra.Command {
	command := &cobra.Command{Use: "runtime", Short: "Inspect persistent decision concurrency and reuse counters", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		name := decisionBrowserName(command)
		if !validDecisionBrowser(name) {
			return reportJevError(command, errors.New("unsupported browser"))
		}
		ctx, cancel := context.WithTimeout(command.Context(), 5*time.Second)
		defer cancel()
		browser, err := deps.browser(ctx, name)
		if err != nil {
			return reportJevError(command, err)
		}
		var status map[string]any
		if err := browser.Call(ctx, "jev.runtime", nil, &status); err != nil {
			return reportJevError(command, err)
		}
		return emitJev(command, command.CommandPath(), status)
	}}
	return command
}

func init() { browserCmd.AddCommand(newInheritedBrowserRuntimeCommand(defaultJevDOMDependencies())) }
