package cmd

import (
	"errors"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/repplus/rep-cli/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// trackRuns records whether a command's own Run began. Errors before that point
// come from cobra's argument, flag, or subcommand validation, which the root
// command's SilenceErrors would otherwise discard without any output.
func trackRuns(root *cobra.Command) *bool {
	started := new(bool)
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		if run := command.RunE; run != nil {
			command.RunE = func(command *cobra.Command, args []string) error {
				*started = true
				return run(command, args)
			}
		} else if run := command.Run; run != nil {
			command.Run = func(command *cobra.Command, args []string) {
				*started = true
				run(command, args)
			}
		}
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(root)
	return started
}

// reportUnreported prints an error that no command has written, with a stable
// code and next steps. JSON callers receive the error object on stdout, like
// every other command; text callers receive it on stderr.
func reportUnreported(command *cobra.Command, err error, runStarted bool, args []string, stdout, stderr io.Writer) {
	if err == nil || output.Reported(err) {
		return
	}
	path := rootCmd.Name()
	if command != nil {
		path = command.CommandPath()
	}
	var ae output.AgentError
	switch {
	case errors.As(err, &ae):
		if ae.Command == "" {
			ae.Command = path
		}
	case !runStarted:
		ae = output.NewAgentError(output.ErrCodeInvalidArgument, path, err.Error(), usageSuggestions(command, err)...)
	default:
		ae = output.WrapError(err, output.ErrCodeCommandFailed, path, path+" --help")
		if topic := describeTopic(command); topic != "" {
			ae.Suggest = append(ae.Suggest, "rep describe "+topic)
		}
	}
	if getOutputMode() == "json" || jsonRequested(args) {
		_ = output.EmitAgentError(stdout, ae, true)
		return
	}
	_ = output.EmitAgentError(stderr, ae, false)
}

// usageSuggestions points an unknown flag at the commands that accept it.
// Advanced flags are hidden from --help, so their full list is in describe.
func usageSuggestions(command *cobra.Command, err error) []string {
	path := rootCmd.Name()
	if command != nil {
		path = command.CommandPath()
	}
	var suggestions []string
	var missing *pflag.NotExistError
	if errors.As(err, &missing) && missing.GetSpecifiedName() != "" {
		name := missing.GetSpecifiedName()
		shorthand := missing.GetSpecifiedShortnames() != ""
		if owners := flagOwners(name, shorthand); len(owners) > 0 {
			spelled := "--" + name
			if shorthand {
				spelled = "-" + name
			}
			suggestions = append(suggestions, spelled+" is accepted by: "+strings.Join(owners, ", "))
		} else {
			suggestions = append(suggestions, "no rep command accepts this flag")
		}
	}
	suggestions = append(suggestions, path+" --help")
	if topic := describeTopic(command); topic != "" {
		suggestions = append(suggestions, "rep describe "+topic+"  # includes advanced flags hidden from --help")
	}
	return suggestions
}

func flagOwners(name string, shorthand bool) []string {
	var owners []string
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		var flag *pflag.Flag
		if shorthand {
			flag = command.LocalFlags().ShorthandLookup(name)
		} else {
			flag = command.LocalFlags().Lookup(name)
		}
		if flag != nil && !command.Hidden {
			owners = append(owners, command.CommandPath())
		}
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(rootCmd)
	sort.Strings(owners)
	if len(owners) > 6 {
		owners = append(owners[:6], "...")
	}
	return owners
}

// describeTopic names the most specific `rep describe` page for command, if any.
func describeTopic(command *cobra.Command) string {
	if command == nil || command == rootCmd {
		return ""
	}
	for _, name := range []string{command.Name(), rootCommandName(command)} {
		if _, ok := knownDescriptions[name]; ok {
			return name
		}
	}
	return ""
}

// jsonRequested detects a JSON request even when flag parsing stopped before
// reaching -j/--json, so a usage error keeps the caller's output format.
func jsonRequested(args []string) bool {
	for index, arg := range args {
		switch {
		case arg == "--":
			return false
		case arg == "-j" || arg == "--json" || arg == "--json=true" || arg == "--raw-json" || arg == "--raw-json=true" || arg == "--envelope" || arg == "--envelope=true":
			return true
		case arg == "--output=json" || arg == "-ojson" || arg == "-o=json":
			return true
		case (arg == "--output" || arg == "-o") && index+1 < len(args) && args[index+1] == "json":
			return true
		}
	}
	return false
}

func processArgs() []string {
	if len(os.Args) < 2 {
		return nil
	}
	return os.Args[1:]
}
