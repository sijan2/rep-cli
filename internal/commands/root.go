package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/agentstate"
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
	evidenceRunID string
)

var rootCmd = &cobra.Command{
	Use:           "rep",
	Short:         "Browser automation and captured-network analysis",
	SilenceUsage:  true,
	SilenceErrors: true,
	Long: `Rep drives browser workflows and preserves captured network evidence.

Start with:
  rep browser page <url>               Open, read, and get control references
  rep browser reference <url>          Save content, CSS, fonts, assets and timings
  rep browser do REF --tab ID --text TEXT  Fill an observed control
  rep browser chat <url> --message TEXT   Send once and observe its reply
  rep browser interact flow.json --tab ID --apply  Run a declared UI plan
  rep browser capture open <url> --keep-tab  Navigate and capture
  rep summary                         Inspect the current capture
  rep describe browser                Full browser options and contracts

Choose --workspace/--task or set REP_WORKSPACE/REP_TASK in the calling process.
Dia is the default browser. --raw-json selects plain JSON by itself.
Low-level controls and specialized tools remain available in the groups below.`,
}

func Execute() {
	if err := verbFirst(rootCmd, processArgs()); err != nil {
		reportUnreported(rootCmd, err, false, processArgs(), os.Stdout, os.Stderr)
		os.Exit(2)
	}
	configureCommandGroups()
	runStarted := trackRuns(rootCmd)
	if command, err := rootCmd.ExecuteC(); err != nil {
		// SilenceErrors keeps cobra from printing raw text; every error that a
		// command has not already written is reported here, never dropped.
		reportUnreported(command, err, *runStarted, processArgs(), os.Stdout, os.Stderr)
		os.Exit(commandExit(err, *runStarted))
	}
}

func init() {
	rootCmd.Version = Version
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	rootCmd.PersistentFlags().StringVarP(&outputMode, "output", "o", "compact", "Output mode: compact, meta, full, json")
	rootCmd.PersistentFlags().BoolVarP(&jsonOutput, "json", "j", false, "Output as JSON (shorthand for --output json)")
	rootCmd.PersistentFlags().BoolVar(&forceEnvelope, "envelope", false, "Compatibility spelling; machine output is plain JSON")
	rootCmd.PersistentFlags().BoolVar(&rawJSON, "raw-json", false, "Output plain JSON; implies --json")
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "Disable ANSI color (also honors NO_COLOR env)")
	rootCmd.PersistentFlags().StringVar(&workspaceName, "workspace", "", "Workspace namespace (or REP_WORKSPACE / nearest project binding)")
	rootCmd.PersistentFlags().StringVar(&taskName, "task", "", "Agent task identity; required for scoped data (or REP_TASK)")
	rootCmd.PersistentFlags().BoolVar(&globalScope, "global", false, "Explicitly use shared legacy data; bypass workspace bindings and environment")
	rootCmd.PersistentFlags().StringVar(&evidenceRunID, "run", "", "Attach operation evidence to this task's explicit run ID")
	// These spellings coexist for the staged migration and are deleted in step 9.
	for _, name := range []string{"output", "json", "envelope", "raw-json", "no-color"} {
		rootCmd.PersistentFlags().Lookup(name).Hidden = true
	}
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if !term.IsTerminal(int(os.Stdout.Fd())) || noColor || os.Getenv("NO_COLOR") != "" {
			pterm.DisableColor()
		}
		if err := configureInvocationScope(cmd, args...); err != nil {
			if _, ok := err.(*agentstate.Error); ok {
				return coreFailure(cmd, err)
			}
			ae, ok := err.(output.AgentError)
			if !ok {
				ae = output.NewAgentError(output.ErrCodeInvalidArgument, cmd.CommandPath(), err.Error(), "rep scope")
				if strings.HasPrefix(err.Error(), "task_required:") {
					ae.Code = "task_required"
					ae.Retry = "needs-agent"
				}
			}
			return output.EmitAgentError(cmd.OutOrStdout(), ae, getOutputMode() == "json" || cmd.Name() == "summary" || cmd.Name() == "context" || cmd.Name() == "coverage")
		}
		return nil
	}
}

func configureInvocationScope(cmd *cobra.Command, args ...string) error {
	owner := ""
	if handle := invocationHandle(cmd, args); handle != "" && !cmd.Flags().Changed("task") && !globalScope {
		registry, err := agentstate.Default()
		if err != nil {
			return err
		}
		owner, err = registry.Owner(handle)
		// Legacy captured h_ IDs remain readable until the archive resolver lands.
		if err != nil && rootCommandName(cmd) != "body" {
			_, _ = scope.ConfigureInvocation(scope.Options{Workspace: workspaceName, Task: taskName, Global: globalScope, WorkspaceSet: cmd.Flags().Changed("workspace"), TaskSet: cmd.Flags().Changed("task")}, "")
			return err
		}
	}
	selected, err := scope.ConfigureInvocation(scope.Options{
		Workspace: workspaceName, Task: taskName, Global: globalScope,
		WorkspaceSet: cmd.Flags().Changed("workspace"), TaskSet: cmd.Flags().Changed("task"),
	}, owner)
	if err != nil {
		return err
	}
	if evidenceRunID != "" && selected.ExplicitGlobal {
		return output.NewAgentError("scope_required", cmd.CommandPath(), "--run requires an explicit workspace and task; it cannot be used with --global", "rep --workspace <project> --task <task> --run <id> "+strings.TrimPrefix(cmd.CommandPath(), rootCmd.Name()+" "))
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
	if evidenceRunID != "" {
		return true
	}
	switch rootCommandName(cmd) {
	case "evidence", "stream", "media":
		return true
	case "packets":
		return cmd.Name() != "interfaces"
	case "summary", "context", "coverage", "list", "body", "detail", "get", "search", "stats", "group", "compare", "diff", "chain", "domains", "js", "save", "sessions", "import", "note", "findings", "primary", "ignore", "mute", "clear", "setup", "recon", "auth", "curl", "download", "replay", "extract", "audit", "browse":
		return true
	case "browser":
		switch cmd.Name() {
		case "open", "fetch", "action", "download", "native-capture", "page", "do", "chat", "reference":
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
	if !term.IsTerminal(int(os.Stdout.Fd())) || jsonOutput || rawJSON || forceEnvelope {
		return "json"
	}
	return outputMode
}

func useEnvelope() bool {
	return false // compatibility flags never change the machine result shape
}
