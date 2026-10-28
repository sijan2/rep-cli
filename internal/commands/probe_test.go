package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/probe"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
)

type probeTestEnvelope[T any] struct {
	Command    string                 `json:"command"`
	Source     string                 `json:"source"`
	Filters    map[string]interface{} `json:"filters"`
	Truncation *output.TruncationInfo `json:"truncation"`
	Suggest    []string               `json:"suggest"`
	Data       T                      `json:"data"`
}

func probeTestFixture(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("REP_WORKSPACE", "probe-tests")
	t.Setenv("REP_TASK", "frontier")
	t.Setenv("REPLIVE_PATH", "")
	t.Setenv("REPANDROID_PATH", "")
	scope.Reset()
	t.Cleanup(scope.Reset)
	oldMode, oldJSON, oldRaw, oldEnvelope := outputMode, jsonOutput, rawJSON, forceEnvelope
	outputMode, jsonOutput, rawJSON, forceEnvelope = "json", false, false, false
	t.Cleanup(func() { outputMode, jsonOutput, rawJSON, forceEnvelope = oldMode, oldJSON, oldRaw, oldEnvelope })
	selected, err := scope.Configure(scope.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(selected.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	export := store.Export{Version: "1", Requests: []store.Request{
		{ID: "h_probe_source", Method: "GET", URL: "https://example.test/users/123?owner=42", Body: "request-body-secret",
			Headers:  store.HeaderMap{"authorization": {"Bearer credential-secret"}},
			Response: &store.Response{Status: 200, Body: "response-body-secret"}},
		{ID: "h_probe_recapture", Method: "GET", URL: "https://EXAMPLE.test:443/users/999?owner=84", Response: &store.Response{Status: 403}},
		{ID: "h_probe_other", Method: "POST", URL: "https://example.test/settings", Response: &store.Response{Status: 200}},
	}}
	data, err := sonic.Marshal(export)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(selected.LivePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	return selected.DataDir
}

func probeTestExecute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	command := probeNewCommand()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SilenceUsage, command.SilenceErrors = true, true
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}

func probeTestJSON[T any](t *testing.T, raw string) probeTestEnvelope[T] {
	t.Helper()
	var envelope probeTestEnvelope[T]
	if err := sonic.UnmarshalString(raw, &envelope); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, raw)
	}
	if envelope.Source != probe.ProbeLedgerName || envelope.Filters["workspace"] != "probe-tests" || envelope.Filters["task"] != "frontier" {
		t.Fatalf("missing source or task envelope: %+v", envelope)
	}
	return envelope
}

func probeTestAddCLI(t *testing.T, oracle probe.ProbeOracleKind, extra ...string) probeAddResult {
	t.Helper()
	args := []string{"add", "--hypothesis", "A client-supplied owner reference may expose another principal's profile.", "--oracle", string(oracle), "--ref", "h_probe_source"}
	raw, err := probeTestExecute(t, append(args, extra...)...)
	if err != nil {
		t.Fatalf("add: %v\n%s", err, raw)
	}
	return probeTestJSON[probeAddResult](t, raw).Data
}

func probeTestError(t *testing.T, args []string, code string) {
	t.Helper()
	raw, err := probeTestExecute(t, args...)
	var payload struct {
		Error output.AgentError `json:"error"`
	}
	if err == nil || sonic.UnmarshalString(raw, &payload) != nil || payload.Error.Code != code || !output.Reported(err) {
		t.Fatalf("%v: expected reported %s, got %v, %s", args, code, err, raw)
	}
}

