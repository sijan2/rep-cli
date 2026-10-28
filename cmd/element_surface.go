package cmd

import (
	"fmt"

	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Element scope is shared, but selection budgets and observation projection
// budgets measure different things and deliberately remain on their verbs.
type elementFlags struct {
	TabID    int
	Kind     string
	Origin   string
	FrameID  string
	FrameURL string
	RootNode int64
}

func bindElementFlags(flags *pflag.FlagSet) *elementFlags {
	shared := &elementFlags{}
	flags.IntVar(&shared.TabID, "tab", -1, "Existing task-owned browser tab")
	flags.StringVar(&shared.Kind, "kind", "controls", "Candidate roles: controls, text, or all")
	flags.StringVar(&shared.Origin, "origin", "", "Require this top-frame origin and skip frames outside it")
	flags.StringVar(&shared.FrameID, "frame", "", "Restrict observation to this frame id")
	flags.StringVar(&shared.FrameURL, "frame-url", "", "Restrict to the unique frame at this exact URL")
	flags.Int64Var(&shared.RootNode, "root-node", 0, "Restrict candidates to this backend node subtree; requires a frame")
	_ = cobra.MarkFlagRequired(flags, "tab")
	return shared
}

func elementCommandFlags(command *cobra.Command, shared *elementFlags) *elementFlags {
	if shared != nil {
		return shared
	}
	return bindElementFlags(command.Flags())
}

func (shared *elementFlags) apply(options *jevdom.Options) {
	options.TabID, options.Kind, options.Origin = shared.TabID, shared.Kind, shared.Origin
	options.FrameID, options.FrameURL = shared.FrameID, shared.FrameURL
	options.ScopeBackendDOMNodeID = shared.RootNode
}

func addDecisionBrowserFlag(command *cobra.Command) {
	command.Flags().String("browser", defaultBrowser, "Browser bridge: dia, arc, chrome, any, or task-owned headless")
}

func decisionBrowserName(command *cobra.Command) string {
	name, _ := command.Flags().GetString("browser")
	return name
}

func newBrowserElementCommand(deps jevDOMDependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "element",
		Short: "Observe, select, and revalidate scoped page elements",
		Long:  "Element commands share a tab and observation scope. Selection returns a model-assessed handle without acting; observation and validation read browser evidence. Use browser interact for explicit actions.",
		Args:  cobra.NoArgs,
	}
	shared := bindElementFlags(command.PersistentFlags())
	command.AddCommand(
		newElementSelectCommand(deps, shared),
		newElementSelectBatchCommand(deps, shared),
		newElementObservationCommand(false, deps, shared),
		newElementObservationCommand(true, deps, shared),
	)
	return command
}

func markElementCompatibility(command *cobra.Command, replacement string) *cobra.Command {
	command.Hidden = true
	previous := command.PreRunE
	command.PreRunE = func(command *cobra.Command, args []string) error {
		// Cobra's Deprecated field writes to stdout, which would corrupt JSON.
		fmt.Fprintf(command.ErrOrStderr(), "Deprecated: %s; use %s instead.\n", command.CommandPath(), replacement)
		if previous != nil {
			return previous(command, args)
		}
		return nil
	}
	return command
}

func init() {
	deps := defaultJevDOMDependencies()
	browserCmd.AddCommand(newBrowserElementCommand(deps), newInheritedBrowserRuntimeCommand(deps))
	browserCmd.AddCommand(
		markElementCompatibility(newElementSelectCommand(deps, nil), "rep browser element select"),
		markElementCompatibility(newElementSelectBatchCommand(deps, nil), "rep browser element select-batch"),
		markElementCompatibility(newElementObservationCommand(false, deps, nil), "rep browser element observe"),
		markElementCompatibility(newElementObservationCommand(true, deps, nil), "rep browser element validate"),
	)
	jevCmd.AddCommand(markElementCompatibility(newBrowserSelectCommand(deps), "rep browser element select"))
}
