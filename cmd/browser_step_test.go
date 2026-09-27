package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/repplus/rep-cli/internal/jevrpc"
)

func TestParseStepValuesKeepsValuesLocalAndOrdered(t *testing.T) {
	file := filepath.Join(t.TempDir(), "values.json")
	if err := os.WriteFile(file, []byte(`{"zip":"87106","email":"a@b.example"}`), 0600); err != nil {
		t.Fatal(err)
	}
	values, err := parseStepValues([]string{"full name=Sijan K=Tester"}, "hello", file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(values.keys, ",") != "email,zip,full name,text" || values.values["full name"] != "Sijan K=Tester" || values.values["text"] != "hello" {
		t.Fatalf("values %+v", values)
	}
	for _, bad := range [][]string{{"novalue"}, {"a=1", "a=2"}} {
		if _, err := parseStepValues(bad, "", ""); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	if _, err := parseStepValues(nil, "", filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("accepted a missing values file")
	}
}

func TestStepHistoryPersistsWithoutValues(t *testing.T) {
	t.Setenv("REP_WORKSPACE", "")
	t.Setenv("REP_TASK", "")
	t.Setenv("TMPDIR", t.TempDir())
	changed := true
	history := []jevdom.StepHistory{}
	for i := 0; i < 14; i++ {
		history = append(history, jevdom.StepHistory{Operation: jevdom.OpTypeText, Target: "textbox Email", ValueKey: "email", Status: "verified", PageChanged: &changed})
	}
	if err := saveStepHistory(9, history); err != nil {
		t.Fatal(err)
	}
	loaded := loadStepHistory(9)
	if len(loaded) != jevdom.MaxStepHistory || loaded[0].ValueKey != "email" {
		t.Fatalf("history %+v", loaded)
	}
	path, _ := stepHistoryPath(9)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("history is not private: %v", err)
	}
	if loadStepHistory(10) != nil {
		t.Fatal("another tab's history leaked")
	}
}

// stepHost answers capability and step requests like rep-host.
type stepHost struct {
	t        *testing.T
	request  jevrpc.StepRequest
	decision jevdom.StepDecision
	oldHost  bool
}

func (host *stepHost) Call(_ context.Context, method string, params any, out any) error {
	switch method {
	case "jev.capabilities":
		data, _ := json.Marshal(jevrpc.Capabilities{Version: jevrpc.Version, PersistentSelection: true, BatchSelection: true, StepDecision: !host.oldHost})
		return json.Unmarshal(data, out)
	case "jev.step":
		encoded, _ := json.Marshal(params)
		if err := json.Unmarshal(encoded, &host.request); err != nil {
			host.t.Fatal(err)
		}
		data, _ := json.Marshal(host.decision)
		return json.Unmarshal(data, out)
	}
	return errors.New("unexpected method " + method)
}

func runStepCommand(t *testing.T, host *stepHost, args ...string) (string, error) {
	t.Helper()
	command := newBrowserStepCommand(jevDOMDependencies{
		loadConfig: func() (jev.Config, error) { return jev.Config{APIKey: "command-key", Model: "jev-1.13.0"}, nil },
		browser:    func(context.Context, string) (jevdom.Browser, error) { return host, nil },
	})
	var out bytes.Buffer
	command.SetArgs(args)
	command.SetOut(&out)
	command.SetErr(&bytes.Buffer{})
	err := command.Execute()
	return out.String(), err
}

func TestStepCommandSendsOnlyValueNamesAndNeverActsWithoutApply(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	host := &stepHost{t: t, decision: jevdom.StepDecision{Status: "selected", Operation: jevdom.OpTypeText, ValueKey: "email", Target: &jevdom.Candidate{ID: "c1", Role: "textbox", Name: "Email"}}}
	out, err := runStepCommand(t, host, "Sign up", "--tab", "5", "--value", "email=private@example.com")
	if err != nil {
		t.Fatalf("step failed: %v %s", err, out)
	}
	request, _ := json.Marshal(host.request)
	if strings.Contains(string(request), "private@example.com") || !strings.Contains(string(request), `"value_keys":["email"]`) {
		t.Fatalf("value handling is wrong: %s", request)
	}
	if host.request.Credentials == nil || host.request.Credentials.APIKey != "command-key" || host.request.Options.Model != "jev-1.13.0" || host.request.Options.Owner == "" {
		t.Fatalf("host request lost configuration: %s", request)
	}
	var envelope struct {
		Data *stepOutput `json:"data"`
	}
	var result stepOutput
	if json.Unmarshal([]byte(out), &envelope) == nil && envelope.Data != nil {
		result = *envelope.Data
	} else if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not JSON: %s", out)
	}
	if result.Applied || result.Execution != nil || !strings.Contains(result.Next, "--apply") {
		t.Fatalf("unexpected output %s", out)
	}
	if strings.Contains(out, "private@example.com") {
		t.Fatal("the value was echoed")
	}
}

func TestStepCommandRefusesUnselectedDecisionsAndOldHosts(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	host := &stepHost{t: t, decision: jevdom.StepDecision{Status: "needs_review", Operation: jevdom.OpClick, Reason: "low confidence"}}
	out, err := runStepCommand(t, host, "Delete it", "--tab", "5", "--apply")
	if err == nil || !strings.Contains(err.Error(), "nothing was performed") {
		t.Fatalf("an unselected decision was applied: %v %s", err, out)
	}
	if _, err := runStepCommand(t, &stepHost{t: t, oldHost: true}, "Go", "--tab", "5"); err == nil {
		t.Fatal("an outdated host was used")
	}
	host = &stepHost{t: t, decision: jevdom.StepDecision{Status: "selected", Operation: jevdom.OpTypeText, ValueKey: "", Target: &jevdom.Candidate{ID: "c1", Role: "textbox", Name: "Search"}}}
	if out, err := runStepCommand(t, host, "Search", "--tab", "5", "--apply"); err == nil || !strings.Contains(err.Error(), "TYPE_TEXT needs a value") {
		t.Fatalf("TYPE_TEXT without a value was attempted: %v %s", err, out)
	}
}

