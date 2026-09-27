package jevdom

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/jev"
)

type packingEvaluator struct {
	t     *testing.T
	calls []recordedCall
	match func(string, visibleCandidate) bool
}

func (e *packingEvaluator) Evaluate(_ context.Context, state any, questions map[string]jev.Question) (jev.Evaluation, error) {
	if err := jev.ValidateRequest(state, questions); err != nil {
		e.t.Fatalf("selector sent oversized request: %v", err)
	}
	e.calls = append(e.calls, recordedCall{state, questions})
	encoded, _ := json.Marshal(state)
	var input struct {
		Goal       string                      `json:"goal"`
		Goals      map[string]string           `json:"goals"`
		Candidates map[string]visibleCandidate `json:"candidates"`
	}
	_ = json.Unmarshal(encoded, &input)
	result := jev.Evaluation{Model: "jev-1.13.0", Usage: jev.Usage{InputTokens: 100, OutputTokens: 10}, Answers: map[string]jev.ChoiceAnswer{}}
	for name, q := range questions {
		goal := input.Goal
		if goal == "" {
			goal = input.Goals[name]
		}
		choice := "none"
		probabilities := map[string]float64{}
		for id, description := range q.Criteria {
			probabilities[id] = 0
			c, found := input.Candidates[id]
			if !found {
				found = json.Unmarshal([]byte(description), &c) == nil
			}
			if id != "none" && found && e.match != nil && e.match(goal, c) {
				choice = id
			}
		}
		probabilities[choice] = 1
		result.Answers[name] = jev.ChoiceAnswer{Type: "choice", Choice: choice, Confidence: 1, Probabilities: probabilities}
	}
	return result, nil
}

func TestAdaptiveUsesOneFlatRequestAndPacksOversizedGroups(t *testing.T) {
	for _, count := range []int{103, 254, 321} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			opt := options()
			opt.Limit = 480
			e := &packingEvaluator{t: t, match: func(_ string, c visibleCandidate) bool { return c.Name == fmt.Sprintf("Item %d", count-1) }}
			result, err := (Selector{Browser: browserFor(page(count)), Evaluator: e}).Select(context.Background(), opt)
			if err != nil || result.Selected == nil || result.Selected.Name != fmt.Sprintf("Item %d", count-1) {
				t.Fatalf("selection=%+v err=%v", result, err)
			}
			want := 1
			if count > 254 {
				want = 2
			}
			if len(e.calls) != want || result.Timing.Requests != want {
				t.Fatalf("requests=%d want=%d", len(e.calls), want)
			}
		})
	}
}

func TestAdaptivePacksEncodedBytesIncludingEscapes(t *testing.T) {
	value := page(80)
	for i, item := range value.frames[0].nodes[1:] {
		item["name"] = map[string]any{"value": fmt.Sprintf("%d ", i) + strings.Repeat("\"", 176)}
	}
	e := &packingEvaluator{t: t}
	result, err := (Selector{Browser: browserFor(value), Evaluator: e}).Select(context.Background(), options())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Decisions) < 3 || len(e.calls) >= 4 {
		t.Fatalf("expected packed groups and final, calls=%d decisions=%d", len(e.calls), len(result.Decisions))
	}
	if result.Status != "no_match" {
		t.Fatalf("status=%s", result.Status)
	}
}

