package jevdom

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/jev"
)

// stepBrowser serves compact control and text observations plus page metrics.
type stepBrowser struct {
	controls, text Snapshot
	changed        bool
	scrollY        float64
	methods        []string
}

func (b *stepBrowser) Call(_ context.Context, method string, params any, out any) error {
	b.methods = append(b.methods, method)
	p := params.(map[string]any)
	var result any
	switch method {
	case "browser.observe":
		if p["kind"] == "text" {
			result = b.text
		} else {
			result = b.controls
		}
	case "browser.validate":
		snapshot := b.controls
		if b.changed {
			snapshot.Fingerprint = strings.Repeat("b", 64)
		}
		result = map[string]any{"fresh": !b.changed, "snapshot": snapshot}
	case "browser.cdp":
		switch p["method"] {
		case "Page.getLayoutMetrics":
			result = map[string]any{"result": map[string]any{"cssContentSize": map[string]any{"height": 3000}, "cssLayoutViewport": map[string]any{"pageY": b.scrollY, "clientHeight": 800}}}
		case "Page.getNavigationHistory":
			result = map[string]any{"result": map[string]any{"currentIndex": 0, "entries": []any{map[string]any{"url": "https://shop.example/checkout?session=SECRET", "title": "Checkout"}}}}
		default:
			return errors.New("unexpected CDP method")
		}
	default:
		return errors.New("unexpected method " + method)
	}
	data, _ := json.Marshal(result)
	return json.Unmarshal(data, out)
}

func stepCandidate(id, role, name string) Candidate {
	return Candidate{ID: id, Role: role, Name: name, FrameID: "frame", BackendDOMNodeID: int64(len(id) + 10), DocumentGeneration: "doc"}
}

func stepSnapshot(candidates ...Candidate) Snapshot {
	for i := range candidates {
		candidates[i].BackendDOMNodeID = int64(100 + i)
	}
	return Snapshot{Schema: 1, Generation: "gen", Fingerprint: strings.Repeat("a", 64), Candidates: candidates, Coverage: Coverage{TotalCandidates: len(candidates), FramesRead: 1}}
}

type stepEvaluator struct {
	calls     int
	state     any
	questions map[string]jev.Question
	answer    func(name string, question jev.Question) (string, float64)
}

func (e *stepEvaluator) Evaluate(_ context.Context, state any, questions map[string]jev.Question) (jev.Evaluation, error) {
	e.calls++
	e.state, e.questions = state, questions
	answers := map[string]jev.ChoiceAnswer{}
	for name, question := range questions {
		choice, confidence := e.answer(name, question)
		probabilities := map[string]float64{}
		for option := range question.Criteria {
			probabilities[option] = 0
		}
		probabilities[choice] = 1
		answers[name] = jev.ChoiceAnswer{Type: "choice", Choice: choice, Probabilities: probabilities, Confidence: confidence}
	}
	return jev.Evaluation{Model: "jev-test", Answers: answers, Usage: jev.Usage{InputTokens: 10, OutputTokens: 2}}, nil
}

// optionFor finds the option whose referenced element has the given name.
func optionFor(t *testing.T, state any, question jev.Question, name string, value string) string {
	t.Helper()
	data, _ := json.Marshal(state)
	var decoded struct {
		Elements map[string]visibleCandidate `json:"elements"`
		Values   map[string]string           `json:"available_values"`
	}
	_ = json.Unmarshal(data, &decoded)
	for option, criterion := range question.Criteria {
		for alias, element := range decoded.Elements {
			if element.Name != name || !strings.Contains(criterion, "state.elements."+alias+"`") {
				continue
			}
			if value == "" {
				return option
			}
			for valueID, key := range decoded.Values {
				if key == value && strings.Contains(criterion, "state.available_values."+valueID+"`") {
					return option
				}
			}
		}
	}
	t.Fatalf("no option for %q/%q", name, value)
	return ""
}

func stepOptions() StepOptions {
	o := DefaultOptions()
	o.TabID, o.Goal, o.Owner, o.LeaseID, o.Model = 7, "Check out with my email", "test-owner", "test-lease", "jev-test"
	return StepOptions{Options: o}
}