func TestPendingStepIsReusedOnceOnlyForTheSameRequest(t *testing.T) {
	t.Setenv("REP_WORKSPACE", "")
	t.Setenv("REP_TASK", "")
	t.Setenv("TMPDIR", t.TempDir())
	decision := jevdom.StepDecision{Status: "selected", Operation: jevdom.OpClick, Target: &jevdom.Candidate{ID: "c1", Role: "button", Name: "Buy"}, Binding: &jevdom.Binding{Generation: "g", SnapshotFingerprint: strings.Repeat("a", 64)}}
	input := jevdom.StepOptions{Options: jevdom.Options{Goal: "Buy the cheaper product"}, ValueKeys: []string{"email"}}
	now := time.Now()
	save := func(at time.Time) {
		savePendingStep(3, pendingStep{Goal: input.Goal, Values: input.ValueKeys, History: 0, SavedAt: at.UnixNano(), Decision: decision})
	}
	save(now)
	if got, ok := takePendingStep(3, input, now.Add(time.Second)); !ok || got.Target.Name != "Buy" {
		t.Fatal("a matching preview was not reused")
	}
	if _, ok := takePendingStep(3, input, now.Add(time.Second)); ok {
		t.Fatal("a preview was reused twice")
	}
	for name, change := range map[string]func(*jevdom.StepOptions){
		"goal":    func(o *jevdom.StepOptions) { o.Goal = "Buy the pricier product" },
		"values":  func(o *jevdom.StepOptions) { o.ValueKeys = nil },
		"history": func(o *jevdom.StepOptions) { o.History = []jevdom.StepHistory{{Operation: jevdom.OpClick}} },
	} {
		save(now)
		changed := input
		change(&changed)
		if _, ok := takePendingStep(3, changed, now.Add(time.Second)); ok {
			t.Fatalf("a preview was reused after its %s changed", name)
		}
	}
	save(now)
	if _, ok := takePendingStep(3, input, now.Add(pendingStepTTL+time.Second)); ok {
		t.Fatal("an expired preview was reused")
	}
}

type afterStepBrowser struct {
	controls, text       string
	controlsErr, textErr error
}

func (browser afterStepBrowser) Call(_ context.Context, method string, params any, out any) error {
	if method == "browser.cdp" {
		return json.Unmarshal([]byte(`{"result":{}}`), out)
	}
	if method != "browser.observe" {
		return errors.New("unexpected method " + method)
	}
	fingerprint, err := browser.controls, browser.controlsErr
	if params.(map[string]any)["kind"] == "text" {
		fingerprint, err = browser.text, browser.textErr
	}
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(map[string]string{"fingerprint": fingerprint})
	return json.Unmarshal(encoded, out)
}

func TestObserveAfterPreservesUnknownEvidence(t *testing.T) {
	readFailed := errors.New("observation unavailable")
	for _, test := range []struct {
		name, before, textBefore string
		browser                  afterStepBrowser
		changed, known           bool
	}{
		{name: "unchanged", before: "controls", textBefore: "text", browser: afterStepBrowser{controls: "controls", text: "text"}, known: true},
		{name: "controls unavailable", before: "controls", browser: afterStepBrowser{controlsErr: readFailed}},
		{name: "controls empty", before: "controls"},
		{name: "controls baseline empty", browser: afterStepBrowser{controls: "controls"}},
		{name: "text unavailable", before: "controls", textBefore: "text", browser: afterStepBrowser{controls: "controls", textErr: readFailed}},
		{name: "text empty", before: "controls", textBefore: "text", browser: afterStepBrowser{controls: "controls"}},
		{name: "controls changed", before: "controls", textBefore: "text", browser: afterStepBrowser{controls: "new", textErr: readFailed}, changed: true, known: true},
		{name: "text changed without controls", before: "controls", textBefore: "text", browser: afterStepBrowser{controlsErr: readFailed, text: "new"}, changed: true, known: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed, _, known := observeAfter(context.Background(), test.browser, jevdom.Options{}, test.before, test.textBefore, time.Millisecond)
			if changed != test.changed || known != test.known {
				t.Fatalf("changed=%v known=%v; want changed=%v known=%v", changed, known, test.changed, test.known)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if changed, _, known := observeAfter(ctx, afterStepBrowser{controls: "controls"}, jevdom.Options{}, "controls", "", time.Millisecond); changed || known {
		t.Fatal("canceled observation was reported as known")
	}
}

func TestApplyStepOmitsPageChangedWhenObservationFails(t *testing.T) {
	decision := jevdom.StepDecision{Status: "selected", Operation: jevdom.OpScrollDown, SnapshotFingerprint: "controls"}
	result, changed, _, err := applyStep(context.Background(), afterStepBrowser{controlsErr: errors.New("unavailable")}, jevdom.Options{}, jevdom.StepOptions{}, decision, stepValues{})
	if err != nil || result == nil || result.Status != "performed" || changed != nil {
		t.Fatalf("result=%+v page_changed=%v err=%v", result, changed, err)
	}
}
