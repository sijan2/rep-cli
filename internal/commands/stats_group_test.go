package commands

import (
	"bytes"
	"encoding/json"
	"os"

	"testing"

	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

func aggregationFixture(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("REP_WORKSPACE", "aggregation-tests")
	t.Setenv("REP_TASK", "review")
	t.Setenv("REPLIVE_PATH", "")
	t.Setenv("REPANDROID_PATH", "")
	scope.Reset()
	t.Cleanup(scope.Reset)
	selected, err := scope.Configure(scope.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(selected.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	fixture := store.Export{Requests: []store.Request{
		{ID: "first", Method: "GET", URL: "https://example.test/users/123", Response: &store.Response{Status: 200}},
		{ID: "second", Method: "GET", URL: "https://example.test/users/456", Response: &store.Response{Status: 201}},
		{ID: "third", Method: "POST", URL: "https://example.test/settings", Response: &store.Response{Status: 403}},
	}}
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(selected.LivePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func executeAggregation(command *cobra.Command, args ...string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SilenceErrors, command.SilenceUsage = true, true
	command.SetArgs(args)
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}

func TestAggregationUsageErrorsRemainStructured(t *testing.T) {
	for _, args := range [][]string{
		{"stats", "count", "typo"},
		{"stats", "count", "method", "status"},
		{"stats", "--unique", "method", "--distribution", "status"},
		{"group", "method", "status"},
		{"group", "path", "--by", "path"},
	} {
		stdout, _, code := runEntryPoint(t, append(args, "--raw-json")...)
		var result struct {
			Error struct{ Code string }
		}
		if code != 2 || json.Unmarshal([]byte(stdout), &result) != nil || result.Error.Code != "invalid_argument" {
			t.Fatalf("invalid syntax lacks stable JSON error: %v: %s (exit %d)", args, stdout, code)
		}
	}
}
