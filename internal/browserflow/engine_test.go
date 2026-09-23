package browserflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeBrowser struct {
	t            *testing.T
	statuses     []string
	calls        []string
	mutations    int
	already      bool
	failPerform  bool
	failWaitOnce bool
}

func (b *fakeBrowser) Call(_ context.Context, method string, args any, out any) error {
	b.calls = append(b.calls, method)
	value := any(map[string]any{})
	switch method {
	case "browser.attach":
		value = map[string]any{"already_attached": b.already}
	case "browser.detach":
	case "browser.cdp":
		p := args.(map[string]any)
		cdp := p["method"].(string)
		b.calls = append(b.calls, cdp)
		if cdp == "Runtime.releaseObjectGroup" {
			value = map[string]any{"result": map[string]any{}}
			break
		}
		if cdp != "Runtime.evaluate" {
			b.t.Fatalf("unexpected CDP %s", cdp)
		}
		expression := p["command_params"].(map[string]any)["expression"].(string)
		// Match only the serialized spec suffix, not words inside the embedded runtime.
		suffix := expression[strings.LastIndex(expression, ")(")+2:]
		var spec runtimeSpec
		if err := json.Unmarshal([]byte(strings.TrimSuffix(suffix, ")")), &spec); err != nil {
			b.t.Fatal(err)
		}
		if spec.Operation == "perform" {
			b.mutations++
			if b.failPerform {
				return errors.New("lost mutation response")
			}
		}
		if spec.Operation == "wait" && b.failWaitOnce {
			b.failWaitOnce = false
			return errors.New("context destroyed")
		}
		status := "verified"
		if len(b.statuses) > 0 {
			status = b.statuses[0]
			b.statuses = b.statuses[1:]
		}
		value = map[string]any{"result": map[string]any{"result": map[string]any{"value": map[string]any{"status": status, "attempted": spec.Operation == "perform", "evidence": "fixture"}}}}
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
	if b.calls[len(b.calls)-1] != "browser.detach" {
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
	if b.calls[len(b.calls)-1] != "browser.detach" {
		t.Fatal("attachment leaked on error")
	}
}
func TestExistingAttachmentIsNotTakenOrDetached(t *testing.T) {
	b := &fakeBrowser{t: t, already: true}
	e := Engine{Browser: b}
	_, err := e.Run(context.Background(), 12, fixturePlan(t), true)
	if err == nil || len(b.calls) != 1 {
		t.Fatal("existing owner was disturbed")
	}
}
func TestFailureStopsDependentSteps(t *testing.T) {
	b := &fakeBrowser{t: t, statuses: []string{"performed", "failed"}}
	e := Engine{Browser: b}
	report, err := e.Run(context.Background(), 12, fixturePlan(t), true)
	if err == nil || report.Status != "unconfirmed" || len(report.Steps) != 1 || b.mutations != 1 {
		t.Fatal("unconfirmed action did not stop the plan")
	}
}

func TestUncertainJevDecisionNeverReachesInput(t *testing.T) {
	b := &fakeBrowser{t: t}
	plan := fixturePlan(t)
	plan.Steps = plan.Steps[:1]
	plan.Steps[0].Target = &Target{Goal: "Find Save"}
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
	b := &fakeBrowser{t: t, statuses: []string{"skipped"}}
	plan := fixturePlan(t)
	plan.Steps = plan.Steps[:1]
	plan.Steps[0].Target = &Target{Goal: "Find Save"}
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
