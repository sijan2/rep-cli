package browserflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/bridge"
)

func observedSelection(goal, frame string) Selection {
	return Selection{Status: "selected", Name: goal, Role: "textbox", BackendDOMNodeID: 42,
		FrameID: frame, FrameURL: "https://child.test/form", SessionID: "session-" + frame,
		DocumentGeneration: "document-" + frame, Generation: "epoch", SnapshotFingerprint: "view", ObservationMode: "compact"}
}

func TestSemanticDefaultSkipUsesResolvedFrame(t *testing.T) {
	for _, scopedURL := range []bool{false, true} {
		for _, completed := range []bool{false, true} {
			t.Run(fmt.Sprintf("url=%t/completed=%t", scopedURL, completed), func(t *testing.T) {
				b := &fakeBrowser{t: t}
				plan := formPlan(false, "Name")
				if scopedURL {
					plan.Steps[0].Target.FrameURL = "https://child.test/form"
				}
				text := "Saved"
				plan.Steps[0].SkipIf = []Condition{{Target: &Target{Selector: "#status"}, Text: &text}}
				checkedChild := false
				b.conditionReply = func(spec runtimeSpec) (string, error) {
					for _, condition := range spec.Step.After {
						if condition.Target == nil {
							continue
						}
						if spec.FrameID != "child" || spec.FrameURL != "https://child.test/form" {
							t.Fatalf("default skip escaped selected document: %+v", spec)
						}
						checkedChild = true
						if !completed {
							return "planned", nil
						}
					}
					return "verified", nil
				}
				engine := Engine{Browser: b, Select: func(context.Context, string, string) (Selection, error) {
					return observedSelection("Name", "child"), nil
				}}
				report, err := engine.Run(context.Background(), 12, plan, true)
				if err != nil || !checkedChild || report.SemanticSelections != 1 {
					t.Fatalf("skip scope was not resolved: %+v %v", report, err)
				}
				if completed && (report.Steps[0].Status != "skipped" || b.mutations != 0) {
					t.Fatalf("completed child action executed: %+v", report)
				}
				if !completed && (report.Steps[0].Status != "verified" || b.mutations != 1) {
					t.Fatalf("root's matching status skipped child action: %+v", report)
				}
			})
		}
	}
}

func TestExistingCrossFrameOutcomeCannotProveActionSuccess(t *testing.T) {
	for _, action := range []string{"click", "fill"} {
		t.Run(action, func(t *testing.T) {
			b := &fakeBrowser{t: t, conditionReply: func(runtimeSpec) (string, error) { return "verified", nil }}
			plan := formPlan(false, "Name")
			plan.Steps[0].Target = &Target{Selector: "#action"}
			plan.Steps[0].Action = action
			if action == "click" {
				plan.Steps[0].Value, plan.Steps[0].Replace = nil, false
			}
			text := "Saved"
			plan.Steps[0].After = []Condition{{Target: &Target{Selector: "#status", FrameID: "child"}, Text: &text}}
			engine := Engine{Browser: b}
			report, err := engine.Run(context.Background(), 12, plan, true)
			if err == nil || b.mutations != 0 || len(report.Steps) != 1 || report.Steps[0].Attempted || report.Steps[0].Code != "postcondition_already_satisfied" {
				t.Fatalf("old child outcome accepted: %+v %v", report, err)
			}
		})
	}
}

func TestCrossFrameOutcomeAllowsAlreadyCorrectValueWithoutInput(t *testing.T) {
	unchanged := false
	b := &fakeBrowser{t: t, previewChanges: &unchanged, conditionReply: func(runtimeSpec) (string, error) { return "verified", nil }}
	plan := formPlan(false, "Name")
	plan.Steps[0].Target = &Target{Selector: "#name"}
	text := "Saved"
	plan.Steps[0].After = []Condition{{Target: &Target{Selector: "#status", FrameID: "child"}, Text: &text}}
	engine := Engine{Browser: b}
	report, err := engine.Run(context.Background(), 12, plan, true)
	if err != nil || report.Steps[0].Status != "verified" || b.mutations != 0 || report.Steps[0].Attempted {
		t.Fatalf("idempotent value rejected: %+v %v", report, err)
	}
}

