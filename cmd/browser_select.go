package cmd

import (
	"context"
	"errors"

	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/spf13/cobra"
)

type jevDOMDependencies struct {
	loadConfig func() (jev.Config, error)
	browser    func(context.Context, string) (jevdom.Browser, error)
	evaluator  func(jev.Config) jevdom.Evaluator
	cache      func() *jevdom.Cache
}

func defaultJevDOMDependencies() jevDOMDependencies {
	return jevDOMDependencies{
		loadConfig: jev.LoadConfig,
		browser: func(ctx context.Context, name string) (jevdom.Browser, error) {
			client, err := connectBrowser(ctx, name)
			if err != nil {
				return nil, errors.New("cannot connect to the selected browser bridge")
			}
			return client, nil
		},
		evaluator: func(config jev.Config) jevdom.Evaluator { return jev.NewClient(config) },
		cache:     jevdom.DefaultCache,
	}
}

func newBrowserSelectCommand(dependencies jevDOMDependencies) *cobra.Command {
	options := jevdom.DefaultOptions()
	browserName := "arc"
	command := &cobra.Command{
		Use:   "select [goal] --tab ID",
		Short: "Find an observed page element with Jev; return a handle without acting",
		Long:  "Read named controls or text from the accessibility trees of a real tab, ask Jev for a typed choice, and recheck the snapshot. Only bounded role/name/context text is sent to Jev; URLs and node handles remain local. No clicks, page scripts, navigation, or form filling are executed.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
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
			if browserName != "arc" && browserName != "chrome" && browserName != "any" && browserName != "headless" {
				return reportJevError(command, errors.New("browser must be arc, chrome, any, or headless"))
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
			selector := jevdom.Selector{Browser: browser, Evaluator: dependencies.evaluator(config)}
			if !validated.NoCache {
				selector.Cache = dependencies.cache()
			}
			result, err := selector.Select(ctx, validated)
			if err != nil {
				return reportJevError(command, err)
			}
			commandName := "jev select"
			if command.Parent() != nil && command.Parent().Name() == "browser" {
				commandName = "browser select"
			}
			return emitJev(command, commandName, result)
		},
	}
	command.Flags().IntVar(&options.TabID, "tab", -1, "Existing browser tab id")
	command.Flags().StringVar(&options.Goal, "goal", "", "The element to find, using 1 to 2000 bytes")
	command.Flags().StringVar(&options.Kind, "kind", "controls", "Candidate roles: controls, text, or all")
	command.Flags().IntVar(&options.Limit, "limit", 240, "Maximum candidates to consider (1 to 480)")
	command.Flags().Float64Var(&options.Confidence, "confidence", 0.8, "Minimum decision confidence (0 to 1)")
	command.Flags().BoolVar(&options.NoCache, "no-cache", false, "Bypass the local five-minute decision cache")
	command.Flags().StringVar(&options.Origin, "origin", "", "Require this top-frame origin and skip frames outside it")
	command.Flags().StringVar(&browserName, "browser", "arc", "Browser bridge: arc, chrome, any, or task-owned headless")
	_ = command.MarkFlagRequired("tab")
	advancedFlags(command, "goal", "limit", "confidence", "no-cache")
	return command
}

func init() {
	browserCmd.AddCommand(newBrowserSelectCommand(defaultJevDOMDependencies()))
	legacy := newBrowserSelectCommand(defaultJevDOMDependencies())
	legacy.Hidden = true
	jevCmd.AddCommand(legacy)
}
