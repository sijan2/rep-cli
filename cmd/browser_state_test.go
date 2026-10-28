package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
)

func stateTestCaller(t *testing.T, signals string, shot string) fastTestCaller {
	return func(_ context.Context, method string, in, out any) error {
		switch method {
		case "browser.lease.acquire":
			return fastTestDecode(out, `{"lease_id":"lease","generation":"generation"}`)
		case "browser.lease.release":
			return fastTestDecode(out, `{}`)
		case "browser.cdp":
			p := in.(map[string]any)
			switch p["method"] {
			case "Runtime.evaluate":
				expression := p["command_params"].(map[string]any)["expression"].(string)
				if strings.Contains(expression, `"operation":"state"`) {
					return fastTestDecode(out, `{"result":{"result":{"type":"object","value":{"status":"observed","url":"https://shop.example.test/","title":"Shop","page_state":`+signals+`,"stack":{"frameworks":["nextjs"]},"data_requests":{"fetch_xhr_requests":3,"endpoints":[]}}}}}`)
				}
				return fastTestDecode(out, `{"result":{"result":{"type":"object","value":{"href":"https://shop.example.test/","title":"Shop","dpr":2,"w":400,"h":300,"x":0,"y":0,"sw":400}}}}`)
			case "Page.captureScreenshot":
				return fastTestDecode(out, `{"result":{"data":"`+shot+`"}}`)
			}
		}
		return errors.New("unexpected RPC " + method)
	}
}

func TestStateUsesSignalsAloneForClearContent(t *testing.T) {
	fastTestScope(t)
	asked := false
	asker := &fakeAsker{}
	caller := stateTestCaller(t, `{"kind":"content","hints":[],"ready_state":"complete","text_chars":1800,"visible_controls":30,"dialogs":0,"password_fields":0,"new_resources_in_window":0,"mutations_in_window":0,"window_ms":300}`, "")
	command := newBrowserStateCommand(jevDOMDependencies{
		loadConfig: func() (jev.Config, error) { return jev.Config{APIKey: "k"}, nil },
		browser:    func(context.Context, string) (jevdom.Browser, error) { return caller, nil },
		asker:      func(jev.Config) jev.Asker { asked = true; return asker },
	})
	addDecisionBrowserFlag(command)
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"--tab", "9"})
	if err := command.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stdout.String())
	}
	var result stateOutput
	if json.Unmarshal(stdout.Bytes(), &result) != nil || result.Kind != "content" || result.Basis != "in_page_signals" || result.ModelRequests != 0 || asked || result.Next == "" {
		t.Fatalf("clear content must not spend a model call: %s", stdout.String())
	}
	if !strings.Contains(string(result.Stack), "nextjs") || !strings.Contains(string(result.DataRequests), "fetch_xhr_requests") {
		t.Fatalf("stack and data requests must pass through: %s", stdout.String())
	}
}

func TestStateAsksJevWhenSignalsAreAmbiguous(t *testing.T) {
	fastTestScope(t)
	asker := &fakeAsker{response: jev.Response{Model: "jev-1.14.0", Usage: jev.Usage{InputTokens: 3000}, Answers: map[string]jev.TypedAnswer{
		"state": {Type: "choice", Choice: "login_wall", Confidence: ptr(0.91), Probabilities: map[string]float64{"login_wall": 0.9, "loading": 0.05, "content": 0.05, "consent_dialog": 0, "challenge": 0, "error": 0, "empty": 0}},
	}}}
	caller := stateTestCaller(t, `{"kind":"loading","hints":["login_text"],"ready_state":"complete","text_chars":40,"visible_controls":2,"dialogs":0,"password_fields":0,"new_resources_in_window":0,"mutations_in_window":0,"window_ms":300}`, locateTestPNG(t, 800, 600))
	command := newBrowserStateCommand(jevDOMDependencies{
		loadConfig: func() (jev.Config, error) { return jev.Config{APIKey: "k"}, nil },
		browser:    func(context.Context, string) (jevdom.Browser, error) { return caller, nil },
		asker:      func(jev.Config) jev.Asker { return asker },
	})
	addDecisionBrowserFlag(command)
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"--tab", "9"})
	if err := command.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stdout.String())
	}
	var result stateOutput
	if json.Unmarshal(stdout.Bytes(), &result) != nil || result.Kind != "login_wall" || result.Basis != "jev_screenshot_choice" || result.ModelRequests != 1 || result.Judgment == nil || result.Judgment.Confidence != 0.91 {
		t.Fatalf("ambiguous signals must be decided by one Jev choice: %s", stdout.String())
	}
	if result.Next != "" || !strings.Contains(result.Advice, "Sign in manually") {
		t.Fatalf("a login wall has no automatic next command: %s", stdout.String())
	}
	if len(asker.request.Questions["state"].Choices) != len(stateKinds) || asker.request.Questions["state"].Type != "choice" {
		t.Fatalf("the question must offer every kind once: %+v", asker.request.Questions)
	}
	if encoded, _ := json.Marshal(asker.request.State); strings.Contains(string(encoded), "shop.example.test") {
		t.Fatal("page URL must not enter model state")
	}
}

func TestStateWithoutProviderStaysHeuristic(t *testing.T) {
	fastTestScope(t)
	caller := stateTestCaller(t, `{"kind":"loading","hints":[],"ready_state":"interactive","text_chars":0,"visible_controls":1,"dialogs":0,"password_fields":0,"new_resources_in_window":4,"mutations_in_window":12,"window_ms":300}`, "")
	command := newBrowserStateCommand(jevDOMDependencies{
		loadConfig: func() (jev.Config, error) {
			return jev.Config{}, &jev.Error{Code: jev.CodeNotConfigured, Message: "no key"}
		},
		browser: func(context.Context, string) (jevdom.Browser, error) { return caller, nil },
	})
	addDecisionBrowserFlag(command)
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"--tab", "9"})
	if err := command.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stdout.String())
	}
	var result stateOutput
	if json.Unmarshal(stdout.Bytes(), &result) != nil || result.Kind != "loading" || result.Basis != "in_page_signals_only" || result.ModelRequests != 0 || !strings.Contains(result.Next, "--timeout 10s") {
		t.Fatalf("heuristic fallback wrong: %s", stdout.String())
	}
}