func TestKeyboardPreDispatchProofDoesNotEraseEarlierInput(t *testing.T) {
	rejection := &bridge.RPCError{Code: "frame_owner_occluded", Data: map[string]any{"phase": "before_dispatch"}}
	for _, test := range []struct {
		name      string
		errors    []error
		wantCalls int
		unknown   bool
	}{
		{"first_key_rejected", []error{rejection}, 1, false},
		{"keyup_rejected", []error{nil, rejection}, 2, true},
		{"lost_keydown_reply", []error{errors.New("reply lost"), nil}, 2, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := &fakeBrowser{t: t, statuses: []string{"prepared"}, keyErrors: test.errors}
			plan := fixturePlan(t)
			plan.Steps = plan.Steps[:1]
			plan.Steps[0].Action, plan.Steps[0].Keys = "press", []string{"Enter"}
			engine := Engine{Browser: b}
			report, err := engine.Run(context.Background(), 12, plan, true)
			if err == nil || b.keyCalls != test.wantCalls {
				t.Fatalf("unexpected key dispatch: %+v %v calls=%d", report, err, b.keyCalls)
			}
			result := report.Steps[0]
			if test.unknown && (result.Status != "unconfirmed" || !result.Attempted || result.Code != "key_outcome_unknown") {
				t.Fatalf("earlier input became safe to replay: %+v", result)
			}
			if !test.unknown && (result.Status != "failed" || result.Attempted || result.Code != "frame_owner_occluded") {
				t.Fatalf("definite first-key refusal is unknown: %+v", result)
			}
		})
	}
}

func formPlan(batch bool, goals ...string) Plan {
	plan := Plan{Version: 1, URL: "https://example.test/form"}
	for i, goal := range goals {
		value := "value"
		step := Step{ID: string(rune('a' + i)), Action: "fill", Target: &Target{Goal: goal}, Value: &value, Replace: true}
		if batch {
			step.Batch = "form"
		}
		plan.Steps = append(plan.Steps, step)
	}
	return plan
}

func TestSemanticFrameRoutesResolveAndActionWithExactDocumentIdentity(t *testing.T) {
	b := &fakeBrowser{t: t}
	e := Engine{Browser: b}
	e.Select = func(context.Context, string, string) (Selection, error) {
		if e.Lease().LeaseID != "execution-lease" {
			t.Fatal("selector did not share execution lease")
		}
		return observedSelection("Name", "child"), nil
	}
	report, err := e.Run(context.Background(), 12, formPlan(false, "Name"), true)
	if err != nil || report.Status != "verified" || b.mutations != 1 {
		t.Fatalf("frame action failed: %+v %v", report, err)
	}
	for _, route := range b.routes {
		method := route["method"]
		if method == "DOM.resolveNode" || method == "Runtime.callFunctionOn" {
			if route["frame_id"] != "child" || route["session_id"] != "session-child" || route["document_generation"] != "document-child" || route["generation"] != "epoch" || route["lease_id"] != "execution-lease" {
				t.Fatalf("identity lost: %#v", route)
			}
			if route["expected_root_url"] != "https://example.test/form" || route["expected_frame_url"] != "https://child.test/form" {
				t.Fatalf("URL guards missing: %#v", route)
			}
		}
	}
	if len(b.validations) != 1 || b.validations[0]["frame_id"] != "" {
		t.Fatal("global selection was incorrectly narrowed during validation")
	}
	if len(b.performed) != 1 || b.performed[0].ExpectedPoint == nil {
		t.Fatal("frame input lacked checked ancestor coordinates")
	}
}

func TestExplicitFrameTargetNeedsNoModel(t *testing.T) {
	b := &fakeBrowser{t: t}
	plan := formPlan(false, "Name")
	plan.Steps[0].Target = &Target{Selector: "#name", FrameID: "child", FrameURL: "https://child.test/form"}
	e := Engine{Browser: b}
	report, err := e.Run(context.Background(), 12, plan, true)
	if err != nil || report.SemanticSelections != 0 || b.mutations != 1 {
		t.Fatalf("exact frame target failed: %+v %v", report, err)
	}
	if b.performed[0].FrameID != "child" || b.performed[0].FrameURL != "https://child.test/form" {
		t.Fatal("exact frame scope lost")
	}
}

