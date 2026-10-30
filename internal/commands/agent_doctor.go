package commands

import (
	"github.com/spf13/cobra"
	"time"
)

// The health migration adds repairs to this read-only identity diagnostic.
func newCoreDoctorCommand() *cobra.Command {
	command := &cobra.Command{Use: "doctor", Short: "Check the selected browser bridge and build identity", Args: cobra.NoArgs}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		store, err := coreStore()
		if err != nil {
			return coreFailure(cmd, err)
		}
		ctx, cancel := coreTimeout(cmd, 5*time.Second)
		defer cancel()
		client, browser, err := resolveCoreBrowser(ctx, store, nil)
		if err != nil {
			return emitCore(cmd, coreResult{Status: "browser_unavailable", Next: coreNext("browser", "headless", "start"), Meta: map[string]any{"connected": false, "repairs": []any{}}})
		}
		return emitCore(cmd, coreResult{Status: "ready", Browser: browser.Name, Profile: browser.Profile, Meta: map[string]any{"connected": true, "build_digest": client.Registry.BuildDigest}})
	}
	return command
}
func init() { rootCmd.AddCommand(newCoreDoctorCommand()) }
