package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/store"
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

func TestListPresetMigrationPreservesFilteredEvidence(t *testing.T) {
	directory := t.TempDir()
	fixture := store.Export{Requests: []store.Request{
		{ID: "read-error", Method: "GET", URL: "https://example.test/a", ResourceType: "fetch", Response: &store.Response{Status: 503}},
		{ID: "api-mutation-error", Method: "POST", URL: "https://example.test/b", ResourceType: "fetch", Response: &store.Response{Status: 403}},
		{ID: "script-mutation-error", Method: "PUT", URL: "https://example.test/c", ResourceType: "script", Response: &store.Response{Status: 500}},
		{ID: "api-mutation-ok", Method: "POST", URL: "https://example.test/d", ResourceType: "fetch", Response: &store.Response{Status: 200}},
	}}
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "fixture.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		canonical, legacy, extra []string
		count                    int
	}{
		{[]string{"--select", "api,errors"}, []string{"--api", "--errors"}, nil, 2},
		{[]string{"--select", "interesting"}, []string{"--interesting"}, nil, 2},
		{[]string{"--select", "mutations"}, []string{"--mutations"}, nil, 3},
		{[]string{"--select", "mutations"}, []string{"--mutations"}, []string{"--method", "GET"}, 1},
		{[]string{"--view", "detail"}, []string{"--detail"}, nil, 4},
	} {
		run := func(options []string) (string, string) {
			t.Helper()
			args := []string{"-test.run=^TestEntryPointHelperProcess$", "--", "--global", "list", "--primary=false", "--raw-json"}
			args = append(args, test.extra...)
			args = append(args, options...)
			command := exec.Command(os.Args[0], args...)
			command.Dir = directory
			for _, item := range os.Environ() {
				name, _, _ := strings.Cut(item, "=")
				switch name {
				case "REP_WORKSPACE", "REP_TASK", "REPLIVE_PATH", "REPANDROID_PATH", "XDG_DATA_HOME":
					continue
				}
				command.Env = append(command.Env, item)
			}
			command.Env = append(command.Env, "REP_ENTRY_POINT_TEST=1", "XDG_DATA_HOME="+filepath.Join(directory, "data"), "REPLIVE_PATH="+path)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			if err := command.Run(); err != nil {
				t.Fatalf("%v: %v stdout=%s stderr=%s", args, err, stdout.String(), stderr.String())
			}
			return stdout.String(), stderr.String()
		}
		canonical, stderr := run(test.canonical)
		if stderr != "" {
			t.Fatalf("canonical warning: %s", stderr)
		}
		legacy, stderr := run(test.legacy)
		if canonical != legacy || !strings.Contains(stderr, "deprecated:") {
			t.Fatalf("migration changed JSON: canonical=%s legacy=%s stderr=%s", canonical, legacy, stderr)
		}
		var rows []json.RawMessage
		if err := json.Unmarshal([]byte(canonical), &rows); err != nil || len(rows) != test.count {
			t.Fatalf("%v: rows=%d want=%d err=%v", test.canonical, len(rows), test.count, err)
		}
	}
}

func TestAndroidLegacyBudgetIsTransparentNoOp(t *testing.T) {
	baseline, stderr, code := runEntryPoint(t, "--global", "android", "-j")
	if code != 0 || stderr != "" {
		t.Fatalf("baseline code=%d stdout=%q stderr=%q", code, baseline, stderr)
	}
	legacy, stderr, code := runEntryPoint(t, "--global", "android", "--budget", "full", "-j")
	if code != 0 || legacy != baseline || !strings.Contains(stderr, "--budget has no effect") {
		t.Fatalf("legacy code=%d stdout=%q stderr=%q", code, legacy, stderr)
	}
}
