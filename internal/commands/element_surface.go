package commands

import (
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
	bindLabTabFlag(flags, &shared.TabID, "Existing task-owned browser tab")
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

func newBrowserElementCommand(deps jevDOMDependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "element",
		Short: "Observe, select, and revalidate scoped page elements",
		Long:  "Element commands share a tab and observation scope. Pass --tab tN from rep page; its task and browser profile are inferred. Selection returns a model-assessed handle without acting; observation and validation read browser evidence. Use rep do for explicit actions.",
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
