package commands

import (
	"bytes"

	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRequestViewGrammar(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		api     bool
		want    requestView
		fail    bool
		warning bool
	}{
		{name: "default", api: true, want: requestView{line: true}},
		{name: "intersection", api: true, args: []string{"--select", "api,errors", "--select", "mutations", "--view", "detail"}, want: requestView{api: true, errors: true, mutations: true, detail: true}},
		{name: "interesting", api: true, args: []string{"--select", "interesting"}, want: requestView{interesting: true, line: true}},
		{name: "legacy", api: true, args: []string{"--api", "--errors", "--line=false"}, want: requestView{api: true, errors: true}, warning: true},
		{name: "legacy-false", api: true, args: []string{"--mutations=false", "--detail=false"}, want: requestView{line: true}, warning: true},
		{name: "android", args: []string{"--select", "errors,mutations"}, want: requestView{errors: true, mutations: true, line: true}},
		{name: "unsupported-mobile", args: []string{"--select", "api"}, fail: true},
		{name: "unknown-preset", api: true, args: []string{"--select", "anything"}, fail: true},
		{name: "empty-preset", api: true, args: []string{"--select="}, fail: true},
		{name: "mixed-preset", api: true, args: []string{"--select", "api", "--errors=false"}, fail: true},
		{name: "mixed-view", api: true, args: []string{"--view", "line", "--detail=false"}, fail: true},
		{name: "unknown-view", api: true, args: []string{"--view", "json"}, fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := &cobra.Command{Use: "list"}
			var flags requestViewFlags
			flags.bind(command, test.api)
			var stdout, stderr bytes.Buffer
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			if err := command.ParseFlags(test.args); err != nil {
				t.Fatal(err)
			}
			got, err := flags.resolve(command)
			if (err != nil) != test.fail {
				t.Fatalf("error=%v want failure=%v", err, test.fail)
			}
			if test.fail {
				return
			}
			if got != test.want {
				t.Fatalf("got %#v want %#v", got, test.want)
			}
			if stdout.Len() != 0 || strings.Contains(stderr.String(), "deprecated:") != test.warning {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}