func TestBatchUsesOneObservationAndSharesUsageWithoutDoubleCounting(t *testing.T) {
	browser := browserFor(page(5))
	e := &packingEvaluator{t: t, match: func(goal string, c visibleCandidate) bool { return goal == c.Name }}
	first, second := options(), options()
	first.Goal = "Item 1"
	second.Goal = "Item 4"
	results, err := (Selector{Browser: browser, Evaluator: e}).SelectBatch(context.Background(), []Options{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.calls) != 1 || len(e.calls[0].questions) != 2 || len(browser.methods) != 6 {
		t.Fatalf("requests=%d browsercalls=%d", len(e.calls), len(browser.methods))
	}
	for i, r := range results {
		if r.Selected == nil || !r.UsageShared || r.Binding == nil {
			t.Fatalf("result %d=%+v", i, r)
		}
	}
	if results[0].Selected.Name != "Item 1" || results[1].Selected.Name != "Item 4" || results[0].Usage.InputTokens+results[1].Usage.InputTokens != 100 {
		t.Fatal("wrong independent choices or double-counted shared usage")
	}
}

func TestRealChromiumSiblingRelationsDistinguishTargetsAndInvalidateCache(t *testing.T) {
	data, err := os.ReadFile("testdata/chromium-products.json")
	if err != nil {
		t.Fatal(err)
	}
	type capture struct {
		FrameTree frameTree        `json:"frameTree"`
		Nodes     []map[string]any `json:"nodes"`
	}
	var source struct{ Before, After capture }
	if err = json.Unmarshal(data, &source); err != nil {
		t.Fatal(err)
	}
	convert := func(c capture) fakePage {
		return fakePage{frames: []fakeFrame{{id: c.FrameTree.Frame.ID, loader: c.FrameTree.Frame.LoaderID, url: c.FrameTree.Frame.URL, nodes: c.Nodes}}}
	}
	before, after := convert(source.Before), convert(source.After)
	a, err := Capture(context.Background(), browserFor(before), 12, "controls", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Capture(context.Background(), browserFor(after), 12, "controls", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Candidates) != 2 || len(b.Candidates) != 2 || a.Fingerprint == b.Fingerprint {
		t.Fatal("product swap was not represented in fingerprint")
	}
	e := &packingEvaluator{t: t, match: func(_ string, c visibleCandidate) bool {
		for _, r := range c.ContextRelations {
			if strings.Contains(r.Name, "Widget Basic $10") {
				return true
			}
		}
		return false
	}}
	selector := Selector{Browser: browserFor(before), Evaluator: e, Cache: NewCache(t.TempDir())}
	first, err := selector.Select(context.Background(), options())
	if err != nil {
		t.Fatal(err)
	}
	selector.Browser = browserFor(after)
	second, err := selector.Select(context.Background(), options())
	if err != nil {
		t.Fatal(err)
	}
	if first.Selected == nil || second.Selected == nil || first.Selected.BackendDOMNodeID == second.Selected.BackendDOMNodeID || second.CacheHit || len(e.calls) != 2 {
		t.Fatal("changed entity reused old binding")
	}
	selector.Browser = browserFor(before, after)
	uncached := options()
	uncached.NoCache = true
	stale, err := selector.Select(context.Background(), uncached)
	if err != nil || stale.Status != "stale" || stale.Selected != nil {
		t.Fatalf("swap during decision not rejected: %+v %v", stale, err)
	}
}

func TestRelationalContextNeverCopiesEditableOrBlockedDescendants(t *testing.T) {
	value := page(0)
	value.frames[0].nodes = append(value.frames[0].nodes, node(2, "row", "", "1000000"), node(3, "StaticText", "Product label", "2"), node(4, "textbox", "Customer input", "2"), node(5, "StaticText", "PRIVATE_EDITABLE_VALUE", "4"), node(6, "button", "Buy", "2"), node(7, "generic", "", "2"), node(8, "StaticText", "PRIVATE_HIDDEN_TEXT", "7"))
	value.frames[0].nodes[6]["properties"] = []any{property("hidden", true)}
	snapshot, err := Capture(context.Background(), browserFor(value), 12, "controls", "")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(snapshot.Candidates)
	if strings.Contains(string(encoded), "PRIVATE_") || !strings.Contains(string(encoded), "Product label") {
		t.Fatalf("unsafe or missing relation: %s", encoded)
	}
}

func TestEntityRelationKeepsProductLinkLabelsWithoutActionButtonNoise(t *testing.T) {
	value := page(0)
	value.frames[0].nodes = append(value.frames[0].nodes, node(2, "row", "", "1000000"), node(3, "link", "Product Alpha", "2"), node(4, "StaticText", "Product Alpha", "3"), node(5, "button", "Buy", "2"), node(6, "button", "Delete account", "2"))
	snapshot, err := Capture(context.Background(), browserFor(value), 12, "controls", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range snapshot.Candidates {
		if c.Name == "Buy" {
			encoded, _ := json.Marshal(c.ContextRelations)
			if strings.Count(string(encoded), "Product Alpha") != 1 || strings.Contains(string(encoded), "Delete account") {
				t.Fatalf("bad linked entity context: %s", encoded)
			}
			return
		}
	}
	t.Fatal("Buy candidate missing")
}

func TestAdaptiveCacheRejectsMissingGroupEvidence(t *testing.T) {
	snapshot, err := Capture(context.Background(), browserFor(page(321)), 12, "controls", "")
	if err != nil {
		t.Fatal(err)
	}
	opt := options()
	opt.Limit = 480
	decisions := make([]evaluated, 1)
	results := make([]Result, 1)
	if err = evaluateAdaptive(context.Background(), &packingEvaluator{t: t}, []Options{opt}, [][]Candidate{snapshot.Candidates}, []int{0}, decisions, results); err != nil {
		t.Fatal(err)
	}
	if !validAdaptive(decisions[0], snapshot.Candidates) {
		t.Fatal("valid adaptive trace rejected")
	}
	decisions[0].Decisions = decisions[0].Decisions[len(decisions[0].Decisions)-1:]
	if validAdaptive(decisions[0], snapshot.Candidates) {
		t.Fatal("missing elimination evidence accepted")
	}
}

// overflowEvaluator behaves like a provider whose tokenizer counts more tokens
// than Rep's estimate: questions above limit tokens report max_tokens_exceeded.
type overflowEvaluator struct {
	packingEvaluator
	limit, rejected int
}

func (e *overflowEvaluator) Evaluate(ctx context.Context, state any, questions map[string]jev.Question) (jev.Evaluation, error) {
	encodedState, _ := json.Marshal(state)
	for _, question := range questions {
		encoded, _ := json.Marshal(question)
		if jev.EstimateTokens(encodedState)+jev.EstimateTokens(encoded) > e.limit {
			e.rejected++
			return jev.Evaluation{}, &jev.Error{Code: jev.CodeContextExceeded, Status: 400, ProviderType: "max_tokens_exceeded", Message: "Jev API returned HTTP 400 (max_tokens_exceeded): the request exceeds the model context limit"}
		}
	}
	return e.packingEvaluator.Evaluate(ctx, state, questions)
}

func TestProviderContextOverflowSplitsTheSameCandidatesMoreFinely(t *testing.T) {
	value := page(200)
	for i, item := range value.frames[0].nodes[1:] {
		item["name"] = map[string]any{"value": fmt.Sprintf("Item %d ", i) + strings.Repeat("detail ", 22)}
	}
	target := fmt.Sprintf("Item 150 %s", strings.TrimSpace(strings.Repeat("detail ", 22)))
	e := &overflowEvaluator{packingEvaluator: packingEvaluator{t: t, match: func(_ string, c visibleCandidate) bool { return c.Name == target }}, limit: 9000}
	result, err := (Selector{Browser: browserFor(value), Evaluator: e}).Select(context.Background(), options())
	if err != nil || result.Selected == nil || result.Selected.Name != target {
		t.Fatalf("selection=%+v err=%v", result.Selected, err)
	}
	if e.rejected < 2 || len(result.Decisions) < 3 {
		t.Fatalf("expected overflow retries and grouped decisions: rejected=%d decisions=%d", e.rejected, len(result.Decisions))
	}
	for _, call := range e.calls {
		encodedState, _ := json.Marshal(call.state)
		for _, question := range call.questions {
			encoded, _ := json.Marshal(question)
			if jev.EstimateTokens(encodedState)+jev.EstimateTokens(encoded) > e.limit {
				t.Fatal("an accepted request exceeded the simulated provider limit")
			}
		}
	}
	persistent := &overflowEvaluator{packingEvaluator: packingEvaluator{t: t}, limit: 10}
	_, err = (Selector{Browser: browserFor(value), Evaluator: persistent}).Select(context.Background(), options())
	if jev.CodeOf(err) != jev.CodeContextExceeded || persistent.rejected != 3 {
		t.Fatalf("persistent overflow: err=%v rejected=%d", err, persistent.rejected)
	}
}