func TestProbeCLIFrontierConverges(t *testing.T) {
	for _, tc := range []struct {
		verdict string
		state   probe.ProbeState
	}{
		{"pass", probe.ProbeVerified}, {"fail", probe.ProbeRefuted}, {"inconclusive", probe.ProbeInconclusive},
	} {
		t.Run(tc.verdict, func(t *testing.T) {
			dir := probeTestFixture(t)
			added := probeTestAddCLI(t, probe.ProbeAuthzUnauthenticated)
			if !added.Created || added.Probe.State != probe.ProbeUntested {
				t.Fatalf("bad initial proposal: %+v", added)
			}
			raw, err := probeTestExecute(t, "next", "--limit", "1")
			if err != nil {
				t.Fatal(err)
			}
			batch := probeTestJSON[probe.ProbeBatch](t, raw)
			if len(batch.Data.Probes) != 1 || batch.Data.Probes[0].ID != added.Probe.ID || len(batch.Suggest) == 0 ||
				!strings.Contains(batch.Suggest[0], "replay h_probe_source --baseline 5 --as anon") {
				t.Fatalf("bad frontier or non-runnable next step: %+v", batch)
			}
			again, err := probeTestExecute(t, "next", "--limit", "1")
			if err != nil || again != raw {
				t.Fatalf("next dequeued or changed an unrecorded batch: %v\n%s", err, again)
			}
			raw, err = probeTestExecute(t, "record", added.Probe.ID, "--verdict", tc.verdict, "--evidence", "run_fixture/op_trial", "--note", "Named oracle assessed against stable control.")
			if err != nil {
				t.Fatal(err)
			}
			recorded := probeTestJSON[probe.Probe](t, raw).Data
			if recorded.State != tc.state || recorded.Assessment != probe.ProbeModelAssessed || recorded.OracleKind != added.Probe.OracleKind {
				t.Fatalf("incorrect recorded verdict: %+v", recorded)
			}
			duplicate := probeTestAddCLI(t, probe.ProbeAuthzUnauthenticated, "--ref", "h_probe_recapture", "--hypothesis", "Rewording the same experiment cannot reopen it.")
			if duplicate.Created || !reflect.DeepEqual(duplicate.Probe, recorded) {
				t.Fatalf("recorded experiment was re-proposed: %+v", duplicate)
			}
			raw, err = probeTestExecute(t, "next")
			if err != nil {
				t.Fatal(err)
			}
			frontier := probeTestJSON[probe.ProbeBatch](t, raw).Data
			if !frontier.FrontierEmpty || len(frontier.Probes) != 0 {
				t.Fatalf("recorded experiment reappeared: %+v", frontier)
			}
			raw, err = probeTestExecute(t, "coverage")
			if err != nil {
				t.Fatal(err)
			}
			coverage := probeTestJSON[probe.ProbeCoverage](t, raw).Data
			if coverage.Total != 1 || coverage.ByState[tc.state] != 1 || coverage.ByState[probe.ProbeUntested] != 0 ||
				coverage.ByOracle[probe.ProbeAuthzUnauthenticated].ByState[tc.state] != 1 || !strings.Contains(coverage.Interpretation, "not proof") {
				t.Fatalf("incorrect coverage: %+v", coverage)
			}
			raw, err = probeTestExecute(t, "show", added.Probe.ID, "--limit", "1")
			if err != nil {
				t.Fatal(err)
			}
			shown := probeTestJSON[probe.ProbeDetail](t, raw)
			if shown.Data.Probe.ID != added.Probe.ID || shown.Data.TotalEvents != 2 || shown.Data.OmittedEvents != 1 || shown.Truncation == nil {
				t.Fatalf("incorrect show history: %+v", shown)
			}
			data, err := os.ReadFile(filepath.Join(dir, probe.ProbeLedgerName))
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"request-body-secret", "response-body-secret", "credential-secret", "executed_and_observed"} {
				if bytes.Contains(data, []byte(secret)) || strings.Contains(raw, secret) {
					t.Fatalf("journal or output contains secret/body/unearned measurement: %s", secret)
				}
			}
		})
	}
}

