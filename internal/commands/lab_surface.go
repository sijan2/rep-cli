//go:build replab

package commands

import (
	"github.com/repplus/rep-cli/internal/browserdiagnostics"
	"github.com/spf13/cobra"
)

// Lab commands have their own top-level paths. The old browser children remain
// migration spellings until the final removal; core never registers either.
func init() {
	deps := defaultJevDOMDependencies()
	element := newBrowserElementCommand(deps)
	element.PersistentFlags().String("browser", defaultBrowser, "Browser bridge: dia, arc, chrome, headless, or any")
	locate := newBrowserLocateCommand(deps)
	addDecisionBrowserFlag(locate)
	state := newBrowserStateCommand(deps)
	addDecisionBrowserFlag(state)
	rootCmd.AddCommand(element, locate, state, newBrowserStepCommand(deps), newNativeBrowserCaptureCommandWith(browserdiagnostics.Run))
	watch := &cobra.Command{Use: "watch", Short: "Manage ambient all-tab network capture"}
	watch.PersistentFlags().StringVar(&browserSelector, "browser", defaultBrowser, "Browser bridge to inspect")
	for _, action := range []string{"start", "stop", "status"} {
		action := action
		watch.AddCommand(&cobra.Command{Use: action, Short: action + " ambient capture", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return runBrowserWatch(cmd, action) }})
	}
	rootCmd.AddCommand(watch)
}
