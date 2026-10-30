//go:build replab

package commands

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
	"os"
	"path/filepath"
	"testing"
)

func TestBatchCommandRejectsInvalidInputBeforeBrowserOrCredentials(t *testing.T) {
	for _, input := range []string{
		`{"version":1,"goals":[{"id":"a","goal":"Name"},{"id":"a","goal":"Email"}]}`,
		`{"version":1,"goals":[{"id":"a","goal":"Name","value":"private"}]}`,
		`{"version":1,"goals":[{"id":"a","goal":""}]}`,
	} {
		file := filepath.Join(t.TempDir(), "goals.json")
		if err := os.WriteFile(file, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		command := newBrowserSelectBatchCommand(jevDOMDependencies{loadConfig: func() (jev.Config, error) { t.Fatal("invalid goals reached configuration"); return jev.Config{}, nil }})
		command.SetArgs([]string{file, "--tab", "12"})
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		if command.Execute() == nil {
			t.Fatal("invalid goals were accepted")
		}
	}
}

func TestObservationProjectionKeepsFullFingerprintWhenOutputIsBounded(t *testing.T) {
	browser := decisionBrowserFunc(func(_ context.Context, method string, params, out any) error {
		if method != "browser.observe" {
			t.Fatal("observation requested inference or mutation")
		}
		wire := `{"schema":1,"generation":"g","fingerprint":"full-page-fingerprint","candidates":[{"id":"a"},{"id":"b"}],"coverage":{"total_candidates":2}}`
		return json.Unmarshal([]byte(wire), out)
	})
	command := newBrowserObservationCommand(false, jevDOMDependencies{browser: func(context.Context, string) (jevdom.Browser, error) { return browser, nil }})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"--tab", "12", "--limit", "1"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if wrapped, ok := decoded["data"].(map[string]any); ok {
		decoded = wrapped
	}
	if decoded["fingerprint"] != "full-page-fingerprint" || len(decoded["candidates"].([]any)) != 1 || decoded["coverage"].(map[string]any)["output_truncated"] != true {
		t.Fatal("bounded projection lost full observation identity or omitted-count signal")
	}
}

func TestTruncatedObservationDeltaCannotAdvance(t *testing.T) {
	browser := decisionBrowserFunc(func(_ context.Context, method string, params, out any) error {
		p := params.(map[string]any)
		if method != "browser.observe" || p["since"] != "before" || p["root_backend_dom_node_id"] != int64(44) || p["frame_id"] != "frame" {
			t.Fatalf("scope lost: %#v", p)
		}
		return json.Unmarshal([]byte(`{"schema":1,"full":false,"generation":"g","fingerprint":"after","delta":{"base_fingerprint":"before","upserted":[{"id":"a"},{"id":"b"}],"removed":["c"]},"coverage":{"total_candidates":2}}`), out)
	})
	command := newBrowserObservationCommand(false, jevDOMDependencies{browser: func(context.Context, string) (jevdom.Browser, error) { return browser, nil }})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"--tab", "12", "--frame", "frame", "--root-node", "44", "--since", "before", "--limit", "1"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(output.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if wrapped, ok := value["data"].(map[string]any); ok {
		value = wrapped
	}
	delta := value["delta"].(map[string]any)
	if delta["can_advance"] != false || delta["truncated"] != true || len(delta["upserted"].([]any)) != 1 || delta["base_fingerprint"] != "before" || value["fingerprint"] != "after" {
		t.Fatalf("unsafe delta: %#v", value)
	}
}
