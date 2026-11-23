package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Keep numeric lab IDs compatible while allowing the same public tab handles
// returned by core commands. Resolve handles after task inference, not while
// Cobra is still parsing flags.
type labTabValue struct {
	target string
	id     *int
}

func (v *labTabValue) String() string { return v.target }
func (v *labTabValue) Type() string   { return "tab" }
func (v *labTabValue) Set(value string) error {
	if agentstate.TabPattern.MatchString(value) {
		v.target, *v.id = value, -1
		return nil
	}
	id, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("use a returned tN handle or a numeric browser tab ID")
	}
	v.target, *v.id = value, id
	return nil
}

func bindLabTabFlag(flags *pflag.FlagSet, id *int, description string) {
	*id = -1
	flags.Var(&labTabValue{target: "-1", id: id}, "tab", description+" (tN handle or numeric ID)")
}

func labTabHandle(command *cobra.Command) string {
	if rootCommandName(command) != "lab" {
		return ""
	}
	flag := labCommandFlag(command, "tab")
	if flag != nil {
		if value, ok := flag.Value.(*labTabValue); ok && agentstate.TabPattern.MatchString(value.target) {
			return value.target
		}
	}
	return ""
}

func labCommandFlag(command *cobra.Command, name string) *pflag.Flag {
	if flag := command.Flags().Lookup(name); flag != nil {
		return flag
	}
	return command.InheritedFlags().Lookup(name)
}

type labTabContextKey struct{}

func configureLabTab(command *cobra.Command) error {
	handle := labTabHandle(command)
	if handle == "" {
		return nil
	}
	index, err := coreStore()
	if err != nil {
		return err
	}
	tab, err := index.Tab(handle)
	if err != nil {
		return err
	}
	selected, _ := scope.Current()
	if selected.Identity() != tab.Owner {
		return agentstate.Failure("owned_by_other_task", "explicit task does not own this tab", coreNext("tabs"))
	}
	if flag := labCommandFlag(command, "browser"); flag != nil {
		if flag.Changed {
			name := strings.ToLower(flag.Value.String())
			if name != "any" && name != tab.Browser.Name {
				return coreArgument("--browser conflicts with the addressed tab's browser; omit it to follow the handle")
			}
		}
		// Lab operations echo the actual browser selection, including when a
		// permissive --browser any was supplied.
		if err := flag.Value.Set(tab.Browser.Name); err != nil {
			return err
		}
	}
	value := labCommandFlag(command, "tab").Value.(*labTabValue)
	*value.id = tab.ChromeID
	ctx := command.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	command.SetContext(context.WithValue(ctx, labTabContextKey{}, tab))
	return nil
}
