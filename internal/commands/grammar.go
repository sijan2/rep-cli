package commands

import (
	"errors"
	"regexp"
	"strings"

	"github.com/repplus/rep-cli/internal/output"
	"github.com/spf13/cobra"
)

var safeShellToken = regexp.MustCompile(`^[a-zA-Z0-9_./:@%+=,-]+$`)

func quoteCommand(arguments ...string) string {
	result := make([]string, len(arguments))
	for i, arg := range arguments {
		if safeShellToken.MatchString(arg) {
			result[i] = arg
		} else {
			result[i] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
		}
	}
	return strings.Join(result, " ")
}

func verbFirst(root *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	bad := false
	verb := -1
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			verb = i
			break
		}
		if arg == "-h" || arg == "--help" || arg == "--version" {
			continue
		}
		bad = true
		name := strings.TrimLeft(strings.SplitN(arg, "=", 2)[0], "-")
		flag := root.PersistentFlags().Lookup(name)
		if flag == nil && len(name) == 1 {
			flag = root.PersistentFlags().ShorthandLookup(name)
		}
		if flag != nil && flag.NoOptDefVal == "" && !strings.Contains(arg, "=") && i+1 < len(args) {
			i++
		}
	}
	if !bad {
		return nil
	}
	corrected := []string{"rep"}
	if verb >= 0 {
		corrected = append(corrected, args[verb])
		corrected = append(corrected, args[:verb]...)
		corrected = append(corrected, args[verb+1:]...)
	} else {
		corrected = append(corrected, "--help")
	}
	ae := output.NewAgentError("flag_before_verb", "rep", "place flags after the verb")
	ae.Next = quoteCommand(corrected...)
	ae.Retry = "needs-agent"
	return ae
}

func commandExit(err error, started bool) int {
	if err == nil {
		return 0
	}
	var ae output.AgentError
	if errors.As(err, &ae) {
		if ae.Code == "invalid_argument" || ae.Code == "flag_before_verb" || ae.Code == "invalid_query" {
			return 2
		}
		return 1
	}
	if !started {
		return 2
	}
	return 1
}
