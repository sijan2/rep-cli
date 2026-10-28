package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
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
	oldMode, oldJSON := outputMode, jsonOutput
	outputMode, jsonOutput = "json", true
	t.Cleanup(func() { outputMode, jsonOutput = oldMode, oldJSON })
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

func TestStatsOperationGrammarPreservesResults(t *testing.T) {
	aggregationFixture(t)
	for _, tc := range []struct {
		mode, legacy, field string
		want                string
	}{
		{"count", "count-by", "method", `{"field":"method","counts":[{"value":"GET","count":2},{"value":"POST","count":1}],"total":3}`},
		{"unique", "unique", "method", `{"field":"method","unique_count":2,"values":["GET","POST"]}`},
		{"distribution", "distribution", "status", `{"field":"status","total":3,"buckets":[{"value":"2xx","count":2,"percent":66.66666666666666},{"value":"4xx","count":1,"percent":33.33333333333333}]}`},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			canonical, stderr, err := executeAggregation(newStatsCommand(), tc.mode, tc.field)
			if err != nil || stderr != "" {
				t.Fatalf("canonical: %s %s %v", canonical, stderr, err)
			}
			var got, want any
			if json.Unmarshal([]byte(canonical), &got) != nil || json.Unmarshal([]byte(tc.want), &want) != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("result mismatch: %s", canonical)
			}
			legacy, stderr, err := executeAggregation(newStatsCommand(), "--"+tc.legacy, tc.field)
			if err != nil || legacy != canonical || !strings.Contains(stderr, "deprecated") || !strings.Contains(stderr, "rep stats "+tc.mode+" FIELD") {
				t.Fatalf("legacy differs or warning missing: %s %s %v", legacy, stderr, err)
			}
		})
	}
	result, _, err := executeAggregation(newStatsCommand(), "--limit", "1", "count", "method")
	var count CountResult
	if err != nil || json.Unmarshal([]byte(result), &count) != nil || len(count.Counts) != 1 || count.Counts[0].Value != "GET" || count.Total != 3 {
		t.Fatalf("--limit before operation changed count semantics: %s %v", result, err)
	}
	after, _, err := executeAggregation(newStatsCommand(), "count", "method", "--limit", "1")
	if err != nil || after != result {
		t.Fatalf("--limit placement changes count semantics: %s %v", after, err)
	}
	legacy, stderr, err := executeAggregation(newStatsCommand(), "count", "method", "--top", "1")
	if err != nil || legacy != result || !strings.Contains(stderr, "use --limit") {
		t.Fatalf("legacy --top changed count semantics: %s %s %v", legacy, stderr, err)
	}
}

func TestStatsUniqueDoesNotAdvertiseOrSilentlyIgnoreLimit(t *testing.T) {
	aggregationFixture(t)
	canonical, _, err := executeAggregation(newStatsCommand(), "unique", "method")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"unique", "method", "--top", "1"},
		{"--unique", "method", "--top", "1"},
	} {
		legacy, stderr, err := executeAggregation(newStatsCommand(), args...)
		if err != nil || legacy != canonical || !strings.Contains(stderr, "has no effect for unique; omit it") {
			t.Fatalf("legacy --top lost no-effect compatibility: %s %s %v", legacy, stderr, err)
		}
	}
	for _, args := range [][]string{
		{"unique", "method", "--limit", "1"},
		{"--limit", "1", "unique", "method"},
	} {
		stdout, _, err := executeAggregation(newStatsCommand(), args...)
		if err == nil || stdout != "" || !strings.Contains(err.Error(), "unknown flag: --limit") {
			t.Fatalf("unique silently ignored a limit: %s %v", stdout, err)
		}
	}
	for _, child := range newStatsCommand().Commands() {
		if (child.Flags().Lookup("limit") != nil) != (child.Name() != "unique") {
			t.Fatalf("--limit must be local to count/distribution, found on %s", child.Name())
		}
	}
}

