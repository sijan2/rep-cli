package jevdom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/jev"
)

type fakeFrame struct {
	id, url, loader string
	nodes           []map[string]any
	fail            bool
}
type fakePage struct{ frames []fakeFrame }
type fakeBrowser struct {
	pages               []fakePage
	frameCalls, current int
	methods             []string
	mutateTree          func(int, *frameTree)
}

func (browser *fakeBrowser) Call(_ context.Context, method string, params any, out any) error {
	p := params.(map[string]any)
	if method != "browser.cdp" || p["keep_attached"] != false {
		return errors.New("unexpected browser operation")
	}
	command := p["method"].(string)
	browser.methods = append(browser.methods, command)
	var data any
	switch command {
	case "Page.getFrameTree":
		browser.current = min(browser.frameCalls/2, len(browser.pages)-1)
		browser.frameCalls++
		page := browser.pages[browser.current]
		frames := make([]frameTree, len(page.frames))
		for index, frame := range page.frames {
			frames[index].Frame.ID = frame.id
			frames[index].Frame.LoaderID = frame.loader
			frames[index].Frame.URL = frame.url
		}
		frames[0].Children = frames[1:]
		if browser.mutateTree != nil {
			browser.mutateTree(browser.frameCalls, &frames[0])
		}
		data = map[string]any{"frameTree": frames[0]}
	case "Accessibility.getFullAXTree":
		id := p["command_params"].(map[string]any)["frameId"]
		for _, frame := range browser.pages[browser.current].frames {
			if frame.id == id {
				if frame.fail {
					return errors.New("AX unavailable with private url")
				}
				data = map[string]any{"nodes": frame.nodes}
				break
			}
		}
	default:
		return errors.New("unexpected CDP method")
	}
	encoded, _ := json.Marshal(map[string]any{"result": data})
	return json.Unmarshal(encoded, out)
}

func node(id int, role, name, parent string) map[string]any {
	return map[string]any{"nodeId": fmt.Sprint(id), "parentId": parent, "backendDOMNodeId": id,
		"role": map[string]any{"value": role}, "name": map[string]any{"value": name}, "ignored": false}
}

func property(name string, value any) map[string]any {
	return map[string]any{"name": name, "value": map[string]any{"value": value}}
}

func page(count int) fakePage {
	nodes := []map[string]any{node(1000000, "RootWebArea", "Test page", "")}
	for index := 0; index < count; index++ {
		nodes = append(nodes, node(index+1, "button", fmt.Sprintf("Item %d", index), "1000000"))
	}
	return fakePage{frames: []fakeFrame{{id: "frame-main", loader: "loader-main", url: "https://example.test/private?token=URL_SECRET", nodes: nodes}}}
}

func browserFor(pages ...fakePage) *fakeBrowser { return &fakeBrowser{pages: pages} }

type recordedCall struct {
	state     any
	questions map[string]jev.Question
}
type fakeEvaluator struct {
	calls      []recordedCall
	target     string
	confidence float64
	pick       func(int, map[string]jev.Question) (string, float64)
	fail       bool
}

func (evaluator *fakeEvaluator) Evaluate(_ context.Context, state any, questions map[string]jev.Question) (jev.Evaluation, error) {
	evaluator.calls = append(evaluator.calls, recordedCall{state, questions})
	if evaluator.fail {
		return jev.Evaluation{}, errors.New("provider echoed PRIVATE_API_KEY")
	}
	choice, confidence := "none", evaluator.confidence
	if confidence == 0 {
		confidence = 1
	}
	for id, description := range questions["selection"].Criteria {
		var candidate visibleCandidate
		if id != "none" && json.Unmarshal([]byte(description), &candidate) == nil && candidate.Name == evaluator.target {
			choice = id
		}
	}
	if evaluator.pick != nil {
		choice, confidence = evaluator.pick(len(evaluator.calls), questions)
	}
	probabilities := map[string]float64{}
	for id := range questions["selection"].Criteria {
		probabilities[id] = 0
	}
	probabilities[choice] = 1
	return jev.Evaluation{Model: "jev-1.13.0", Usage: jev.Usage{InputTokens: 100, OutputTokens: 10},
		Answers: map[string]jev.ChoiceAnswer{"selection": {Type: "choice", Choice: choice, Probabilities: probabilities, Confidence: confidence}}}, nil
}

