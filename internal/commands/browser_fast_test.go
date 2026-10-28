package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
)

type fastTestCaller func(context.Context, string, any, any) error

func (f fastTestCaller) Call(ctx context.Context, method string, in, out any) error {
	return f(ctx, method, in, out)
}
func fastTestDecode(out any, value string) error { return json.Unmarshal([]byte(value), out) }

func fastTestScope(t *testing.T) {
	t.Helper()
	_, err := scope.Configure(scope.Options{Workspace: "fast-test", Task: "action", WorkspaceSet: true, TaskSet: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = scope.Configure(scope.Options{}) })
	previous := rawJSON
	rawJSON = true
	t.Cleanup(func() { rawJSON = previous })
}

func TestFastDoLostInputAckDoesNotRetryAndEmitsOneDocument(t *testing.T) {
	fastTestScope(t)
	keyCalls := 0
	caller := fastTestCaller(func(_ context.Context, method string, in, out any) error {
		switch method {
		case "browser.lease.acquire":
			return fastTestDecode(out, `{"lease_id":"lease","generation":"generation"}`)
		case "browser.lease.release":
			return fastTestDecode(out, `{}`)
		case "browser.cdp":
			p := in.(map[string]any)
			if p["method"] == "Runtime.evaluate" {
				expression := p["command_params"].(map[string]any)["expression"].(string)
				if strings.Contains(expression, `"operation":"focus_guard"`) {
					return fastTestDecode(out, `{"result":{"result":{"value":{"status":"verified","url":"https://example.test/"}}}}`)
				}
				return fastTestDecode(out, `{"result":{"result":{"value":{"status":"prepared","url":"https://example.test/"}}}}`)
			}
			if p["method"] == "Input.dispatchKeyEvent" {
				keyCalls++
				kind := p["command_params"].(map[string]any)["type"]
				if kind == "keyDown" {
					return errors.New("connection closed after dispatch")
				}
				return fastTestDecode(out, `{"result":{}}`)
			}
		}
		return errors.New("unexpected RPC " + method)
	})
	command := newBrowserDoCommand(jevDOMDependencies{browser: func(context.Context, string) (jevdom.Browser, error) { return caller, nil }})
	addDecisionBrowserFlag(command)
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"e_ref", "--tab", "7", "--press", "Enter"})
	err := command.Execute()
	if err == nil || !output.Reported(err) {
		t.Fatalf("expected reported nonzero error, got %v", err)
	}
	var result fastOutput
	if json.Unmarshal(stdout.Bytes(), &result) != nil || result.Status != "unconfirmed" || result.Code != "input_outcome_unknown" || result.Error == nil {
		t.Fatalf("bad ambiguity report: %s", stdout.String())
	}
	if keyCalls != 2 {
		t.Fatalf("keyDown was retried, or paired release missing: %d", keyCalls)
	}
}

func TestFastDoRejectsChangedFocusBeforeDispatch(t *testing.T) {
	fastTestScope(t)
	keyCalls := 0
	caller := fastTestCaller(func(_ context.Context, method string, in, out any) error {
		if method == "browser.lease.acquire" {
			return fastTestDecode(out, `{"lease_id":"lease","generation":"generation"}`)
		}
		if method == "browser.lease.release" {
			return fastTestDecode(out, `{}`)
		}
		p := in.(map[string]any)
		if p["method"] == "Input.dispatchKeyEvent" {
			keyCalls++
			return fastTestDecode(out, `{"result":{}}`)
		}
		expression := p["command_params"].(map[string]any)["expression"].(string)
		if strings.Contains(expression, `"operation":"focus_guard"`) {
			return fastTestDecode(out, `{"result":{"result":{"value":{"status":"failed","code":"focus_changed","url":"https://example.test/"}}}}`)
		}
		return fastTestDecode(out, `{"result":{"result":{"value":{"status":"prepared","url":"https://example.test/"}}}}`)
	})
	command := newBrowserDoCommand(jevDOMDependencies{browser: func(context.Context, string) (jevdom.Browser, error) { return caller, nil }})
	addDecisionBrowserFlag(command)
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"e_ref", "--tab", "7", "--press", "Enter"})
	if command.Execute() == nil {
		t.Fatal("changed focus accepted")
	}
	if keyCalls != 0 {
		t.Fatal("key dispatched into changed focus")
	}
}