func TestGroupPositionalGrammarPreservesNormalization(t *testing.T) {
	aggregationFixture(t)
	options := []string{"--pattern", `\d+`, "--replace", ":id", "--min-count", "2", "--limit", "1"}
	canonical, stderr, err := executeAggregation(newGroupCommand(), append([]string{"path"}, options...)...)
	var groups []RequestGroup
	if err != nil || stderr != "" || json.Unmarshal([]byte(canonical), &groups) != nil || len(groups) != 1 || groups[0].Key != "/users/:id" || groups[0].Count != 2 || groups[0].Methods["GET"] != 2 || len(groups[0].Requests) != 2 {
		t.Fatalf("wrong grouped result: %s %s %v", canonical, stderr, err)
	}
	legacy, stderr, err := executeAggregation(newGroupCommand(), append([]string{"--by", "path"}, options...)...)
	var legacyGroups []RequestGroup
	if err != nil || json.Unmarshal([]byte(legacy), &legacyGroups) != nil || !reflect.DeepEqual(legacyGroups, groups) || !strings.Contains(stderr, "deprecated") || !strings.Contains(stderr, "rep group FIELD") {
		t.Fatalf("legacy differs or warning missing: %s %s %v", legacy, stderr, err)
	}
}

func TestAggregationRejectsAmbiguousOrIgnoredArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		new  func() *cobra.Command
		args []string
	}{
		{"stats missing", newStatsCommand, nil},
		{"stats duplicate limits", newStatsCommand, []string{"count", "method", "--limit", "1", "--top", "1"}},
		{"stats pretend commands", newStatsCommand, []string{"method", "status", "domain"}},
		{"stats extra field", newStatsCommand, []string{"count", "method", "status"}},
		{"stats unknown field", newStatsCommand, []string{"count", "typo"}},
		{"stats malformed segment", newStatsCommand, []string{"count", "path:segment:no"}},
		{"stats partial segment", newStatsCommand, []string{"count", "path:segment:1x"}},
		{"stats empty segment", newStatsCommand, []string{"count", "path:segment:"}},
		{"stats overflow segment", newStatsCommand, []string{"count", "path:segment:999999999999999999999999999999"}},
		{"stats negative segment", newStatsCommand, []string{"count", "path:segment:-1"}},
		{"stats empty header", newStatsCommand, []string{"unique", "header:"}},
		{"stats blank header", newStatsCommand, []string{"unique", "header:   "}},
		{"stats missing field", newStatsCommand, []string{"distribution"}},
		{"stats legacy conflict", newStatsCommand, []string{"--count-by", "method", "--unique", "status"}},
		{"stats mixed syntax", newStatsCommand, []string{"count", "method", "--count-by", "method"}},
		{"stats legacy positional", newStatsCommand, []string{"--count-by", "method", "ignored"}},
		{"group missing", newGroupCommand, nil},
		{"group multiple", newGroupCommand, []string{"method", "status"}},
		{"group unknown", newGroupCommand, []string{"typo"}},
		{"group mixed syntax", newGroupCommand, []string{"method", "--by", "method"}},
		{"group empty header", newGroupCommand, []string{"header:"}},
		{"group blank header", newGroupCommand, []string{"header:   "}},
		{"group unsupported stats field", newGroupCommand, []string{"body-type"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, err := executeAggregation(tc.new(), tc.args...)
			if err == nil || stdout != "" {
				t.Fatalf("invalid command read traffic or succeeded: %s %v", stdout, err)
			}
		})
	}
}

func TestAggregationHelpHasFiniteExplicitOperations(t *testing.T) {
	command := newStatsCommand()
	var names []string
	for _, child := range command.Commands() {
		names = append(names, child.Name())
		if len(child.Commands()) != 0 {
			t.Fatalf("field unexpectedly became a command under %s", child.Name())
		}
	}
	if !reflect.DeepEqual(names, []string{"count", "distribution", "unique"}) {
		t.Fatalf("unexpected operations: %v", names)
	}
	for _, name := range []string{"count-by", "unique", "distribution", "top"} {
		flag := command.PersistentFlags().Lookup(name)
		if !flag.Hidden {
			t.Fatalf("legacy flag %s remains advertised", name)
		}
	}
	if len(newGroupCommand().Commands()) != 0 || !newGroupCommand().Flags().Lookup("by").Hidden {
		t.Fatal("group field should be data with a hidden legacy adapter")
	}
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
		if code != 1 || json.Unmarshal([]byte(stdout), &result) != nil || result.Error.Code != "invalid_argument" {
			t.Fatalf("invalid syntax lacks stable JSON error: %v: %s (exit %d)", args, stdout, code)
		}
	}
}
