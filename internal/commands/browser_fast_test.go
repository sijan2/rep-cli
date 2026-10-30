package commands

import (
	"context"
	"encoding/json"

	"testing"

	"github.com/repplus/rep-cli/internal/agentstate"

	"github.com/repplus/rep-cli/internal/scope"
)

type fastTestCaller func(context.Context, string, any, any) error

func (f fastTestCaller) Call(ctx context.Context, method string, in, out any) error {
	return f(ctx, method, in, out)
}
func fastTestDecode(out any, value string) error { return json.Unmarshal([]byte(value), out) }

func fastTestScope(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	_, err := scope.Configure(scope.Options{Workspace: "fast-test", Task: "action", WorkspaceSet: true, TaskSet: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scope.Reset)
	registry, _ := coreStore()
	_, err = registry.Register(agentstate.Browser{Name: defaultBrowser, Profile: "fixture", Epoch: "fixture"}, 7, "fast-test/action", "", true)
	if err != nil {
		t.Fatal(err)
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