func options() Options {
	value := DefaultOptions()
	value.TabID = 12
	value.Goal = "Find Item 0"
	return value
}

func TestObservedHandlesNoMatchConfidenceAndZeroCandidates(t *testing.T) {
	for _, test := range []struct {
		target     string
		confidence float64
		status     string
	}{{"Item 0", 1, "selected"}, {"missing", 1, "no_match"}, {"Item 0", 0.3, "needs_review"}} {
		evaluator := &fakeEvaluator{target: test.target, confidence: test.confidence}
		browser := browserFor(page(3))
		result, err := (Selector{Browser: browser, Evaluator: evaluator}).Select(context.Background(), options())
		if err != nil || result.Status != test.status {
			t.Fatalf("result=%+v error=%v", result, err)
		}
		if test.target == "Item 0" && (result.Selected == nil || result.Selected.BackendDOMNodeID != 1 || result.Selected.FrameID != "frame-main") {
			t.Fatal("lost observed handle")
		}
		if result.Usage.InputTokens != 100 || len(evaluator.calls) != 1 {
			t.Fatal("incorrect evaluation accounting")
		}
	}
	evaluator := &fakeEvaluator{}
	result, err := (Selector{Browser: browserFor(page(0)), Evaluator: evaluator}).Select(context.Background(), options())
	if err != nil || result.Status != "no_match" || len(evaluator.calls) != 0 {
		t.Fatalf("zero candidate result=%+v err=%v", result, err)
	}
}

func TestDuplicateLabelsKeepSeparateContextAndHandles(t *testing.T) {
	value := page(0)
	value.frames[0].nodes = append(value.frames[0].nodes,
		node(100, "region", "Draft", "1000000"), node(101, "region", "Published", "1000000"), node(1, "button", "Review", "100"), node(2, "button", "Review", "101"))
	snapshot, err := Capture(context.Background(), browserFor(value), 12, "controls", "")
	if err != nil || len(snapshot.Candidates) != 2 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	a, b := snapshot.Candidates[0], snapshot.Candidates[1]
	if a.ID == b.ID || a.Name != b.Name || a.Context != "Draft" || b.Context != "Published" {
		t.Fatal("duplicate labels lost context")
	}
}

func TestSecretsEditableValuesAndHiddenDisabledNodesNeverBecomeCandidates(t *testing.T) {
	value := page(0)
	input := node(1, "textbox", "Search", "1000000")
	input["value"] = map[string]any{"value": "PRIVATE_TYPED_VALUE_DO_NOT_SEND"}
	input["name"] = map[string]any{"value": "Search", "sources": []any{map[string]any{"value": "SOURCE_ATTRIBUTE_SECRET"}}}
	input["properties"] = []any{property("editable", "plaintext"), property("url", "https://private.test/secret")}
	editable := node(4, "generic", "Editor", "1000000")
	editable["properties"] = []any{property("editable", "richtext")}
	combo := node(6, "combobox", "Category", "1000000")
	disabled := node(10, "button", "Disabled", "1000000")
	disabled["properties"] = []any{property("disabled", true)}
	hidden := node(11, "button", "Hidden", "1000000")
	hidden["ignored"] = true
	value.frames[0].nodes = append(value.frames[0].nodes, input, node(2, "generic", "", "1"), node(3, "StaticText", "PRIVATE_TYPED_VALUE_DO_NOT_SEND", "2"),
		editable, node(5, "StaticText", "PRIVATE_CONTENTEDITABLE_VALUE", "4"), combo,
		node(7, "option", "Public option label", "6"), node(8, "StaticText", "PRIVATE_COMBO_VALUE", "6"),
		node(9, "button", "Visit https://private.test/a?token=SECRET account@example.test api_key=SECRET", "1000000"), disabled, hidden)
	opt := options()
	opt.Kind = "all"
	evaluator := &fakeEvaluator{target: "Search"}
	result, err := (Selector{Browser: browserFor(value), Evaluator: evaluator}).Select(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal([]any{evaluator.calls[0].state, evaluator.calls[0].questions})
	for _, forbidden := range []string{"PRIVATE_", "SOURCE_ATTRIBUTE_SECRET", "private.test", "account@example", "URL_SECRET", "frame-main", "loader-main", "Disabled", "Hidden"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("provider request contains %s", forbidden)
		}
	}
	if !strings.Contains(string(data), "Public option label") || result.Coverage.TotalCandidates != 4 {
		t.Fatalf("option omitted or private descendants included: %+v", result.Coverage)
	}
}

