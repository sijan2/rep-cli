package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var (
	// Global flags
	outputMode string
	jsonOutput bool
)

// rootCmd represents the base command
var rootCmd = &cobra.Command{
	Use:   "rep",
	Short: "HTTP traffic analyzer for bug bounty hunting",
	Long: `rep-cli - AI-agent optimized HTTP traffic analyzer

Designed for efficient analysis by AI agents like Claude Code.
Works with rep+ Chrome extension for real-time traffic capture.

Real-time analysis (reads live.json, same as extension):
  rep summary                          Quick overview for first-pass analysis
  rep domains                          List all domains with stats
  rep list                             List requests (compact by default)
  rep body <id>                        Get full response body for deep analysis

Session management:
  rep save                             Save current session for later
  rep save --note "auth flow"          Save with descriptive note
  rep sessions                         List saved sessions
  rep list --saved latest              View most recent saved session
  rep list --saved 20231227            View by session ID prefix

Configuration:
  rep ignore <domain>                  Add domain to ignore list
  rep primary <domain>                 Mark domain as primary target
  rep clear                            Clear all data (live + saved + config)

Output modes (--output):
  compact   Truncated bodies, perfect for scanning (default)
  meta      Headers only, no bodies - ultra fast
  full      Complete bodies for deep analysis
  json      Raw JSON for piping to other tools`,
}

// Execute adds all child commands to the root command
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.Version = Version
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	rootCmd.PersistentFlags().StringVarP(&outputMode, "output", "o", "compact", "Output mode: compact, meta, full, json")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output as JSON (shorthand for --output json)")
}

// getOutputMode returns the current output mode
func getOutputMode() string {
	if jsonOutput {
		return "json"
	}
	return outputMode
}