func TestFastClickLostAckNeverRepeatsPress(t *testing.T) {
	fastTestScope(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	presses, releases := 0, 0
	caller := fastTestCaller(func(callCtx context.Context, method string, in, out any) error {
		switch method {
		case "browser.lease.acquire":
			return fastTestDecode(out, `{"lease_id":"lease","generation":"generation"}`)
		case "browser.lease.release":
			return fastTestDecode(out, `{}`)
		case "browser.cdp":
			params := in.(map[string]any)
			if params["method"] == "Runtime.evaluate" {
				return fastTestDecode(out, `{"result":{"result":{"value":{"status":"prepared_pointer","point":{"x":13,"y":14},"url":"https://example.test/"}}}}`)
			}
			input := params["command_params"].(map[string]any)
			if input["type"] == "mousePressed" {
				presses++
				cancel()
				return errors.New("connection lost after press")
			}
			if input["type"] == "mouseReleased" {
				if callCtx.Err() != nil {
					t.Fatal("release inherited the expired input deadline")
				}
				releases++
				return fastTestDecode(out, `{"result":{}}`)
			}
		}
		return errors.New("unexpected browser call")
	})
	command := newBrowserDoCommand(jevDOMDependencies{browser: func(context.Context, string) (jevdom.Browser, error) { return caller, nil }})
	command.SetContext(ctx)
	addDecisionBrowserFlag(command)
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"e_ref", "--tab", "7", "--click"})
	err := command.Execute()
	var result fastOutput
	if err == nil || !output.Reported(err) || json.Unmarshal(stdout.Bytes(), &result) != nil || result.Status != "unconfirmed" || result.Code != "input_outcome_unknown" || presses != 1 || releases != 1 {
		t.Fatalf("lost click acknowledgment was not preserved: presses=%d releases=%d result=%s err=%v", presses, releases, stdout.String(), err)
	}
}

func TestFastInvalidCommandsFailBeforeConnecting(t *testing.T) {
	fastTestScope(t)
	deps := jevDOMDependencies{browser: func(context.Context, string) (jevdom.Browser, error) {
		t.Fatal("invalid command connected to browser")
		return nil, nil
	}}
	for _, args := range [][]string{{"--tab", "7"}, {"--tab", "7", "--message", ""}, {"https://example.test/", "--message", "x", "--turn", "t_123"}} {
		command := newBrowserChatCommand(deps)
		addDecisionBrowserFlag(command)
		command.SetOut(new(bytes.Buffer))
		command.SetArgs(args)
		if command.Execute() == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	command := newBrowserPageCommand(deps)
	addDecisionBrowserFlag(command)
	command.SetOut(new(bytes.Buffer))
	command.SetArgs([]string{"https://user:password@example.test/"})
	if command.Execute() == nil {
		t.Fatal("credential URL accepted")
	}
}

func TestFastPagePartialWithoutCursorHasNoRetryLoop(t *testing.T) {
	fastTestScope(t)
	caller := fastTestCaller(func(_ context.Context, method string, in, out any) error {
		switch method {
		case "browser.lease.acquire":
			return fastTestDecode(out, `{"lease_id":"lease","generation":"generation"}`)
		case "browser.lease.release":
			return fastTestDecode(out, `{}`)
		case "browser.cdp":
			return fastTestDecode(out, `{"result":{"result":{"value":{"status":"partial","url":"https://example.test/","coverage":{"dom_scan_limited":true,"dom_nodes_scanned":50000,"dom_node_scan_limit":50000}}}}}`)
		default:
			return errors.New("unexpected RPC " + method)
		}
	})
	command := newBrowserPageCommand(jevDOMDependencies{browser: func(context.Context, string) (jevdom.Browser, error) { return caller, nil }})
	addDecisionBrowserFlag(command)
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"--tab", "7"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var result fastOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "partial" || result.Next != "" {
		t.Fatalf("partial terminal read suggested an identical retry: %s", stdout.String())
	}
}

func TestFastNavigationTimeoutRetainsTheAllocatedOwnedTab(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	caller := fastTestCaller(func(_ context.Context, method string, in, out any) error {
		switch method {
		case "browser.create":
			return fastTestDecode(out, `{"created":true,"tab_id":7}`)
		case "browser.navigate":
			params := in.(map[string]any)
			if params["tab_id"] != 7 || params["url"] != "https://example.test/" {
				t.Fatal("navigation did not use the preallocated owned tab")
			}
			return context.DeadlineExceeded
		}
		t.Fatal("timed-out navigation attempted attachment or dispatch")
		return nil
	})
	session, created, err := fastSession(ctx, caller, "owner", "https://example.test/", -1)
	if session == nil || session.Tab != 7 || !created || !fastReadPending(context.Background(), err) || session.Calls != 2 {
		t.Fatalf("slow navigation lost its continuation or ownership: %+v, %v, %v", session, created, err)
	}
}

func TestFastShortNavigationBudgetRemainsUsable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	caller := fastTestCaller(func(callCtx context.Context, method string, in, out any) error {
		if method == "browser.navigate" {
			deadline, bounded := callCtx.Deadline()
			if !bounded || time.Until(deadline) <= 0 || callCtx.Err() != nil {
				t.Fatal("short configured budget expired before navigation")
			}
			return context.DeadlineExceeded
		}
		t.Fatal("explicit tab unexpectedly created or attached")
		return nil
	})
	session, created, err := fastSession(ctx, caller, "owner", "https://example.test/", 7)
	if session == nil || session.Tab != 7 || created || !fastReadPending(ctx, err) {
		t.Fatalf("short pending navigation lost its explicit tab: %+v, %v, %v", session, created, err)
	}
}

