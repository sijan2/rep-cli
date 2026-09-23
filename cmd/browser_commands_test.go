package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/spf13/cobra"
)

func TestEvaluationCommandsKeepIndependentDefaultsAndLegacyFlags(t *testing.T) {
	var payloads []map[string]interface{}
	var deadlines []time.Duration
	run := func(_ *cobra.Command, _ string, _ string, params map[string]interface{}, timeout time.Duration) error {
		payloads = append(payloads, params)
		deadlines = append(deadlines, timeout)
		return nil
	}
	customized := newBrowserEvaluationCommand(true, run)
	customized.SetArgs([]string{"document.title", "--tab", "7", "--await=false", "--by-value=false", "--settle", "10ms", "--max-result", "2048"})
	customized.SetOut(&bytes.Buffer{})
	customized.SetErr(&bytes.Buffer{})
	if err := customized.Execute(); err != nil {
		t.Fatal(err)
	}
	normal := newBrowserEvaluationCommand(true, run)
	normal.SetArgs([]string{"document.title", "--tab", "8"})
	normal.SetOut(&bytes.Buffer{})
	normal.SetErr(&bytes.Buffer{})
	if err := normal.Execute(); err != nil {
		t.Fatal(err)
	}
	if payloads[0]["await_promise"] != false || payloads[0]["return_by_value"] != false || payloads[0]["settle_ms"] != int64(10) || payloads[0]["max_result_bytes"] != 2048 {
		t.Fatal("advanced compatibility flags changed behavior")
	}
	if payloads[1]["await_promise"] != true || payloads[1]["return_by_value"] != true || payloads[1]["settle_ms"] != int64(1500) || payloads[1]["max_result_bytes"] != 65536 {
		t.Fatal("one command contaminated another's defaults")
	}
	if deadlines[0] != 40*time.Second || deadlines[1] != 40*time.Second {
		t.Fatal("capture handoff grace changed")
	}
}
func TestRawJSONAndEnvelopeEachSelectJSONWithoutSecondFlag(t *testing.T) {
	oldMode, oldJSON, oldRaw, oldEnvelope := outputMode, jsonOutput, rawJSON, forceEnvelope
	defer func() { outputMode, jsonOutput, rawJSON, forceEnvelope = oldMode, oldJSON, oldRaw, oldEnvelope }()
	outputMode = "compact"
	jsonOutput = false
	rawJSON = true
	forceEnvelope = false
	if getOutputMode() != "json" || useEnvelope() {
		t.Fatal("raw-json still needs a second output flag")
	}
	rawJSON = false
	forceEnvelope = true
	if getOutputMode() != "json" || !useEnvelope() {
		t.Fatal("envelope flag did not select JSON")
	}
}
func TestInteractionAcceptsPositionalPlanAndRejectsConflictingInputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"url":"https://example.test/","steps":[{"id":"name","action":"fill","target":{"name":"Name"},"value":"test"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, conflict := range []bool{false, true} {
		called := false
		command := newBrowserInteractCommand(jevDOMDependencies{browser: func(context.Context, string) (jevdom.Browser, error) {
			called = true
			return nil, errors.New("fixture connection")
		}})
		args := []string{path, "--tab", "7"}
		if conflict {
			args = append(args, "--plan", path)
		}
		command.SetArgs(args)
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		if err := command.Execute(); err == nil {
			t.Fatal("fixture unexpectedly connected")
		}
		if called == conflict {
			t.Fatal("positional plan was not validated before connecting")
		}
	}
}