func TestMoreThan255CandidatesUsesBoundedGroupsAndOneCrossGroupDecision(t *testing.T) {
	opt := options()
	opt.Limit = 480
	evaluator := &fakeEvaluator{target: "Item 310"}
	result, err := (Selector{Browser: browserFor(page(321)), Evaluator: evaluator}).Select(context.Background(), opt)
	if err != nil || result.Selected == nil || result.Selected.Name != "Item 310" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(evaluator.calls) != 10 || len(result.Decisions) != 10 {
		t.Fatalf("calls=%d", len(evaluator.calls))
	}
	for _, call := range evaluator.calls {
		if len(call.questions["selection"].Criteria) > 41 {
			t.Fatal("group exceeded 40 plus none")
		}
	}
	finalState, _ := json.Marshal(evaluator.calls[9].state)
	if strings.Contains(string(finalState), "probabilities") || strings.Contains(string(finalState), "confidence") {
		t.Fatal("compared independent group probabilities")
	}
	if result.Coverage.Truncated {
		t.Fatal("unexpected shortlist truncation")
	}
}

func TestGroupUncertaintyCannotBeHiddenByConfidentFinalResult(t *testing.T) {
	evaluator := &fakeEvaluator{target: "Item 0", pick: func(call int, questions map[string]jev.Question) (string, float64) {
		choice := "none"
		for id, description := range questions["selection"].Criteria {
			if strings.Contains(description, `"name":"Item 0"`) {
				choice = id
			}
		}
		if call == 1 {
			return choice, 0.2
		}
		return choice, 1
	}}
	result, err := (Selector{Browser: browserFor(page(41)), Evaluator: evaluator}).Select(context.Background(), options())
	if err != nil || result.Status != "needs_review" || result.Confidence != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestShortlistIsDeterministicAndChangesOutsideItMakeSelectionStale(t *testing.T) {
	first, second := page(50), page(50)
	second.frames[0].nodes[50]["name"] = map[string]any{"value": "Changed outside shortlist"}
	opt := options()
	opt.Limit = 10
	evaluator := &fakeEvaluator{target: "Item 0"}
	result, err := (Selector{Browser: browserFor(first, second), Evaluator: evaluator}).Select(context.Background(), opt)
	if err != nil || result.Status != "stale" || result.Selected != nil || !result.Coverage.Truncated {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	stable, err := (Selector{Browser: browserFor(first), Evaluator: &fakeEvaluator{target: "Item 0"}}).Select(context.Background(), opt)
	if err != nil || stable.Status != "needs_review" || stable.Coverage.Considered != 10 {
		t.Fatalf("result=%+v err=%v", stable, err)
	}
}

func TestFrameOriginAndNavigationAreCheckedBeforeProviderReceivesAnything(t *testing.T) {
	opt := options()
	opt.Origin = "https://example.test"
	for _, navigateDuringCapture := range []bool{false, true} {
		browser := browserFor(page(1))
		browser.mutateTree = func(call int, tree *frameTree) {
			if !navigateDuringCapture || call == 2 {
				tree.Frame.URL = "https://other.test/PRIVATE_URL"
				tree.Frame.LoaderID = "changed-loader"
			}
		}
		evaluator := &fakeEvaluator{target: "Item 0"}
		_, err := (Selector{Browser: browser, Evaluator: evaluator}).Select(context.Background(), opt)
		if err == nil || len(evaluator.calls) != 0 || strings.Contains(err.Error(), "PRIVATE_URL") {
			t.Fatalf("err=%v calls=%d", err, len(evaluator.calls))
		}
		if !navigateDuringCapture && len(browser.methods) != 1 {
			t.Fatal("AX read before origin check")
		}
	}
}

func TestFramesReportIncompleteCoverageAndScopedFramesAreNotRead(t *testing.T) {
	value := page(1)
	value.frames = append(value.frames, fakeFrame{id: "foreign-frame", url: "https://foreign.test/PRIVATE", loader: "foreign-loader", nodes: page(2).frames[0].nodes},
		fakeFrame{id: "failed-frame", url: "https://example.test/frame", loader: "other-loader", fail: true})
	opt := options()
	opt.Origin = "https://example.test"
	browser := browserFor(value)
	result, err := (Selector{Browser: browser, Evaluator: &fakeEvaluator{target: "Item 0"}}).Select(context.Background(), opt)
	if err != nil || result.Status != "needs_review" || result.Coverage.UnavailableFrames != 2 || result.Coverage.FramesRead != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Coverage.TotalCandidates != 1 {
		t.Fatal("cross-origin candidates were read")
	}
	value = page(1)
	for index := 1; index < 18; index++ {
		value.frames = append(value.frames, fakeFrame{id: fmt.Sprint(index), url: "https://example.test/", nodes: page(1).frames[0].nodes})
	}
	snapshot, err := Capture(context.Background(), browserFor(value), 12, "controls", "")
	if err != nil || snapshot.Coverage.FramesRead != 16 || snapshot.Coverage.UnavailableFrames != 2 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func TestWhitelistedStatesAffectFingerprintAndValuesNeverLeave(t *testing.T) {
	first, second := page(1), page(1)
	for index, current := range []*fakePage{&first, &second} {
		current.frames[0].nodes[1]["properties"] = []any{property("checked", index == 1), property("expanded", false), property("invalid", "spelling"), property("value", "PRIVATE_VALUE"), property("valuetext", "PRIVATE_VALUETEXT"), property("pressed", "PRIVATE_INVALID_ENUM")}
	}
	evaluator := &fakeEvaluator{target: "Item 0"}
	result, err := (Selector{Browser: browserFor(first, second), Evaluator: evaluator}).Select(context.Background(), options())
	if err != nil || result.Status != "stale" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	encoded, _ := json.Marshal(evaluator.calls[0].questions)
	if strings.Contains(string(encoded), "PRIVATE_") || !strings.Contains(string(encoded), "checked") {
		t.Fatal("incorrect property allowlist")
	}
}

func TestUnresolvedAncestryAndTruncatedTextRequireReview(t *testing.T) {
	value := page(0)
	value.frames[0].nodes = append(value.frames[0].nodes, node(1, "StaticText", "PRIVATE_DEEP_VALUE", "2"))
	for index := 2; index < 69; index++ {
		value.frames[0].nodes = append(value.frames[0].nodes, node(index, "generic", "", fmt.Sprint(index+1)))
	}
	value.frames[0].nodes = append(value.frames[0].nodes, node(69, "textbox", "Deep editor", "1000000"))
	opt := options()
	opt.Kind = "text"
	evaluator := &fakeEvaluator{}
	result, err := (Selector{Browser: browserFor(value), Evaluator: evaluator}).Select(context.Background(), opt)
	if err != nil || result.Status != "needs_review" || result.Coverage.OmittedNodes != 1 || len(evaluator.calls) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	value = page(1)
	value.frames[0].nodes[1]["name"] = map[string]any{"value": strings.Repeat("x", 181)}
	result, err = (Selector{Browser: browserFor(value), Evaluator: &fakeEvaluator{target: strings.Repeat("x", 180)}}).Select(context.Background(), options())
	if err != nil || result.Status != "needs_review" || result.Coverage.TextTruncated != 1 || result.Selected == nil || !result.Selected.TextTruncated {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestCacheReusesOnlyVerifiedSnapshotsWithoutPersistingPageText(t *testing.T) {
	directory := t.TempDir()
	cache := NewCache(directory)
	evaluator := &fakeEvaluator{target: "Item 0"}
	selector := Selector{Browser: browserFor(page(2)), Evaluator: evaluator, Cache: cache}
	first, err := selector.Select(context.Background(), options())
	if err != nil {
		t.Fatal(err)
	}
	second, err := selector.Select(context.Background(), options())
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheHit || !second.CacheHit || len(evaluator.calls) != 1 || second.Usage.InputTokens != 0 || second.OriginalUsage == nil || second.OriginalUsage.InputTokens != 100 {
		t.Fatal("invalid cache usage accounting")
	}
	entries, _ := os.ReadDir(directory)
	if len(entries) != 1 {
		t.Fatalf("cache entries=%d", len(entries))
	}
	info, _ := entries[0].Info()
	if info.Mode().Perm() != 0600 {
		t.Fatal("cache mode is not0600")
	}
	data, _ := os.ReadFile(filepath.Join(directory, entries[0].Name()))
	for _, forbidden := range []string{"Item 0", "Test page", "frame-main", "URL_SECRET", options().Goal} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("cache contains %s", forbidden)
		}
	}
	changed := page(2)
	changed.frames[0].loader = "new-loader"
	selector.Browser = browserFor(page(2), changed)
	stale, err := selector.Select(context.Background(), options())
	if err != nil || stale.Status != "stale" || !stale.CacheHit || stale.Selected != nil {
		t.Fatalf("result=%+v err=%v", stale, err)
	}
}

func TestCacheExpiryOptionsAndNoCacheInvalidateReuse(t *testing.T) {
	clock := time.Now()
	evaluator := &fakeEvaluator{target: "Item 0"}
	selector := Selector{Browser: browserFor(page(2)), Evaluator: evaluator, Cache: NewCache(t.TempDir()), Now: func() time.Time { return clock }}
	if _, err := selector.Select(context.Background(), options()); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(CacheTTL)
	result, err := selector.Select(context.Background(), options())
	if err != nil || result.CacheHit || len(evaluator.calls) != 2 {
		t.Fatalf("expired result=%+v err=%v", result, err)
	}
	clock = time.Now()
	for _, mutate := range []func(*Options){func(o *Options) { o.Goal = "Different goal" }, func(o *Options) { o.Model = "jev-preview" }, func(o *Options) { o.NoCache = true }} {
		opt := options()
		mutate(&opt)
		before := len(evaluator.calls)
		result, err := selector.Select(context.Background(), opt)
		if err != nil || result.CacheHit || len(evaluator.calls) != before+1 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
}

func TestMalformedCacheTraceCannotBypassGroupGates(t *testing.T) {
	data := page(41)
	snapshot, err := Capture(context.Background(), browserFor(data), 12, "controls", "")
	if err != nil {
		t.Fatal(err)
	}
	decision, err := evaluateCandidates(context.Background(), &fakeEvaluator{target: "Item 0"}, options().Goal, snapshot.Candidates)
	if err != nil {
		t.Fatal(err)
	}
	if !validEvaluated(decision, snapshot.Candidates) {
		t.Fatal("valid grouped decision rejected")
	}
	for _, mutate := range []func(*evaluated){
		func(v *evaluated) { v.Decisions = v.Decisions[len(v.Decisions)-1:] },
		func(v *evaluated) { v.Decisions[0].Stage = "arbitrary" },
		func(v *evaluated) { v.Decisions[0].CandidateIDs[0] = "unobserved" },
	} {
		encoded, _ := json.Marshal(decision)
		var broken evaluated
		_ = json.Unmarshal(encoded, &broken)
		mutate(&broken)
		if validEvaluated(broken, snapshot.Candidates) {
			t.Fatal("malformed cache trace accepted")
		}
	}
}

func TestCacheEntryCountBoundAndProviderErrorsStaySafe(t *testing.T) {
	cache := NewCache(t.TempDir())
	for index := 0; index < 135; index++ {
		cache.store(digest(fmt.Sprint(index)), evaluated{}, time.Now())
	}
	entries, _ := os.ReadDir(cache.Directory)
	if len(entries) != maxCacheEntries {
		t.Fatalf("cacheentries=%d", len(entries))
	}
	_, err := (Selector{Browser: browserFor(page(1)), Evaluator: &fakeEvaluator{fail: true}}).Select(context.Background(), options())
	if err == nil || strings.Contains(err.Error(), "PRIVATE_API_KEY") {
		t.Fatalf("unsafe error=%v", err)
	}
}

func TestQuestionDescriptionsContainOnlyMinimizedDataAndFitConservativeBudget(t *testing.T) {
	value := page(40)
	for _, item := range value.frames[0].nodes[1:] {
		item["name"] = map[string]any{"value": strings.Repeat("a", 180)}
	}
	evaluator := &fakeEvaluator{}
	_, err := (Selector{Browser: browserFor(value), Evaluator: evaluator}).Select(context.Background(), options())
	if err != nil {
		t.Fatal(err)
	}
	call := evaluator.calls[0]
	state, _ := json.Marshal(call.state)
	question, _ := json.Marshal(call.questions["selection"])
	if len(state)+len(question) > 24*1024 {
		t.Fatalf("contextbytes=%d", len(state)+len(question))
	}
	var parsed map[string]any
	_ = json.Unmarshal(state, &parsed)
	if !reflect.DeepEqual(parsed, map[string]any{"goal": options().Goal}) {
		t.Fatalf("state=%v", parsed)
	}
}
