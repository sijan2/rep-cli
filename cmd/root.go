package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	outputMode    string
	jsonOutput    bool
	forceEnvelope bool
	rawJSON       bool
	noColor       bool
	workspaceName string
	taskName      string
	globalScope   bool
)

var rootCmd = &cobra.Command{
	Use:           "rep",
	Short:         "Browser automation and captured-network analysis",
	SilenceUsage:  true,
	SilenceErrors: true,
	Long: `Rep drives browser workflows and preserves captured network evidence.

Start with:
  rep browser create <url>             Open a task-owned tab
  rep browser select "Save button" --tab ID
  rep browser interact flow.json --tab ID --apply
  rep browser open <url> --keep-tab    Navigate and capture
  rep summary                         Inspect the current capture
  rep describe browser                Full browser options and contracts

Choose --workspace/--task or set REP_WORKSPACE/REP_TASK in the calling process.
Arc is the default browser. --raw-json selects plain JSON by itself.
Low-level controls and specialized tools remain available in the groups below.`,
}

func Execute() {
	configureCommandGroups()
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
	rootCmd.PersistentFlags().BoolVar(&rawJSON, "raw-json", false, "Output plain JSON; implies --json")
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "Disable ANSI color (also honors NO_COLOR env)")
	rootCmd.PersistentFlags().StringVar(&workspaceName, "workspace", "", "Workspace namespace (or REP_WORKSPACE / nearest project binding)")
	rootCmd.PersistentFlags().StringVar(&taskName, "task", "", "Agent task identity; required for scoped data (or REP_TASK)")
	rootCmd.PersistentFlags().BoolVar(&globalScope, "global", false, "Explicitly use shared legacy data; bypass workspace bindings and environment")
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if noColor || os.Getenv("NO_COLOR") != "" {
			pterm.DisableColor()
		}
		if err := configureInvocationScope(cmd); err != nil {
			ae, ok := err.(output.AgentError)
			if !ok {
				ae = output.NewAgentError(output.ErrCodeInvalidArgument, cmd.CommandPath(), err.Error(), "rep scope")
			}
			return output.EmitAgentError(cmd.OutOrStdout(), ae, getOutputMode() == "json" || cmd.Name() == "summary" || cmd.Name() == "context")
		}
		return nil
	}
}

func configureInvocationScope(cmd *cobra.Command) error {
	selected, err := scope.Configure(scope.Options{
		Workspace: workspaceName, Task: taskName, Global: globalScope,
		WorkspaceSet: cmd.Flags().Changed("workspace"), TaskSet: cmd.Flags().Changed("task"),
	})
	if err != nil {
		return err
	}
	if selected.Scoped && (rootCommandName(cmd) == "android" || rootCommandName(cmd) == "ig") {
		return output.NewAgentError("scope_unsupported", cmd.CommandPath(), "this command family uses shared device state and does not support workspace isolation; explicitly use --global", "rep --global "+strings.TrimPrefix(cmd.CommandPath(), rootCmd.Name()+" "))
	}
	if selected.Scoped && (rootCommandName(cmd) == "auth" || rootCommandName(cmd) == "setup") {
		return output.NewAgentError("scope_unsupported", cmd.CommandPath(), "this command uses shared credential export files and does not support workspace isolation; explicitly use --global", "rep --global "+strings.TrimPrefix(cmd.CommandPath(), rootCmd.Name()+" "))
	}
	if !commandRequiresScope(cmd) {
		return nil
	}
	if !selected.Scoped && !selected.ExplicitGlobal {
		return output.NewAgentError("scope_required", cmd.CommandPath(), "select a workspace and task before reading or changing capture data; use --global only to intentionally access shared legacy data", "rep --workspace <project> --task <task> "+strings.TrimPrefix(cmd.CommandPath(), rootCmd.Name()+" "), "rep workspace init <project>")
	}
	if selected.Scoped {
		if selected.TaskSource == "default" {
			return output.NewAgentError("task_required", cmd.CommandPath(), "choose a task identity for this agent with --task or REP_TASK; a project binding alone does not isolate concurrent agents", "rep --workspace "+selected.Workspace+" --task <unique-task> "+strings.TrimPrefix(cmd.CommandPath(), rootCmd.Name()+" "), "rep scope")
		}
		for _, name := range []string{"REPLIVE_PATH", "REPANDROID_PATH"} {
			if os.Getenv(name) != "" {
				return fmt.Errorf("%s cannot override a scoped store; unset it or explicitly use --global", name)
			}
		}
	}
	return nil
}

func rootCommandName(cmd *cobra.Command) string {
	current := cmd
	for current.Parent() != nil && current.Parent() != rootCmd {
		current = current.Parent()
	}
	return current.Name()
}

func commandRequiresScope(cmd *cobra.Command) bool {
	switch rootCommandName(cmd) {
	case "summary", "context", "list", "body", "detail", "get", "search", "stats", "group", "compare", "diff", "chain", "domains", "js", "save", "sessions", "import", "note", "findings", "primary", "ignore", "mute", "clear", "setup", "recon", "auth", "curl", "download", "replay", "extract", "audit", "browse":
		return true
	case "browser":
		switch cmd.Name() {
		case "open", "fetch", "action", "download":
			return true
		}
		return cmd.Parent() != nil && cmd.Parent().Name() == "watch"
	case "jev":
		return cmd.Name() == "classify"
	case "android":
		// Configuration and device setup remain independent of stored capture data.
		if cmd.Parent() != nil && cmd.Parent().Name() == "config" {
			return false
		}
		switch cmd.Name() {
		case "android", "clear", "status", "summary", "search", "stats", "group", "extract":
			return true
		}
	case "ig":
		return cmd.Name() == "list" || cmd.Name() == "surface" || cmd.Name() == "body"
	}
	return false
}

func getOutputMode() string {
	if jsonOutput || rawJSON || forceEnvelope {
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