func TestIndependentTargetsShareSelectionButValidateEachAction(t *testing.T) {
	b := &fakeBrowser{t: t}
	e := Engine{Browser: b}
	batches := 0
	e.SelectBatch = func(_ context.Context, goals []string, page string) ([]Selection, error) {
		batches++
		if len(goals) != 3 || page != "https://example.test/form" {
			t.Fatalf("unexpected batch %#v", goals)
		}
		selections := make([]Selection, len(goals))
		for i, goal := range goals {
			selections[i] = observedSelection(goal, "child")
		}
		return selections, nil
	}
	report, err := e.Run(context.Background(), 12, formPlan(true, "Name", "Email", "Phone"), true)
	if err != nil || batches != 1 || b.mutations != 3 || report.SemanticSelections != 3 || report.BindingReuses != 2 {
		t.Fatalf("batch failed: %+v %v batches=%d", report, err, batches)
	}
	if len(b.validations) < 3 {
		t.Fatal("batch skipped per-action dependency checks")
	}
}

func TestDependentChangeInvalidatesPendingBatchBeforeInput(t *testing.T) {
	b := &fakeBrowser{t: t, freshness: []bool{true, false, true, true, true}}
	e := Engine{Browser: b}
	var batchSizes []int
	e.SelectBatch = func(_ context.Context, goals []string, _ string) ([]Selection, error) {
		batchSizes = append(batchSizes, len(goals))
		selections := make([]Selection, len(goals))
		for i, goal := range goals {
			selections[i] = observedSelection(goal, "child")
		}
		return selections, nil
	}
	report, err := e.Run(context.Background(), 12, formPlan(true, "Country", "State", "City"), true)
	if err != nil || b.mutations != 3 || len(batchSizes) != 2 || batchSizes[0] != 3 || batchSizes[1] != 2 || report.SemanticSelections != 5 {
		t.Fatalf("stale batch reused: %+v %v batches=%v", report, err, batchSizes)
	}
}

func TestGeometryChangeCannotUsePreviouslyValidSemanticDecision(t *testing.T) {
	b := &fakeBrowser{t: t, freshness: []bool{false}}
	e := Engine{Browser: b, Select: func(context.Context, string, string) (Selection, error) {
		return observedSelection("Name", "child"), nil
	}}
	report, err := e.Run(context.Background(), 12, formPlan(false, "Name"), true)
	if err == nil || b.mutations != 0 || report.Steps[0].Code != "semantic_stale_before_action" {
		t.Fatalf("stale action dispatched: %+v %v", report, err)
	}
}

func TestUnchangedBindingReusesExactGoalWithinRun(t *testing.T) {
	b := &fakeBrowser{t: t}
	selections := 0
	e := Engine{Browser: b, Select: func(context.Context, string, string) (Selection, error) {
		selections++
		return observedSelection("Name", "child"), nil
	}}
	report, err := e.Run(context.Background(), 12, formPlan(false, "Name", "Name"), true)
	if err != nil || selections != 1 || b.mutations != 2 || report.BindingReuses != 1 {
		t.Fatalf("binding reuse failed: %+v %v", report, err)
	}
}

func TestScopedBatchDoesNotCollapseSameGoalInDifferentFrames(t *testing.T) {
	b := &fakeBrowser{t: t}
	plan := formPlan(true, "Name", "Name")
	plan.Steps[0].Target.FrameID = "left"
	plan.Steps[1].Target.FrameID = "right"
	e := Engine{Browser: b, SelectBatchScoped: func(_ context.Context, targets []Target, _ string) ([]Selection, error) {
		if len(targets) != 2 || targets[0].FrameID != "left" || targets[1].FrameID != "right" {
			t.Fatalf("frame scopes collapsed: %+v", targets)
		}
		values := make([]Selection, len(targets))
		for i, target := range targets {
			values[i] = observedSelection(target.Goal, target.FrameID)
			values[i].ScopeFrameID = target.FrameID
		}
		return values, nil
	}}
	report, err := e.Run(context.Background(), 12, plan, true)
	if err != nil || b.mutations != 2 || report.SemanticSelections != 2 {
		t.Fatalf("scoped batch failed: %+v %v", report, err)
	}
	if b.validations[0]["frame_id"] != "left" || b.validations[len(b.validations)-1]["frame_id"] != "right" {
		t.Fatal("original observation scopes not validated")
	}
}

