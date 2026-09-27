package browserflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeBrowser struct {
	t              *testing.T
	statuses       []string
	calls          []string
	mutations      int
	already        bool
	failPerform    bool
	performError   error
	failWaitOnce   bool
	failConditions bool
	freshness      []bool
	routes         []map[string]any
	performed      []runtimeSpec
	validations    []map[string]any
	conditionReply func(runtimeSpec) (string, error)
	keyErrors      []error
	keyCalls       int
	previewChanges *bool
}

func (b *fakeBrowser) Call(_ context.Context, method string, args any, out any) error {
	b.calls = append(b.calls, method)
	value := any(map[string]any{})
	switch method {
	case "browser.lease.acquire":
		if b.already {
			return errors.New("tab_busy")
		}
		value = map[string]any{"lease_id": "execution-lease", "generation": "epoch"}
	case "browser.lease.release":
	case "browser.validate":
		b.validations = append(b.validations, args.(map[string]any))
		fresh := true
		if len(b.freshness) > 0 {
			fresh = b.freshness[0]
			b.freshness = b.freshness[1:]
		}
		value = map[string]any{"fresh": fresh}
	case "browser.cdp":
		p := args.(map[string]any)
		b.routes = append(b.routes, p)
		cdp := p["method"].(string)
		b.calls = append(b.calls, cdp)
		if cdp == "Runtime.releaseObjectGroup" {
			value = map[string]any{"result": map[string]any{}}
			break
		}
		if cdp == "DOM.resolveNode" {
			value = map[string]any{"result": map[string]any{"object": map[string]any{"objectId": "bound-node"}}}
			break
		}
		if cdp == "Input.dispatchKeyEvent" {
			b.keyCalls++
			if len(b.keyErrors) > 0 {
				err := b.keyErrors[0]
				b.keyErrors = b.keyErrors[1:]
				if err != nil {
					return err
				}
			}
			value = map[string]any{"result": map[string]any{}}
			break
		}
		if cdp != "Runtime.evaluate" && cdp != "Runtime.callFunctionOn" {
			b.t.Fatalf("unexpected CDP %s", cdp)
		}
		var spec runtimeSpec
		params := p["command_params"].(map[string]any)
		var data []byte
		if cdp == "Runtime.evaluate" {
			expression := params["expression"].(string)
			suffix := expression[strings.LastIndex(expression, ")(")+2:]
			data = []byte(strings.TrimSuffix(suffix, ")"))
		} else {
			data, _ = json.Marshal(params["arguments"].([]any)[0].(map[string]any)["value"])
		}
		if err := json.Unmarshal(data, &spec); err != nil {
			b.t.Fatal(err)
		}
		if spec.Operation == "perform" {
			if b.performError != nil {
				return b.performError
			}
			if spec.Step.Action != "press" && (b.previewChanges == nil || *b.previewChanges) {
				b.mutations++
			}
			b.performed = append(b.performed, spec)
			if b.failPerform {
				return errors.New("lost mutation response")
			}
		}
		if spec.Operation == "check_conditions" && b.failWaitOnce {
			b.failWaitOnce = false
			return errors.New("context destroyed")
		}
		status := "verified"
		if spec.Operation == "arm_conditions" {
			status = "prepared"
		} else if spec.Operation == "check_conditions" && b.conditionReply != nil {
			var err error
			status, err = b.conditionReply(spec)
			if err != nil {
				return err
			}
		} else if spec.Operation == "check_conditions" && strings.HasSuffix(spec.Step.ID, ":before") {
			status = "planned"
		} else if spec.Operation == "prepare_geometry" {
			status = "ready"
		} else if spec.Operation == "preview" {
			status = "ready"
		} else if spec.Operation == "check_conditions" && b.failConditions {
			status = "planned"
		} else if len(b.statuses) > 0 {
			status = b.statuses[0]
			b.statuses = b.statuses[1:]
		}
		wouldChange := true
		if b.previewChanges != nil {
			wouldChange = *b.previewChanges
		}
		value = map[string]any{"result": map[string]any{"result": map[string]any{"value": map[string]any{"status": status, "attempted": spec.Operation == "perform" && spec.Step.Action != "press" && wouldChange, "would_change": wouldChange, "evidence": "fixture", "point": map[string]any{"x": 25, "y": 30}}}}}
	default:
		b.t.Fatalf("unexpected RPC %s", method)
	}
	raw, _ := json.Marshal(value)
	return json.Unmarshal(raw, out)
}
func fixturePlan(t *testing.T) Plan {
	t.Helper()
	p, err := Decode(strings.NewReader(`{"version":1,"url":"https://example.test/","steps":[{"id":"save","action":"click","target":{"selector":"button"},"after":[{"target":{"selector":"#status"},"text":"Saved"}]},{"id":"next","action":"wait","after":[{"target":{"selector":"#next"},"visible":true}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestPreviewNeverMutatesAndDefersDependentSteps(t *testing.T) {
	b := &fakeBrowser{t: t, statuses: []string{"ready"}}
	e := Engine{Browser: b}
	report, err := e.Run(context.Background(), 12, fixturePlan(t), false)
	if err != nil || report.Status != "preview" || b.mutations != 0 || report.Steps[1].Status != "planned" {
		t.Fatalf("bad preview %+v %v", report, err)
	}
	if b.calls[len(b.calls)-1] != "browser.lease.release" {
		t.Fatal("attachment leaked")
	}
}
func TestWaitRetriesReadsAcrossNavigationWithoutRepeatingAction(t *testing.T) {
	b := &fakeBrowser{t: t, statuses: []string{"performed", "verified", "verified"}, failWaitOnce: true}
	e := Engine{Browser: b}
	report, err := e.Run(context.Background(), 12, fixturePlan(t), true)
	if err != nil || report.Status != "verified" || b.mutations != 1 {
		t.Fatalf("bad navigation %+v %v mutations=%d", report, err, b.mutations)
	}
}
func TestLostActionResponseIsUnconfirmedAndNeverRetried(t *testing.T) {
	b := &fakeBrowser{t: t, failPerform: true}
	e := Engine{Browser: b}
	report, err := e.Run(context.Background(), 12, fixturePlan(t), true)
	if err == nil || report.Status != "unconfirmed" || len(report.Steps) != 1 || b.mutations != 1 {
		t.Fatalf("unsafe replay %+v %v", report, err)
	}
	if b.calls[len(b.calls)-1] != "browser.lease.release" {
		t.Fatal("attachment leaked on error")
	}
}
func TestConflictingExecutionLeaseIsNotTakenOrDetached(t *testing.T) {
	b := &fakeBrowser{t: t, already: true}
	e := Engine{Browser: b}
	_, err := e.Run(context.Background(), 12, fixturePlan(t), true)
	if err == nil || len(b.calls) != 1 {
		t.Fatal("existing owner was disturbed")
	}
}
func TestFailureStopsDependentSteps(t *testing.T) {
	b := &fakeBrowser{t: t, statuses: []string{"performed"}, failConditions: true}
	e := Engine{Browser: b}
	plan := fixturePlan(t)
	plan.Steps[0].TimeoutMS = 10
	report, err := e.Run(context.Background(), 12, plan, true)
	if err == nil || report.Status != "unconfirmed" || len(report.Steps) != 1 || b.mutations != 1 {
		t.Fatal("unconfirmed action did not stop the plan")
	}
}

func TestUncertainJevDecisionNeverReachesInput(t *testing.T) {
	b := &fakeBrowser{t: t}
	plan := fixturePlan(t)
	plan.Steps = plan.Steps[:1]
	plan.Steps[0].Target = &Target{Goal: "Find Save", FrameID: "root"}
	e := Engine{Browser: b, Select: func(_ context.Context, goal, page string) (Selection, error) {
		if goal != "Find Save" || page != plan.URL {
			t.Fatal("selector received unexpected data")
		}
		return Selection{Status: "needs_review", NeedsReview: true}, nil
	}}
	report, err := e.Run(context.Background(), 12, plan, true)
	if err == nil || b.mutations != 0 || report.Steps[0].Code != "semantic_needs_review" {
		t.Fatalf("uncertain decision acted: %+v", report)
	}
}
func TestCompletedSemanticStepSkipsBeforeModelOrTargetResolution(t *testing.T) {
	b := &fakeBrowser{t: t, statuses: []string{"verified"}}
	plan := fixturePlan(t)
	plan.Steps = plan.Steps[:1]
	plan.Steps[0].Target = &Target{Goal: "Find Save", FrameID: "root"}
	plan.Steps[0].SkipIf = plan.Steps[0].After
	e := Engine{Browser: b, Select: func(context.Context, string, string) (Selection, error) {
		t.Fatal("completed step queried model")
		return Selection{}, nil
	}}
	report, err := e.Run(context.Background(), 12, plan, true)
	if err != nil || b.mutations != 0 || report.Steps[0].Status != "skipped" || report.SemanticSelections != 0 {
		t.Fatalf("bad semantic resume: %+v %v", report, err)
	}
}