func TestReferenceNavigationHandsOffAtCommitBeforeDOMReadiness(t *testing.T) {
	acquired := false
	caller := fastTestCaller(func(_ context.Context, method string, in, out any) error {
		switch method {
		case "browser.create":
			return fastTestDecode(out, `{"created":true,"tab_id":7}`)
		case "browser.navigate":
			params := in.(map[string]any)
			if params["wait"] != "commit" || params["tab_id"] != 7 {
				t.Fatal("reference readiness is still held by temporary navigation attachment")
			}
			return fastTestDecode(out, `{"tab_id":7,"reached":"commit","ready_state":"loading"}`)
		case "browser.lease.acquire":
			acquired = true
			return fastTestDecode(out, `{"lease_id":"lease","generation":"generation"}`)
		default:
			t.Fatal("unexpected RPC " + method)
			return nil
		}
	})
	session, created, err := fastSessionWithWait(context.Background(), caller, "owner", "https://example.test/", -1, "commit")
	if err != nil || !created || session == nil || session.Tab != 7 || !acquired || session.Calls != 3 {
		t.Fatalf("reference lost its leased readiness observation: %+v, %v, %v", session, created, err)
	}
}

func TestCloseTabGrammarNeverAcceptsPartialOrConflictingIDs(t *testing.T) {
	for _, c := range []struct {
		args []string
		flag int
		set  bool
		want int
	}{{[]string{"12"}, -1, false, 12}, {nil, 12, true, 12}, {[]string{"12junk"}, -1, false, -1}, {[]string{"12"}, 12, true, -1}, {nil, -1, false, -1}, {nil, -1, true, -1}} {
		got, err := resolveCloseTabID(c.args, c.flag, c.set)
		if got != c.want || (err != nil) != (c.want < 0) {
			t.Fatalf("%+v: got %d,%v", c, got, err)
		}
	}
}

func TestHydrationPendingOnlyForShortColdEmptyReads(t *testing.T) {
	empty := &browserfast.Result{Status: "ready"}
	if !hydrationPending(empty, true, 2*time.Second) {
		t.Fatal("a cold two-second read with nothing on the page is still loading")
	}
	if hydrationPending(empty, false, 2*time.Second) || hydrationPending(empty, true, 10*time.Second) {
		t.Fatal("warm reads and long budgets are final")
	}
	withText := &browserfast.Result{Status: "ready", Text: "hello"}
	if hydrationPending(withText, true, 2*time.Second) {
		t.Fatal("content means the page rendered")
	}
	shell := &browserfast.Result{Status: "ready", Controls: []browserfast.Control{{Ref: "e_1"}, {Ref: "e_2"}}}
	if !hydrationPending(shell, true, 2*time.Second) {
		t.Fatal("two scaffold controls with no text is still an unhydrated shell")
	}
	form := &browserfast.Result{Status: "ready", Controls: make([]browserfast.Control, 6)}
	if hydrationPending(form, true, 2*time.Second) {
		t.Fatal("several controls mean the page rendered even without main text")
	}
}

func TestRebindAfterURLChangeRetriesOnceOnTheSameTab(t *testing.T) {
	leases, evals := 0, 0
	caller := fastTestCaller(func(_ context.Context, method string, in, out any) error {
		switch method {
		case "browser.lease.acquire":
			leases++
			return fastTestDecode(out, `{"lease_id":"lease`+string(rune('0'+leases))+`","generation":"g"}`)
		case "browser.lease.release":
			return fastTestDecode(out, `{}`)
		}
		return errors.New("unexpected RPC " + method)
	})
	session := &browserfast.Session{Browser: caller, Tab: 3, Owner: "o"}
	if err := session.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	result := fastOutput{Result: browserfast.Result{Status: "failed", Code: "page_changed"}}
	read := func() (browserfast.Result, error) {
		evals++
		return browserfast.Result{Status: "ready", URL: "https://docs.example.test/d/1/edit?tab=t.0"}, nil
	}
	if err := rebindAfterURLChange(context.Background(), session, &result, nil, read); err != nil || !result.Rebound || result.Status != "ready" || leases != 2 || evals != 1 {
		t.Fatalf("expected one re-lease and one re-read: err=%v leases=%d evals=%d %+v", err, leases, evals, result)
	}
	untouched := fastOutput{Result: browserfast.Result{Status: "failed", Code: "reference_stale"}}
	if err := rebindAfterURLChange(context.Background(), session, &untouched, nil, read); err != nil || untouched.Rebound || evals != 1 {
		t.Fatal("other failures must not be retried")
	}
}