func TestPostconditionsRouteRootURLAndExplicitChildIndependently(t *testing.T) {
	b := &fakeBrowser{t: t, statuses: []string{"performed"}}
	plan := formPlan(false, "Name")
	text := "Saved"
	plan.Steps[0].After = []Condition{{URL: "https://example.test/done"}, {Target: &Target{Selector: "#status", FrameID: "result", FrameURL: "https://child.test/done"}, Text: &text}}
	e := Engine{Browser: b, Select: func(context.Context, string, string) (Selection, error) {
		return observedSelection("Name", "child"), nil
	}}
	report, err := e.Run(context.Background(), 12, plan, true)
	if err != nil || report.Status != "verified" {
		t.Fatalf("frame postconditions failed: %+v %v", report, err)
	}
	seen := false
	for _, route := range b.routes {
		if route["frame_id"] == "result" && route["expected_root_url"] == "https://example.test/done" {
			seen = true
			if route["expected_frame_url"] != "https://child.test/done" || route["document_generation"] != nil || route["session_id"] != nil {
				t.Fatalf("outcome reused old document: %#v", route)
			}
		}
	}
	if !seen {
		t.Fatal("child outcome was never checked")
	}
}

func TestLegacyBindingNeverUsesCompactHashOrRoutes(t *testing.T) {
	b := &fakeBrowser{t: t}
	e := Engine{Browser: b, Select: func(context.Context, string, string) (Selection, error) {
		s := observedSelection("Name", "child")
		s.ObservationMode = "legacy"
		return s, nil
	}}
	report, err := e.Run(context.Background(), 12, formPlan(false, "Name"), true)
	if err == nil || b.mutations != 0 || len(b.validations) != 0 || report.Steps[0].Code != "semantic_legacy_binding_requires_compact" {
		t.Fatalf("legacy binding crossed contracts: %+v %v", report, err)
	}
}

func TestExplicitSemanticNameUsesAXBeforeResolving(t *testing.T) {
	b := &fakeBrowser{t: t}
	plan := formPlan(false, "Name")
	plan.Steps[0].Target.Name = "Different name"
	e := Engine{Browser: b, Select: func(context.Context, string, string) (Selection, error) {
		return observedSelection("Name", "child"), nil
	}}
	report, err := e.Run(context.Background(), 12, plan, true)
	if err == nil || b.mutations != 0 || report.Steps[0].Code != "semantic_name_mismatch" {
		t.Fatalf("AX name requirement ignored: %+v %v", report, err)
	}
	if strings.Contains(strings.Join(b.calls, ","), "DOM.resolveNode") {
		t.Fatal("invalid AX target resolved")
	}
}

func TestProvenPreDispatchRefusalIsNotAnUnknownMutation(t *testing.T) {
	for _, phase := range []string{"before_dispatch", ""} {
		t.Run(phase, func(t *testing.T) {
			b := &fakeBrowser{t: t, performError: &bridge.RPCError{Code: "frame_owner_occluded", Message: "ancestor guard rejected", Data: map[string]any{"phase": phase}}}
			plan := formPlan(false, "Name")
			plan.Steps[0].Target = &Target{Selector: "#name", FrameID: "child"}
			engine := Engine{Browser: b}
			report, err := engine.Run(context.Background(), 12, plan, true)
			if err == nil || len(report.Steps) != 1 || b.mutations != 0 {
				t.Fatalf("unexpected result: %+v %v", report, err)
			}
			result := report.Steps[0]
			if phase == "before_dispatch" {
				if result.Status != "failed" || result.Attempted || result.Code != "frame_owner_occluded" {
					t.Fatalf("proven rejection reported unknown: %+v", result)
				}
			} else if result.Status != "unconfirmed" || !result.Attempted || result.Code != "action_outcome_unknown" {
				t.Fatalf("unproven error became retryable: %+v", result)
			}
		})
	}
}
