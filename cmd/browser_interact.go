package cmd

import (
	"context"
	"errors"
	"net/url"
	"os"
	"time"

	"github.com/repplus/rep-cli/internal/browserflow"
	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/spf13/cobra"
)

func newBrowserInteractCommand(deps jevDOMDependencies) *cobra.Command {
	var tab int
	var path, browserName string
	var apply, noCache bool
	command := &cobra.Command{Use: "interact [plan.json] --tab ID", Short: "Run explicit, verified UI interactions with optional Jev target selection", Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if len(args) == 1 {
				if path != "" {
					return reportJevError(command, errors.New("provide a positional plan or --plan, not both"))
				}
				path = args[0]
			}
			if tab < 0 || path == "" {
				return reportJevError(command, errors.New("tab and plan are required"))
			}
			if browserName != "arc" && browserName != "chrome" && browserName != "headless" && browserName != "any" {
				return reportJevError(command, errors.New("unsupported browser"))
			}
			file, err := os.Open(path)
			if err != nil {
				return reportJevError(command, errors.New("cannot open interaction plan"))
			}
			plan, err := browserflow.Decode(file)
			_ = file.Close()
			if err != nil {
				return reportJevError(command, err)
			}
			// Credentials are unnecessary for exact local targets. Nothing in the plan's
			// input values or postconditions is forwarded to the semantic selector.
			var config jev.Config
			semantic := false
			for _, step := range plan.Steps {
				semantic = semantic || (step.Target != nil && step.Target.Goal != "")
			}
			if semantic {
				config, err = deps.loadConfig()
				if err != nil {
					return reportJevError(command, err)
				}
			}
			ctx, cancel := context.WithTimeout(command.Context(), 10*time.Minute)
			defer cancel()
			browser, err := deps.browser(ctx, browserName)
			if err != nil {
				return reportJevError(command, err)
			}
			engine := browserflow.Engine{Browser: browser}
			if semantic {
				selector := jevdom.Selector{Browser: &engine, Evaluator: deps.evaluator(config)}
				if !noCache {
					selector.Cache = deps.cache()
				}
				engine.Select = func(ctx context.Context, goal, page string) (browserflow.Selection, error) {
					options := jevdom.DefaultOptions()
					options.TabID = tab
					options.Goal = goal
					options.Model = config.Model
					options.NoCache = noCache
					u, _ := url.Parse(page)
					options.Origin = u.Scheme + "://" + u.Host
					decision, err := selector.Select(ctx, options)
					resolved := browserflow.Selection{Status: decision.Status, NeedsReview: decision.NeedsReview}
					if decision.Selected != nil {
						resolved.Name = decision.Selected.Name
						resolved.Role = decision.Selected.Role
						resolved.BackendDOMNodeID = decision.Selected.BackendDOMNodeID
						resolved.TextTruncated = decision.Selected.TextTruncated
					}
					return resolved, err
				}
			}
			report, runErr := engine.Run(ctx, tab, plan, apply)
			if err = emitJev(command, command.CommandPath(), report); err != nil {
				return err
			}
			if runErr != nil {
				return reportJevError(command, runErr)
			}
			return nil
		}}
	command.Flags().IntVar(&tab, "tab", -1, "Existing task-owned browser tab")
	command.Flags().StringVar(&path, "plan", "", "Versioned JSON interaction plan (values remain local)")
	command.Flags().StringVar(&browserName, "browser", "arc", "arc, chrome, any, or task-owned headless")
	command.Flags().BoolVar(&apply, "apply", false, "Execute the explicit plan; default validates and previews first target")
	command.Flags().BoolVar(&noCache, "no-cache", false, "Bypass the Jev decision cache for semantic targets")
	_ = command.MarkFlagRequired("tab")
	advancedFlags(command, "plan", "no-cache")
	return command
}
func init() {
	browserCmd.AddCommand(newBrowserInteractCommand(defaultJevDOMDependencies()))
	legacy := newBrowserInteractCommand(defaultJevDOMDependencies())
	legacy.Use = "act [plan.json] --tab ID"
	legacy.Hidden = true
	jevCmd.AddCommand(legacy)
}