func TestProbeCLIAllCommandsValidateInputs(t *testing.T) {
	probeTestFixture(t)
	added := probeTestAddCLI(t, probe.ProbeInputReflection)
	for _, tc := range []struct {
		name string
		args []string
		code string
	}{
		{"add needs flags", []string{"add"}, "invalid_argument"},
		{"add rejects oracle", []string{"add", "--hypothesis", "Test.", "--oracle", "http_200", "--ref", "h_probe_source"}, "invalid_argument"},
		{"add missing ref", []string{"add", "--hypothesis", "Test.", "--oracle", "input_reflection", "--ref", "missing"}, "request_not_found"},
		{"add no positional args", []string{"add", "extra"}, "invalid_argument"},
		{"next zero limit", []string{"next", "--limit", "0"}, "invalid_argument"},
		{"next too large", []string{"next", "--limit", "101"}, "invalid_argument"},
		{"next oracle", []string{"next", "--oracle", "invention"}, "invalid_argument"},
		{"next args", []string{"next", "extra"}, "invalid_argument"},
		{"record no ID", []string{"record"}, "invalid_argument"},
		{"record no evidence", []string{"record", added.Probe.ID, "--verdict", "pass"}, "invalid_argument"},
		{"record inline body", []string{"record", added.Probe.ID, "--verdict", "pass", "--evidence", `{"status":200}`}, "invalid_argument"},
		{"record wrong verdict", []string{"record", added.Probe.ID, "--verdict", "success", "--evidence", "run_trial"}, "invalid_argument"},
		{"record unknown ID", []string{"record", "missing", "--verdict", "pass", "--evidence", "run_trial"}, "probe_not_found"},
		{"record cannot reset", []string{"record", added.Probe.ID, "--state", "untested", "--evidence", "run_trial"}, "invalid_argument"},
		{"record needs blocked reason", []string{"record", added.Probe.ID, "--state", "blocked", "--evidence", "run_trial"}, "invalid_argument"},
		{"record conflicting states", []string{"record", added.Probe.ID, "--state", "blocked", "--verdict", "pass", "--evidence", "run_trial"}, "invalid_argument"},
		{"coverage negative limit", []string{"coverage", "--limit", "-1"}, "invalid_argument"},
		{"coverage args", []string{"coverage", "extra"}, "invalid_argument"},
		{"show no ID", []string{"show"}, "invalid_argument"},
		{"show unknown ID", []string{"show", "missing"}, "probe_not_found"},
		{"show bad limit", []string{"show", added.Probe.ID, "--limit", "101"}, "invalid_argument"},
	} {
		t.Run(tc.name, func(t *testing.T) { probeTestError(t, tc.args, tc.code) })
	}
	for _, tc := range []struct {
		state probe.ProbeState
		note  string
	}{
		{probe.ProbeAttempted, "Execution pending."}, {probe.ProbeBlocked, "Second principal unavailable."},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			raw, err := probeTestExecute(t, "record", added.Probe.ID, "--state", string(tc.state), "--evidence", "op_prerequisite", "--note", tc.note)
			if err != nil {
				t.Fatal(err)
			}
			if p := probeTestJSON[probe.Probe](t, raw).Data; p.State != tc.state || p.Note != tc.note {
				t.Fatalf("lost prerequisite state: %+v", p)
			}
		})
	}
}

func TestProbeCLIBoundsFiltersAndText(t *testing.T) {
	probeTestFixture(t)
	for _, kind := range []probe.ProbeOracleKind{probe.ProbeRateOrLockout, probe.ProbeAuthzCrossPrincipal, probe.ProbeInformationDisclosure} {
		probeTestAddCLI(t, kind)
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"next", []string{"next", "--limit", "1"}},
		{"coverage", []string{"coverage", "--limit", "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := probeTestExecute(t, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			envelope := probeTestJSON[map[string]interface{}](t, raw)
			if envelope.Truncation == nil || envelope.Truncation.Returned != 1 || envelope.Truncation.Total != 3 {
				t.Fatalf("missing omission accounting: %+v", envelope)
			}
		})
	}
	raw, err := probeTestExecute(t, "next", "--oracle", "rate_or_lockout")
	if err != nil {
		t.Fatal(err)
	}
	filtered := probeTestJSON[probe.ProbeBatch](t, raw)
	if len(filtered.Data.Probes) != 1 || filtered.Data.Probes[0].OracleKind != probe.ProbeRateOrLockout || filtered.Filters["oracle"] != "rate_or_lockout" {
		t.Fatalf("wrong filter result: %+v", filtered)
	}
	outputMode = "compact"
	for _, args := range [][]string{{"next", "--limit", "1"}, {"coverage"}} {
		raw, err := probeTestExecute(t, args...)
		if err != nil || !strings.Contains(raw, "Suggest: rep --workspace probe-tests --task frontier") || !strings.Contains(raw, "untested") {
			t.Fatalf("missing text projection or executable suggestion: %v\n%s", err, raw)
		}
	}
}

