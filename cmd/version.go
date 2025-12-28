package cmd

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/spf13/cobra"
)

// Version metadata injected at build time.
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version information",
	RunE: func(cmd *cobra.Command, args []string) error {
		if getOutputMode() == "json" {
			payload := map[string]string{
				"version":    Version,
				"commit":     Commit,
				"build_date": BuildDate,
			}
			out, _ := sonic.MarshalIndent(payload, "", "  ")
			fmt.Println(string(out))
			return nil
		}

		fmt.Printf("%s (commit %s, built %s)\n", Version, Commit, BuildDate)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
