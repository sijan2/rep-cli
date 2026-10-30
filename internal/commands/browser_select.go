//go:build replab

package commands

import (
	"context"
	"errors"

	"github.com/repplus/rep-cli/internal/jevdom"

	"github.com/spf13/cobra"
)

func newElementSelectCommand(dependencies jevDOMDependencies, shared *elementFlags) *cobra.Command {
	options := jevdom.DefaultOptions()
	command := &cobra.Command{
		Use:   "select [goal] --tab ID",
		Short: "Find an observed page element with Jev; return a handle without acting",
		Long:  "Read named controls or text from the accessibility trees of a real tab, ask Jev for a typed choice, and recheck the snapshot. Only bounded role/name/context text is sent to Jev; URLs and node handles remain local. No clicks, page scripts, navigation, or form filling are executed.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			shared.apply(&options)
			browserName := decisionBrowserName(command)
			if len(args) == 1 {
				if command.Flags().Changed("goal") {
					return reportJevError(command, errors.New("provide a positional goal or --goal, not both"))
				}
				options.Goal = args[0]
			}
			validated, err := options.Validate()
			if err != nil {
				return reportJevError(command, err)
			}
			if !validDecisionBrowser(browserName) {
				return reportJevError(command, errors.New("browser must be dia, arc, chrome, any, or headless"))
			}
			config, err := dependencies.loadConfig()
			if err != nil {
				return reportJevError(command, err)
			}
			validated.Model = config.Model
			ctx, cancel := context.WithTimeout(command.Context(), jevdom.Timeout)
			defer cancel()
			browser, err := dependencies.browser(ctx, browserName)
			if err != nil {
				return reportJevError(command, err)
			}
			if dependencies.selectBatch != nil {
				validated.Owner, err = decisionOwner()
				if err != nil {
					return reportJevError(command, err)
				}
			}
			results, err := runSelections(ctx, dependencies, browser, config, []jevdom.Options{validated})
			if err != nil {
				return reportJevError(command, err)
			}
			result := results[0]
			commandName := "jev select"
			if command.Parent() != nil {
				switch command.Parent().Name() {
				case "browser":
					commandName = "browser select"
				case "element":
					commandName = "browser element select"
				}
			}
			return emitJev(command, commandName, result)
		},
	}
	shared = elementCommandFlags(command, shared)
	command.Flags().StringVar(&options.Goal, "goal", "", "The element to find, using 1 to 2000 bytes")
	command.Flags().IntVar(&options.Limit, "limit", 240, "Maximum candidates to consider (1 to 480)")
	command.Flags().Float64Var(&options.Confidence, "confidence", 0.8, "Minimum decision confidence (0 to 1)")
	command.Flags().BoolVar(&options.NoCache, "no-cache", false, "Bypass the local five-minute decision cache")
	command.Flags().StringVar(&options.Strategy, "strategy", "auto", "Decision packing: auto or legacy (comparison baseline)")
	command.Flags().StringVar(&options.ObservationMode, "observation", "auto", "Observation transport: auto or legacy (comparison baseline)")

	return command
}