func TestStepUsesOneRequestAndConsumesOnlyTheChosenHead(t *testing.T) {
	browser := &stepBrowser{
		controls: stepSnapshot(stepCandidate("c1", "button", "Place order"), stepCandidate("c2", "textbox", "Email"), stepCandidate("c3", "textbox", "Full name"), stepCandidate("c4", "link", "Help")),
		text:     stepSnapshot(stepCandidate("t1", "heading", "Checkout"), stepCandidate("t2", "statictext", "Contact sijan@unm.edu for help")),
	}
	evaluator := &stepEvaluator{}
	evaluator.answer = func(name string, question jev.Question) (string, float64) {
		switch name {
		case "operation":
			return OpTypeText, 0.93
		case "click_target":
			return optionFor(t, evaluator.state, question, "Place order", ""), 0.9
		default:
			return optionFor(t, evaluator.state, question, "Email", "email"), 0.91
		}
	}
	input := stepOptions()
	input.ValueKeys = []string{"email", "full name"}
	decision, err := Selector{Browser: browser, Evaluator: evaluator}.Step(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if evaluator.calls != 1 {
		t.Fatalf("step used %d provider requests, want one fan-out request", evaluator.calls)
	}
	if decision.Status != "selected" || decision.Operation != OpTypeText || decision.Target == nil || decision.Target.Name != "Email" || decision.ValueKey != "email" {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	if decision.Binding == nil || decision.Binding.Kind != "controls" || decision.Binding.BackendDOMNodeID != decision.Target.BackendDOMNodeID {
		t.Fatalf("binding lost observation identity: %+v", decision.Binding)
	}
	for _, head := range []string{"operation", "click_target", "type_text_target"} {
		if _, ok := evaluator.questions[head]; !ok {
			t.Fatalf("missing %s head", head)
		}
	}
	request, _ := json.Marshal(struct {
		State     any
		Questions map[string]jev.Question
	}{evaluator.state, evaluator.questions})
	for _, secret := range []string{"sijan@unm.edu", "SECRET", "session="} {
		if strings.Contains(string(request), secret) {
			t.Fatalf("request leaked %q: %s", secret, request)
		}
	}
	if !strings.Contains(string(request), "Checkout") || !strings.Contains(string(request), "https://shop.example") {
		t.Fatal("page title/origin evidence is missing")
	}
	if strings.Contains(string(request), "SCROLL_UP") || !strings.Contains(string(request), "SCROLL_DOWN") {
		t.Fatal("scroll operations do not follow the observed scroll position")
	}
}

func TestStepClickAndNonTargetOperations(t *testing.T) {
	browser := &stepBrowser{controls: stepSnapshot(stepCandidate("c1", "button", "Search"), stepCandidate("c2", "textbox", "Query")), scrollY: 400}
	evaluator := &stepEvaluator{}
	next := OpClick
	evaluator.answer = func(name string, question jev.Question) (string, float64) {
		if name == "operation" {
			return next, 0.95
		}
		if name == "click_target" {
			return optionFor(t, evaluator.state, question, "Search", ""), 0.95
		}
		return optionFor(t, evaluator.state, question, "Query", ""), 0.95
	}
	selector := Selector{Browser: browser, Evaluator: evaluator}
	decision, err := selector.Step(context.Background(), stepOptions())
	if err != nil || decision.Status != "selected" || decision.Target.Name != "Search" || decision.ValueKey != "" {
		t.Fatalf("click decision %+v %v", decision, err)
	}
	if !decision.Evidence.CanScrollUp || !strings.Contains(strings.Join(decision.Operations, ","), OpScrollUp) {
		t.Fatal("scroll up was not offered on a scrolled page")
	}
	for _, op := range []string{OpDone, OpWait, OpScrollDown} {
		next = op
		decision, err = selector.Step(context.Background(), stepOptions())
		if err != nil || decision.Status != "selected" || decision.Target != nil || decision.Binding != nil {
			t.Fatalf("%s decision %+v %v", op, decision, err)
		}
	}
}

func TestStepReviewsLowConfidenceDisagreementAndStaleness(t *testing.T) {
	controls := stepSnapshot(stepCandidate("c1", "button", "Delete account"), stepCandidate("c2", "button", "Save"))
	cases := []struct {
		name       string
		opConf     float64
		targetNone bool
		changed    bool
		want       string
	}{
		{"low confidence", 0.5, false, false, "needs_review"},
		{"target head disagrees", 0.95, true, false, "needs_review"},
		{"controls changed", 0.95, false, true, "stale"},
	}
	for _, test := range cases {
		browser := &stepBrowser{controls: controls, changed: test.changed}
		evaluator := &stepEvaluator{}
		evaluator.answer = func(name string, question jev.Question) (string, float64) {
			if name == "operation" {
				return OpClick, test.opConf
			}
			if test.targetNone {
				return "none", 0.9
			}
			return optionFor(t, evaluator.state, question, "Save", ""), 0.95
		}
		decision, err := Selector{Browser: browser, Evaluator: evaluator}.Step(context.Background(), stepOptions())
		if err != nil || decision.Status != test.want || !decision.NeedsReview {
			t.Fatalf("%s: %+v %v", test.name, decision, err)
		}
	}
}

func TestStepValidatesValueNamesAndRejectsInvalidAnswers(t *testing.T) {
	input := stepOptions()
	for _, keys := range [][]string{{"email", "EMAIL"}, {"bad\nname"}, {""}, {strings.Repeat("x", 65)}} {
		input.ValueKeys = keys
		if _, err := input.Validate(); err == nil {
			t.Fatalf("accepted value names %q", keys)
		}
	}
	browser := &stepBrowser{controls: stepSnapshot(stepCandidate("c1", "button", "Go"))}
	evaluator := &stepEvaluator{answer: func(name string, question jev.Question) (string, float64) { return "not-offered", 0.99 }}
	if _, err := (Selector{Browser: browser, Evaluator: evaluator}).Step(context.Background(), stepOptions()); jev.CodeOf(err) != jev.CodeInvalidResponse {
		t.Fatalf("an unoffered answer was accepted: %v", err)
	}
}

func TestStepHistoryIsBoundedAndScrubbed(t *testing.T) {
	input := stepOptions()
	for i := 0; i < 15; i++ {
		input.History = append(input.History, StepHistory{Operation: OpClick, Target: "button Send to a@b.example"})
	}
	validated, err := input.Validate()
	if err != nil || len(validated.History) != MaxStepHistory {
		t.Fatalf("history not bounded: %d %v", len(validated.History), err)
	}
	if strings.Contains(validated.History[0].Target, "a@b.example") {
		t.Fatal("history target was not scrubbed")
	}
}

func TestStepOptionsDecodeKeepsDefaultsAndRejectsUnknownFields(t *testing.T) {
	var input StepOptions
	if err := json.Unmarshal([]byte(`{"tab_id":4,"goal":"Go","value_keys":["email"],"history":[{"operation":"CLICK"}],"no_text":true}`), &input); err != nil {
		t.Fatal(err)
	}
	if input.TabID != 4 || input.Confidence != 0.8 || input.Limit != 240 || len(input.ValueKeys) != 1 || len(input.History) != 1 || !input.NoText {
		t.Fatalf("decoded %+v", input)
	}
	if err := json.Unmarshal([]byte(`{"tab_id":4,"goal":"Go","surprise":1}`), &input); err == nil {
		t.Fatal("unknown option field accepted")
	}
	if err := json.Unmarshal([]byte(`{"tab_id":4,"goal":"Go","history":[{"operation":"CLICK","value":"secret"}]}`), &input); err == nil {
		t.Fatal("unknown history field accepted")
	}
	encoded, _ := json.Marshal(StepOptions{Options: Options{TabID: 2, Goal: "x"}, ValueKeys: []string{"a"}})
	var roundTrip StepOptions
	if err := json.Unmarshal(encoded, &roundTrip); err != nil || roundTrip.TabID != 2 || roundTrip.ValueKeys[0] != "a" {
		t.Fatalf("round trip failed: %s %v", encoded, err)
	}
}

func TestStepSelectsNativeOptionsThroughTheirSelect(t *testing.T) {
	country := stepCandidate("s1", "combobox", "Country")
	us := stepCandidate("o1", "option", "United States")
	us.States = map[string]any{"selected": true}
	us.ContextRelations = []ContextRelation{{Role: "select", Name: "Country"}}
	canada := stepCandidate("o2", "option", "Canada")
	canada.ContextRelations = []ContextRelation{{Role: "select", Name: "Country"}}
	browser := &stepBrowser{controls: stepSnapshot(country, us, canada, stepCandidate("b1", "button", "Continue"), stepCandidate("t1", "textbox", "City"))}
	evaluator := &stepEvaluator{}
	evaluator.answer = func(name string, question jev.Question) (string, float64) {
		switch name {
		case "operation":
			return OpSelect, 0.95
		case "select_target":
			return optionFor(t, evaluator.state, question, "Canada", ""), 0.94
		case "click_target":
			return optionFor(t, evaluator.state, question, "Continue", ""), 0.9
		default:
			return optionFor(t, evaluator.state, question, "City", ""), 0.9
		}
	}
	decision, err := Selector{Browser: browser, Evaluator: evaluator}.Step(context.Background(), stepOptions())
	if err != nil || decision.Status != "selected" || decision.Operation != OpSelect {
		t.Fatalf("decision %+v %v", decision, err)
	}
	if decision.Target == nil || decision.Target.Name != "Country" || decision.Option != "Canada" || decision.Binding.BackendDOMNodeID != decision.Target.BackendDOMNodeID {
		t.Fatalf("SELECT must bind the owning select and carry the option label: %+v", decision)
	}
	// Native options and their select are never CLICK or TYPE_TEXT targets.
	for _, head := range []string{"click_target", "type_text_target"} {
		question := evaluator.questions[head]
		data, _ := json.Marshal(evaluator.state)
		for option, criterion := range question.Criteria {
			if option == "none" {
				continue
			}
			alias := strings.TrimSuffix(strings.TrimPrefix(criterion[strings.Index(criterion, "state.elements.")+len("state.elements."):], ""), "`")
			if end := strings.Index(alias, "`"); end >= 0 {
				alias = alias[:end]
			}
			var decoded struct {
				Elements map[string]visibleCandidate `json:"elements"`
			}
			_ = json.Unmarshal(data, &decoded)
			if name := decoded.Elements[alias].Name; name == "Country" || name == "Canada" || name == "United States" {
				t.Fatalf("%s offered native dropdown element %q", head, name)
			}
		}
	}
	if decision.Evidence.SelectOptions != 2 {
		t.Fatalf("select options = %d", decision.Evidence.SelectOptions)
	}
}
