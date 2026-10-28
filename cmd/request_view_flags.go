package cmd

import (
	"fmt"
	"strings"

	"github.com/repplus/rep-cli/internal/output"
	"github.com/spf13/cobra"
)

// Selection and rendering are separate axes. Keeping presets composable preserves
// existing intersections without adding a boolean flag for every useful view.
type requestViewFlags struct {
	selects []string
	view    string
	legacy  requestView
	api     bool
}

type requestView struct {
	api, interesting, errors, mutations bool
	line, detail                        bool
}

func (flags *requestViewFlags) bind(command *cobra.Command, api bool) {
	flags.api = api
	choices := "errors, mutations"
	if api {
		choices = "api, errors, mutations, interesting"
	}
	command.Flags().StringSliceVar(&flags.selects, "select", nil, "Intersect request presets: "+choices+" (comma-separated or repeatable)")
	command.Flags().StringVar(&flags.view, "view", "line", "Text layout: line or detail (JSON is selected with --json)")
	command.Flags().BoolVar(&flags.legacy.line, "line", true, "Legacy one-line text layout")
	command.Flags().BoolVar(&flags.legacy.detail, "detail", false, "Legacy detailed text layout")
	command.Flags().BoolVar(&flags.legacy.errors, "errors", false, "Legacy error-response preset")
	command.Flags().BoolVar(&flags.legacy.mutations, "mutations", false, "Legacy state-changing-method preset")
	names := []string{"line", "detail", "errors", "mutations"}
	if api {
		command.Flags().BoolVar(&flags.legacy.api, "api", false, "Legacy API request preset")
		command.Flags().BoolVar(&flags.legacy.interesting, "interesting", false, "Legacy error and mutation intersection")
		names = append(names, "api", "interesting")
	}
	for _, name := range names {
		_ = command.Flags().MarkHidden(name)
	}
}

func (flags *requestViewFlags) resolve(command *cobra.Command) (requestView, error) {
	view := flags.legacy
	invalid := func(message string) (requestView, error) {
		return requestView{}, output.NewAgentError(output.ErrCodeInvalidArgument, command.CommandPath(), message, command.CommandPath()+" --help")
	}
	if flags.view != "line" && flags.view != "detail" {
		return invalid("--view must be line or detail")
	}
	if command.Flags().Changed("view") {
		if command.Flags().Changed("line") || command.Flags().Changed("detail") {
			return invalid("use --view alone; it cannot be combined with legacy --line or --detail")
		}
		view.line, view.detail = flags.view == "line", flags.view == "detail"
	}
	if command.Flags().Changed("select") {
		for _, name := range []string{"api", "interesting", "errors", "mutations"} {
			if command.Flags().Changed(name) {
				return invalid("use --select alone; it cannot be combined with legacy preset flags")
			}
		}
		for _, name := range flags.selects {
			switch strings.TrimSpace(name) {
			case "api":
				if !flags.api {
					return invalid("--select api is only supported by list")
				}
				view.api = true
			case "interesting":
				if !flags.api {
					return invalid("--select interesting is only supported by list")
				}
				view.interesting = true
			case "errors":
				view.errors = true
			case "mutations":
				view.mutations = true
			default:
				return invalid("unknown --select preset: " + name)
			}
		}
		if len(flags.selects) == 0 {
			return invalid("--select requires at least one preset; omit it to use normal filters")
		}
	}
	// Cobra's command deprecations can write to stdout. Migration notes must never
	// corrupt a JSON result or become part of an evidence artifact.
	for _, name := range []string{"api", "interesting", "errors", "mutations", "line", "detail"} {
		if command.Flags().Changed(name) {
			replacement := "--select " + name
			if name == "line" || name == "detail" {
				replacement = "--view line|detail"
			}
			fmt.Fprintf(command.ErrOrStderr(), "deprecated: --%s; use %s\n", name, replacement)
		}
	}
	return view, nil
}