func TestProbeCLIRequiresExplicitIsolatedTask(t *testing.T) {
	dir := probeTestFixture(t)
	added := probeTestAddCLI(t, probe.ProbeInputReflection)
	for _, options := range []scope.Options{
		{Global: true}, {Workspace: "probe-tests", TaskSet: false},
	} {
		t.Setenv("REP_TASK", "")
		if _, err := scope.Configure(options); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"add", "--hypothesis", "Test.", "--oracle", "input_reflection", "--ref", "h_probe_source"},
			{"next"}, {"coverage"}, {"show", added.Probe.ID},
			{"record", added.Probe.ID, "--verdict", "fail", "--evidence", "run_trial"},
		} {
			probeTestError(t, args, "scope_required")
		}
	}
	if _, err := scope.Configure(scope.Options{Workspace: "probe-tests", Task: "other-agent"}); err != nil {
		t.Fatal(err)
	}
	raw, err := probeTestExecute(t, "coverage")
	if err != nil {
		t.Fatal(err)
	}
	var other probeTestEnvelope[probe.ProbeCoverage]
	if err := sonic.UnmarshalString(raw, &other); err != nil || other.Data.Total != 0 || other.Filters["task"] != "other-agent" {
		t.Fatalf("task isolation failed: %v\n%s", err, raw)
	}
	probeTestError(t, []string{"show", added.Probe.ID}, "probe_not_found")
	if data, err := os.ReadFile(filepath.Join(dir, probe.ProbeLedgerName)); err != nil || bytes.Count(data, []byte{'\n'}) != 1 {
		t.Fatalf("another task changed the first task's ledger: %v", err)
	}
}

func TestProbeCLISuggestionsUseRealPrincipalsAndShellQuoting(t *testing.T) {
	dir := probeTestFixture(t)
	identities := map[string]identityRecord{
		"alice": {Name: "alice", Fingerprint: "first", Headers: map[string]string{"authorization": "credential-secret"}},
		"bob":   {Name: "bob", Fingerprint: "second", Headers: map[string]string{"authorization": "another-secret"}},
	}
	data, err := sonic.Marshal(identities)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "identities.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	probeTestAddCLI(t, probe.ProbeAuthzCrossPrincipal)
	raw, err := probeTestExecute(t, "next")
	if err != nil {
		t.Fatal(err)
	}
	envelope := probeTestJSON[probe.ProbeBatch](t, raw)
	if !strings.Contains(envelope.Suggest[0], "replay h_probe_source --baseline 5 --as alice,bob") || strings.Contains(raw, "secret") {
		t.Fatalf("suggestion used invented identities or exposed secrets: %s", raw)
	}
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"alice,bob", "alice,bob"},
		{"$(touch /tmp/injected)", "'$(touch /tmp/injected)'"},
		{"alice'bob", "'alice'\"'\"'bob'"},
		{"a; echo x", "'a; echo x'"},
	} {
		if line := probeCommandLine("replay", "h_probe_source", "--as", tc.input); !strings.HasSuffix(line, "--as "+tc.want) {
			t.Fatalf("unsafe shell quoting for %q: %s", tc.input, line)
		}
	}
}
