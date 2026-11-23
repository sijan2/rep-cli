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

var taskName string

var rootCmd = &cobra.Command{
	Use:           "rep",
	Short:         "Browser automation and captured-network analysis",
	SilenceUsage:  true,
	SilenceErrors: true,
	Long:          "Read pages, act on observed controls, and inspect captured requests.",
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
	configureContractHelp(rootCmd)
	rootCmd.Version = Version
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	rootCmd.PersistentFlags().StringVar(&taskName, "task", "", "Override the inferred task identity")
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if !term.IsTerminal(int(os.Stdout.Fd())) || os.Getenv("NO_COLOR") != "" {
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
			ae.Next, ae.Retry = "rep scope", "needs-agent"
			return coreFailure(cmd, ae)
		}
		if err := configureLabTab(cmd); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
}

func configureInvocationScope(cmd *cobra.Command, args ...string) error {
	owner := ""
	options := scope.Options{Task: taskName, TaskSet: cmd.Flags().Changed("task")}
	if handle := invocationHandle(cmd, args); handle != "" && !options.TaskSet {
		registry, err := agentstate.Default()
		if err != nil {
			return err
		}
		owner, err = registry.Owner(handle)
		if err != nil {
			_, _ = scope.ConfigureInvocation(options, "")
			return err
		}
	}
	selected, err := scope.ConfigureInvocation(options, owner)
	if err != nil {
		return err
	}
	if selected.Scoped && (labCommandName(cmd) == "android" || labCommandName(cmd) == "ig" || labCommandName(cmd) == "watch") {
		return output.NewAgentError("scope_unsupported", cmd.CommandPath(), "this lab command uses shared device or all-tab state; select REP_TASK=global explicitly", "rep lab --help")
	}
	if selected.Scoped {
		for _, name := range []string{"REPLIVE_PATH", "REPANDROID_PATH"} {
			if os.Getenv(name) != "" {
				return fmt.Errorf("%s cannot override a task store; unset it", name)
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

// labCommandName is the tool a `rep lab` invocation addresses, or "" for a core verb.
func labCommandName(cmd *cobra.Command) string {
	for current := cmd; current.Parent() != nil; current = current.Parent() {
		if current.Parent() == labCmd {
			return current.Name()
		}
	}
	return ""
}

func getOutputMode() string { return "json" }

func useEnvelope() bool { return false }
