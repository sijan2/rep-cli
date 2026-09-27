package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/repplus/rep-cli/internal/jevrpc"
)

type decisionBrowserFunc func(context.Context, string, any, any) error

func (f decisionBrowserFunc) Call(ctx context.Context, method string, params, out any) error {
	return f(ctx, method, params, out)
}

func TestHostCapabilityNegotiationPrecedesPrivateCredentials(t *testing.T) {
	var calls []string
	browser := decisionBrowserFunc(func(_ context.Context, method string, params, _ any) error {
		calls = append(calls, method)
		if method != "jev.capabilities" || params != nil {
			t.Fatal("credential-bearing request reached an unverified host")
		}
		return errors.New("old host")
	})
	_, err := hostSelectBatch(context.Background(), browser, jev.Config{APIKey: "must-stay-local"}, []jevdom.Options{{TabID: 12, Goal: "Continue"}})
	if err == nil || len(calls) != 1 {
		t.Fatal("older host did not fail before credentials")
	}
}

func TestHostSelectionBatchRetainsGoalOrderAndUsesOneDecisionRPC(t *testing.T) {
	var decisions int
	browser := decisionBrowserFunc(func(_ context.Context, method string, params, out any) error {
		if method == "jev.capabilities" {
			*out.(*jevrpc.Capabilities) = jevrpc.Capabilities{Version: 1, PersistentSelection: true, BatchSelection: true, MaxBatch: 32}
			return nil
		}
		if method != "jev.select_batch" {
			t.Fatalf("unexpected request: %s", method)
		}
		request := params.(jevrpc.SelectRequest)
		if request.Credentials.APIKey != "unix-only" || request.Options[0].Goal != "Name" || request.Options[1].Goal != "Email" {
			t.Fatal("batch or local configuration was changed")
		}
		decisions++
		*out.(*jevrpc.BatchResult) = jevrpc.BatchResult{Results: []jevdom.Result{{Status: "selected", Confidence: 0.91}, {Status: "no_match", Confidence: 0.92}}}
		return nil
	})
	results, err := hostSelectBatch(context.Background(), browser, jev.Config{APIKey: "unix-only"}, []jevdom.Options{{Goal: "Name"}, {Goal: "Email"}})
	if err != nil || decisions != 1 || len(results) != 2 || results[1].Status != "no_match" {
		t.Fatalf("invalid batch: %v", err)
	}
}

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

func TestInteractionSelectionRetainsFrameAndDocumentBinding(t *testing.T) {
	decision := jevdom.Result{Status: "selected", Selected: &jevdom.Candidate{Name: "Save", Role: "button", BackendDOMNodeID: 8}, Binding: &jevdom.Binding{Generation: "session-generation", FrameID: "child-frame", FrameURL: "https://child.example/form", SessionID: "child-session", DocumentGeneration: "document-generation", SnapshotFingerprint: "scope-fingerprint", Kind: "controls", Origin: "https://root.example", ScopeFrameID: "original-frame", ScopeBackendDOMNodeID: 44}}
	selection := interactionSelection(decision)
	if selection.FrameID != "child-frame" || selection.SessionID != "child-session" || selection.FrameURL != "https://child.example/form" || selection.DocumentGeneration != "document-generation" || selection.Generation != "session-generation" || selection.SnapshotFingerprint != "scope-fingerprint" || selection.BackendDOMNodeID != 8 || selection.Kind != "controls" || selection.Origin != "https://root.example" || selection.ScopeFrameID != "original-frame" || selection.ScopeBackendDOMNodeID != 44 {
		t.Fatal("interaction adapter discarded part of the observed handle")
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
