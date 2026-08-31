package cmd

import (
	"os"

	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	outputMode    string
	jsonOutput    bool
	forceEnvelope bool
	rawJSON       bool
	noColor       bool
)

var rootCmd = &cobra.Command{
	Use:           "rep",
	Short:         "AI-agent optimized HTTP traffic analyzer and browser bridge",
	SilenceUsage:  true,
	SilenceErrors: true,
	Long: `rep-cli - AI-agent optimized HTTP traffic analyzer

Captures traffic from the rep+ Chrome/Arc extension and can drive a connected
browser through its existing signed-in profile.

Agent workflow:
  rep browser status                 Confirm the Arc/Chrome bridge
  rep browse github.com              Navigate in a background tab and capture
  rep browser fetch <url>            Send a session-authenticated browser fetch
  rep browser action <js> --tab <id> Run page logic with isolated capture
  rep browser create about:blank     Create a task-owned tab without capture
  rep summary                        Inspect the capture landscape
  rep primary <domain>               Scope the target
  rep list --primary -o meta         Triage request metadata
  rep body <id>                      Read one response body
  rep download <id> <path>           Stream a captured GET without exposing secrets
  rep browser download <id> <path>   Stream a captured GET through the browser
  rep save --note "flow"             Archive the live capture

Output modes:
  compact   Concise, body-limited output (default)
  meta      Headers and metadata only
  full      Complete captured bodies
  json      Structured JSON`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.Version = Version
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	rootCmd.PersistentFlags().StringVarP(&outputMode, "output", "o", "compact", "Output mode: compact, meta, full, json")
	rootCmd.PersistentFlags().BoolVarP(&jsonOutput, "json", "j", false, "Output as JSON (shorthand for --output json)")
	rootCmd.PersistentFlags().BoolVar(&forceEnvelope, "envelope", false, "Force envelope wrap on JSON output (default: auto-on for non-TTY stdout)")
	rootCmd.PersistentFlags().BoolVar(&rawJSON, "raw-json", false, "Emit JSON without envelope (bare array/object) — for older scripts")
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "Disable ANSI color (also honors NO_COLOR env)")
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if noColor || os.Getenv("NO_COLOR") != "" {
			pterm.DisableColor()
		}
	}
}

func getOutputMode() string {
	if jsonOutput {
		return "json"
	}
	return outputMode
}

func useEnvelope() bool {
	if rawJSON {
		return false
	}
	if forceEnvelope {
		return true
	}
	return !term.IsTerminal(int(os.Stdout.Fd()))
}
