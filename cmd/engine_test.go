package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/engine"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/spf13/cobra"
)

func engineTestCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	previousJSON, previousRaw, previousEnvelope := jsonOutput, rawJSON, forceEnvelope
	jsonOutput, rawJSON, forceEnvelope = true, false, true
	t.Cleanup(func() { jsonOutput, rawJSON, forceEnvelope = previousJSON, previousRaw, previousEnvelope })
	root := &cobra.Command{Use: "rep", SilenceErrors: true, SilenceUsage: true}
	root.AddCommand(engineNewCommand())
	var buffer bytes.Buffer
	root.SetOut(&buffer)
	root.SetErr(&buffer)
	root.SetArgs(append([]string{"engine"}, args...))
	err := root.Execute()
	return buffer.String(), err
}

func TestEngineLocalCommandsDoNotCallModels(t *testing.T) {
	for _, command := range []string{"list", "check"} {
		t.Run(command, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			t.Setenv("JEV", "")
			t.Setenv("JEV_API_KEY", "")
			t.Setenv("TYPESAFE_API_KEY", "")
			t.Setenv("JEV_ENV_FILE", filepath.Join(dir, "missing.env"))
			t.Setenv("JEV_MODEL", "")
			text, err := engineTestCommand(t, command)
			if err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				Command string `json:"command"`
				Data    struct {
					Engines   []engine.EngineInfo `json:"engines"`
					ModelCall bool                `json:"model_call"`
				} `json:"data"`
			}
			if sonic.UnmarshalString(text, &envelope) != nil || envelope.Command != "engine "+command || len(envelope.Data.Engines) != 2 || envelope.Data.ModelCall {
				t.Fatalf("invalid local listing: %s", text)
			}
			for _, info := range envelope.Data.Engines {
				if info.Available || info.Reason == "" || len(info.Models) == 0 {
					t.Fatalf("unavailable backend not described: %+v", info)
				}
			}
		})
	}
}

func TestEngineRunCommandErrors(t *testing.T) {
	input := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(input, []byte(`{"proposal":{"engine":"codex","model":"gpt-6.1-sol","oracle":"object_reference"},"evidence":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		code string
	}{
		{"independence", []string{"--job", "adjudicate", "--engine", "codex", "--model", "gpt-6-luna", "--input", "@" + input}, "engine_independence_required"},
		{"input notation", []string{"--job", "triage", "--input", input}, "engine_invalid_input"},
		{"missing input file", []string{"--job", "triage", "--input", "@" + input + "-absent"}, "engine_invalid_input"},
		{"input bound", []string{"--job", "triage", "--input", "@" + input, "--max-input-bytes", "1"}, "engine_input_limit"},
		{"timeout bound", []string{"--job", "triage", "--input", "@" + input, "--timeout", "5m"}, "engine_invalid_limits"},
		{"unknown task", []string{"--job", "prompt", "--input", "@" + input}, "engine_invalid_task"},
		{"unknown engine", []string{"--job", "triage", "--input", "@" + input, "--engine", "other"}, "engine_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, err := engineTestCommand(t, append([]string{"run"}, tc.args...)...)
			var decoded struct {
				Error output.AgentError `json:"error"`
			}
			if err == nil || sonic.UnmarshalString(text, &decoded) != nil || decoded.Error.Code != tc.code || !output.Reported(err) {
				t.Fatalf("wrong CLI error: %s %v", text, err)
			}
			if tc.name == "independence" && !strings.Contains(decoded.Error.Message, "independence") {
				t.Fatalf("independence reason omitted: %s", text)
			}
		})
	}
}

func TestEngineRunCommandTagsAndRedacts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX subprocess fixture")
	}
	for _, tc := range []struct {
		name, response string
		inconclusive   bool
	}{
		{"proposal", `{"proposals":[{"rank":1,"family_id":"family_1","oracle":"object_reference","hypothesis":"Access may cross principals.","next_test":"Compare access to a seeded object."}]}`, false},
		{"invalid answer", `{"proposals":[{"rank":1,"family_id":"family_1","oracle":"invented","hypothesis":"Impact.","next_test":"Guess."}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, "codex-unm")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\n/bin/cat >/dev/null\nprintf '%s' '"+tc.response+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			input := filepath.Join(dir, "input.json")
			if err := os.WriteFile(input, []byte(`{"headers":{"Authorization":"Bearer NEVER_FORWARD_THIS"},"families":[{"family_id":"family-local","method":"GET","route":"/api/orders/123","sample_count":2,"status_codes":[200],"signals":["object_reference"]}]}`), 0600); err != nil {
				t.Fatal(err)
			}
			text, err := engineTestCommand(t, "run", "--job", "triage", "--input", "@"+input)
			var envelope struct {
				Data engine.EngineResult `json:"data"`
			}
			if err != nil || sonic.UnmarshalString(text, &envelope) != nil || envelope.Data.Assessment != "model_assessed" || envelope.Data.Timestamp.IsZero() || envelope.Data.Engine != "codex" || envelope.Data.Sanitization.HeadersDropped != 1 || strings.Contains(text, "NEVER_FORWARD_THIS") {
				t.Fatalf("incorrect tagged/scrubbed CLI output: %s %v", text, err)
			}
			if tc.inconclusive != (envelope.Data.Verdict == engine.EngineNeedsMoreEvidence) {
				t.Fatalf("wrong fail-closed verdict: %s", text)
			}
		})
	}
}
