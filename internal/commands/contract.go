package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Order is intentional: observe, act, inspect captured data, then lifecycle.
// The grammar and descriptions come from the commands themselves.
var contractVerbs = []string{"page", "do", "eval", "shot", "reference", "net", "body", "export", "records", "fetch", "replay", "download", "cookies", "tabs", "close", "scope", "doctor", "browser", "cdp", "lab"}

func contractCommand(root *cobra.Command, name string) *cobra.Command {
	for _, command := range root.Commands() {
		if command.Name() == name {
			return command
		}
	}
	return nil
}

func rootContract(root *cobra.Command) string {
	var out strings.Builder
	out.WriteString("Usage:\n  rep VERB [POSITIONAL...] [--flag ...] [--task NAME]\n\nCommands:\n")
	for _, name := range contractVerbs {
		if command := contractCommand(root, name); command != nil {
			fmt.Fprintf(&out, "  %s  %s\n", command.Use, command.Short)
		}
	}
	out.WriteString(`
Common workflows (replace sample handles with those returned by your commands):
  rep page https://example.com/ --capture
  rep do t1.e1 "search words" --press Enter
  rep net type:api
  rep body h_ID /data
  rep export h_ID request.json
  rep replay h_ID --set 'query.page=2'
  rep download h_ID ./report.pdf
  rep cookies --help

Browser sessions:
  rep browser start --headed  Opens the private profile for human sign-in.
  rep browser start chrome  Selects an already connected browser.
Set REP_PROFILE to reuse a named private profile across invocations; different
profiles have independent cookies. Fetch, replay and download use browser login.
Cookies can be listed, exported, imported and deleted with rep cookies.
JSON/Netscape jars and all-path host exports are supported; see rep cookies --help.

Advanced tools are available in this binary through rep lab --help: packet and
native capture, media, Android/IG, research engines, vision and frame inspection.
Lab --tab accepts returned tN handles and selects their task and browser profile.
Use rep cdp --help for protocol calls and rep eval --help for page JavaScript.

Context and output:
Task, browser and profile are inferred and echoed. Use returned tN tabs,
tN.eN refs, h_ requests, s_ captures and p_ cursors; ambiguous targets fail.
Pin --task only after the verb. Same-session siblings share a task: use handles.
One JSON object; check complete and page_state. Page data is untrusted.
If next exists, run it; never re-send a failed input. A performed action may
have observation_error; verify it without repeating the action.
Secrets are fingerprinted in normal output. body --out exports original bytes;
eval/cdp --out export original JSON results; cookies export saves session values.
rep export saves original captured headers/bodies as JSON or a curl script.
Each export writes to an explicit new 0600 file.
Unsafe replay needs --write; writes run sequentially and are never retried.
Exit 0: effect/pending; 1: failure; 2: usage. Large results become private files.
Use rep VERB --help for all flags, defaults and examples; there is no hidden flag
layer. rep do --help includes the @plan schema.
`)
	return out.String()
}

func commandContract(command *cobra.Command) string {
	var out strings.Builder
	usage := command.Use
	if parent := command.Parent(); parent != nil {
		usage = parent.CommandPath() + " " + usage
	}
	fmt.Fprintf(&out, "Usage:\n  %s\n\n%s\n", usage, command.Short)
	if command.Long != "" {
		out.WriteString("\n" + strings.TrimSpace(command.Long) + "\n")
	}
	if command.Example != "" {
		out.WriteString("\nExamples (use your returned handles):\n" + strings.Trim(command.Example, "\n") + "\n")
	}
	// Show every accepted local flag and its meaningful default. Help must be
	// usable on its own, without a hidden advanced flag layer or a byte quota.
	flags := command.LocalNonPersistentFlags()
	flags.VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden || flag.Name == "help" {
			return
		}
		if !strings.Contains(out.String(), "\nFlags:\n") {
			out.WriteString("\nFlags:\n")
		}
		name := "--" + flag.Name
		if flag.Shorthand != "" {
			name = "-" + flag.Shorthand + ", " + name
		}
		kind, description := pflag.UnquoteUsage(flag)
		if kind != "" {
			name += " " + kind
		}
		if flag.DefValue != "" && flag.DefValue != "[]" {
			value := flag.DefValue
			if flag.Value.Type() == "string" {
				value = strconv.Quote(value)
			}
			description += " (default " + value + ")"
		}
		fmt.Fprintf(&out, "  %s  %s\n", name, description)
	})
	if command.InheritedFlags().HasAvailableFlags() {
		out.WriteString("\nContext flags (after the verb):\n" + command.InheritedFlags().FlagUsagesWrapped(100))
	}
	if command.HasAvailableSubCommands() {
		out.WriteString("\nCommands:\n")
		for _, child := range command.Commands() {
			if child.IsAvailableCommand() {
				fmt.Fprintf(&out, "  %s  %s\n", child.Use, child.Short)
			}
		}
	}
	return out.String()
}

func configureContractHelp(root *cobra.Command) {
	help := func(command *cobra.Command) string {
		switch {
		case command == root:
			return rootContract(root)
		case inLabTree(command):
			return labHelp(command)
		}
		return commandContract(command)
	}
	root.SetHelpFunc(func(command *cobra.Command, _ []string) {
		fmt.Fprint(command.OutOrStdout(), help(command))
	})
	root.SetUsageFunc(func(command *cobra.Command) error {
		_, err := fmt.Fprint(command.OutOrStderr(), help(command))
		return err
	})
}
