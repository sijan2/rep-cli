package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/scope"
)

func TestReferenceCleanupRequiresOwnedCurrentDocumentAndConfirmsClosure(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	fastTestScope(t)
	closeCalls := 0
	stillOpen := false
	caller := fastTestCaller(func(_ context.Context, method string, in, out any) error {
		switch method {
		case "browser.lease.acquire":
			return fastTestDecode(out, `{"lease_id":"lease","generation":"generation"}`)
		case "browser.cdp":
			params := in.(map[string]any)
			if params["method"] != "Page.close" || params["expected_root_url"] != "https://example.test/" || params["generation"] != "generation" || params["owner"] != "owner" {
				t.Fatalf("close lost its document/owner guard: %+v", params)
			}
			closeCalls++
			return errors.New("close acknowledgment lost")
		case "browser.tabs":
			if stillOpen {
				return fastTestDecode(out, `{"tabs":[{"id":7},{"id":99}]}`)
			}
			return fastTestDecode(out, `{"tabs":[{"id":99}]}`)
		}
		return errors.New("unexpected RPC " + method)
	})
	session := &browserfast.Session{Browser: caller, Tab: 7, Owner: "owner"}
	if err := session.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	owned := &referenceOwnedTab{Tab: 7, Browser: "arc", URL: "https://example.test/", Created: time.Now()}
	if err := referenceSaveTab(owned); err != nil {
		t.Fatal(err)
	}
	result := fastOutput{Result: browserfast.Result{Status: "ready", URL: owned.URL}}
	referenceFinishTab(session, nil, &result, "read-next")
	if closeCalls != 0 || result.TabState != "retained_external" {
		t.Fatal("external tab was adopted")
	}
	result.URL = "https://another.test/"
	referenceFinishTab(session, owned, &result, "read-next")
	if closeCalls != 0 || result.TabState != "kept_changed_document" {
		t.Fatal("changed document was closed")
	}
	result.URL = owned.URL
	result.Status = "loading"
	referenceFinishTab(session, owned, &result, "read-next")
	if closeCalls != 0 || result.TabState != "kept_pending" {
		t.Fatal("pending reference lost its tab")
	}
	result.Status = "ready"
	referenceFinishTab(session, owned, &result, "read-next")
	if closeCalls != 1 || result.TabState != "closed" || result.CleanupNext != "" {
		t.Fatalf("lost acknowledgment was not resolved: %+v", result)
	}
	if _, err := os.Stat(referenceTabPath(7)); !os.IsNotExist(err) {
		t.Fatal("closed ownership marker remained")
	}
	if referenceLoadClosedTab(7, "arc") == nil {
		t.Fatal("verified closure receipt was not retained")
	}
	if already, err := referenceAlreadyClosed(context.Background(), caller, 7, "arc"); err != nil || !already || closeCalls != 1 {
		t.Fatal("redundant close mutated the tab or failed to verify absence")
	}
	stillOpen = true
	if already, err := referenceAlreadyClosed(context.Background(), caller, 7, "arc"); err == nil || already {
		t.Fatal("a reused tab ID was treated as absent")
	}
	result = fastOutput{Result: browserfast.Result{Status: "ready", URL: owned.URL}}
	referenceFinishTab(session, owned, &result, "read-next")
	if closeCalls != 2 || result.TabState != "kept_beforeunload_or_close_failed" || result.CleanupNext != "read-next" {
		t.Fatalf("unresolved close was retried or reported complete: %+v", result)
	}
}

func TestReferenceOwnershipContinuationIsTaskScopedAndExpires(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	fastTestScope(t)
	owned := &referenceOwnedTab{Tab: 7, Browser: "arc", URL: "https://example.test/", Created: time.Now(), Keep: true}
	if err := referenceSaveTab(owned); err != nil {
		t.Fatal(err)
	}
	if got := referenceLoadTab(7, "arc"); got == nil || !got.Keep || got.URL != owned.URL {
		t.Fatal("pending read lost its keep preference or source")
	}
	if referenceLoadTab(7, "chrome") != nil {
		t.Fatal("ownership crossed browser profiles")
	}
	owned.Created = time.Now().Add(-3 * time.Minute)
	_ = referenceSaveTab(owned)
	if referenceLoadTab(7, "arc") != nil {
		t.Fatal("expired ownership adopted a tab")
	}
	owned.Created = time.Now()
	_ = referenceSaveTab(owned)
	_, _ = scope.Configure(scope.Options{Workspace: "fast-test", Task: "another", WorkspaceSet: true, TaskSet: true})
	if referenceLoadTab(7, "arc") != nil {
		t.Fatal("ownership crossed tasks")
	}
}

func TestFastBrowserJSONNeedsNoOutputFlags(t *testing.T) {
	fastTestScope(t)
	rawJSON = false
	previousEnvelope := forceEnvelope
	forceEnvelope = false
	t.Cleanup(func() { forceEnvelope = previousEnvelope })
	command := newBrowserReferenceCommand(jevDOMDependencies{})
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	if err := emitFast(command, fastOutput{Result: browserfast.Result{Status: "ready", Controls: []browserfast.Control{{Ref: "e_ref", Role: "link", Name: "A&B", Enabled: true, URL: "https://example.test/?a=1&b=2"}}}, Tab: 7}, nil); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if json.Unmarshal(stdout.Bytes(), &result) != nil || result["status"] != "ready" || result["data"] != nil || result["task"] != "action" {
		t.Fatalf("bare browser output changed shape: %s", stdout.String())
	}
	if bytes.Contains(stdout.Bytes(), []byte(`\u0026`)) || !bytes.Contains(stdout.Bytes(), []byte(`A&B`)) {
		t.Fatalf("fast JSON expanded HTML-safe escapes into agent context: %s", stdout.String())
	}
}

func TestReferenceFactsPrecedeLongTextInAgentOutput(t *testing.T) {
	fastTestScope(t)
	command := newBrowserReferenceCommand(jevDOMDependencies{})
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	reference := json.RawMessage(`{"preview":{"typography":[{"role":"body","font_size":"17px"}],"headline_css":[{"conditions":[{"kind":"media","text":"(max-width: 1068px)"}]}]}}`)
	result := fastOutput{Result: browserfast.Result{Status: "ready", Text: string(bytes.Repeat([]byte("Long unrelated content "), 600)), Reference: reference}, Tab: 7}
	if err := emitFast(command, result, nil); err != nil {
		t.Fatal(err)
	}
	first := stdout.Bytes()[:min(2000, stdout.Len())]
	if !bytes.Contains(first, []byte(`"font_size":"17px"`)) || !bytes.Contains(first, []byte(`max-width: 1068px`)) {
		t.Fatal("a bounded agent tool output hides the required reference facts")
	}
	var decoded fastOutput
	if json.Unmarshal(stdout.Bytes(), &decoded) != nil || decoded.Text != result.Text || !bytes.Equal(decoded.Reference, reference) || decoded.Tab != 7 {
		t.Fatal("priority field order changed the JSON contract")
	}
}
