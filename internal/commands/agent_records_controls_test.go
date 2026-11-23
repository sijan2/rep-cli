package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/scene"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

func sceneBridgeBrowser() agentstate.Browser {
	return agentstate.Browser{Name: "dia", Profile: "profile_" + strings.Repeat("a", 32), Epoch: "epoch_" + strings.Repeat("b", 32)}
}

// sceneHandler answers the geometry RPC with the scene fixture and the act
// sequence with a healthy page whose frame still matches the fixture.
func sceneHandler(t *testing.T, geometry []byte, clickable int, actCalls *[]bridge.RPCRequest) func(bridge.RPCRequest) fakeBrowserBridgeReply {
	t.Helper()
	return func(request bridge.RPCRequest) fakeBrowserBridgeReply {
		ok := func(result any) fakeBrowserBridgeReply { return fakeBrowserBridgeReply{result: result} }
		name := fakeBrowserRequestName(request)
		if strings.HasPrefix(name, "browser.cdp/") && actCalls != nil {
			*actCalls = append(*actCalls, request)
		}
		switch name {
		case "bridge.ping":
			return ok(map[string]any{"connected": true})
		case "browser.geometry":
			return ok(json.RawMessage(geometry))
		case "browser.lease.acquire":
			return ok(map[string]any{"lease_id": "l_" + strings.Repeat("d", 32), "generation": "g1"})
		case "browser.lease.release":
			return ok(map[string]any{"released": true})
		case "browser.cdp/Page.getFrameTree":
			return ok(map[string]any{"result": map[string]any{"frameTree": map[string]any{"frame": map[string]any{"id": "F1", "loaderId": "L1", "url": "https://www.reddit.com/r/email/comments/abc123/why_do_smtp_relays_charge_per_1k/"}}}})
		case "browser.cdp/DOM.resolveNode":
			return ok(map[string]any{"result": map[string]any{"object": map[string]any{"objectId": "obj-1"}}})
		case "browser.cdp/Runtime.callFunctionOn":
			var params struct {
				Params struct {
					Arguments []map[string]any `json:"arguments"`
				} `json:"command_params"`
			}
			decodeFakeBrowserParams(t, request, &params)
			if len(params.Params.Arguments) > 0 {
				if _, validation := params.Params.Arguments[0]["value"]; validation {
					return ok(map[string]any{"result": map[string]any{"result": map[string]any{"type": "object", "value": map[string]any{"connected": true, "label": "reply", "label_ok": true, "key_ok": true}}}})
				}
			}
			return ok(map[string]any{"result": map[string]any{"result": map[string]any{"type": "boolean", "value": true}}})
		case "browser.cdp/DOM.scrollIntoViewIfNeeded":
			return ok(map[string]any{"result": map[string]any{}})
		case "browser.cdp/DOM.getContentQuads":
			return ok(map[string]any{"result": map[string]any{"quads": [][]float64{{100, 200, 180, 200, 180, 232, 100, 232}}}})
		case "browser.cdp/DOM.getNodeForLocation":
			return ok(map[string]any{"result": map[string]any{"backendNodeId": clickable, "frameId": "F1"}})
		case "browser.cdp/Input.dispatchMouseEvent", "browser.cdp/Input.dispatchKeyEvent":
			return ok(map[string]any{"result": map[string]any{}})
		}
		return fakeBrowserBridgeReply{rpcErr: &bridge.RPCError{Code: "unknown_method", Message: name}}
	}
}

func registerSceneTab(t *testing.T) *agentstate.Tab {
	t.Helper()
	store, err := coreStore()
	if err != nil {
		t.Fatal(err)
	}
	selected, _ := scope.Current()
	tab, err := store.Register(sceneBridgeBrowser(), 7, selected.Identity(), selected.SessionKey, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Change(tab.Handle, "", func(stored *agentstate.Tab) error {
		stored.URL = "https://www.reddit.com/r/email/comments/abc123/why_do_smtp_relays_charge_per_1k/"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return tab
}

func TestRecordsControlsProjectsOwnedControlsAndWritesTheIndex(t *testing.T) {
	selected := recordsTestScope(t)
	geometry, err := os.ReadFile(filepath.Join("..", "scene", "testdata", "reddit_thread.synthetic.json"))
	if err != nil {
		t.Fatal(err)
	}
	server := newRecordsBridge(t, sceneHandler(t, geometry, 0, nil))
	tab := registerSceneTab(t)
	command := &cobra.Command{Use: "controls"}
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetContext(context.Background())
	if err := coreRecordsControls(command, tab.Handle); err != nil {
		t.Fatalf("controls: %v\n%s", err, stdout.String())
	}
	var result struct {
		Status string `json:"status"`
		Meta   struct {
			Records  int            `json:"records"`
			Controls int            `json:"controls"`
			Orphans  int            `json:"orphans"`
			Network  map[string]any `json:"network"`
			Model    int            `json:"model_requests"`
		} `json:"meta"`
		Untrusted struct {
			Lines []string `json:"lines"`
		} `json:"untrusted"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "ready" || result.Meta.Records != 5 || result.Meta.Controls != 28 || result.Meta.Orphans != 7 || result.Meta.Model != 0 || result.Meta.Network["joined"] != false {
		t.Fatalf("controls meta: %s", stdout.String())
	}
	if len(result.Untrusted.Lines) == 0 || !strings.HasPrefix(result.Untrusted.Lines[0], "r_t1_") || !strings.Contains(result.Untrusted.Lines[0], " n_") {
		t.Fatalf("projection lines: %v", result.Untrusted.Lines)
	}
	for _, line := range result.Untrusted.Lines {
		if len(line) > scene.MaxLineBytes+8 {
			t.Fatalf("line over budget: %d bytes", len(line))
		}
	}
	index, err := readSceneIndex(selected.DataDir, tab.Handle)
	if err != nil || len(index.Controls) != 35 || index.Frame6 == "" {
		t.Fatalf("scene index: %v %+v", err, index)
	}
	info, err := os.Stat(sceneIndexPath(selected.DataDir, tab.Handle))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("scene index permissions: %v", err)
	}
	for _, name := range fakeBrowserRequestNames(server.Requests()) {
		if strings.HasPrefix(name, "browser.cdp/Input.") || name == "browser.eval" {
			t.Fatalf("observation dispatched input: %v", name)
		}
	}
}

func TestDoNodeHandleClicksThroughTheLeaseAndRefusesStaleHandles(t *testing.T) {
	selected := recordsTestScope(t)
	geometry, err := os.ReadFile(filepath.Join("..", "scene", "testdata", "reddit_thread.synthetic.json"))
	if err != nil {
		t.Fatal(err)
	}
	var actCalls []bridge.RPCRequest
	tab := registerSceneTab(t)
	// Observe first so the index exists, then pick a record-owned button.
	server := newRecordsBridge(t, sceneHandler(t, geometry, 0, nil))
	observe := &cobra.Command{Use: "controls"}
	observe.SetOut(&bytes.Buffer{})
	observe.SetContext(context.Background())
	if err := coreRecordsControls(observe, tab.Handle); err != nil {
		t.Fatal(err)
	}
	server.Close()
	index, err := readSceneIndex(selected.DataDir, tab.Handle)
	if err != nil {
		t.Fatal(err)
	}
	var handle string
	var control scene.Control
	for h, c := range index.Controls {
		if c.Owner != "" && c.Role == "button" && (handle == "" || h < handle) {
			handle, control = h, c
		}
	}
	if handle == "" {
		t.Fatal("fixture has no record-owned button")
	}
	server = newRecordsBridge(t, sceneHandler(t, geometry, control.Backend, &actCalls))
	command := &cobra.Command{Use: "do"}
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetContext(context.Background())
	if err := coreDo(command, []string{handle, tab.Handle}, "", nil, false); err != nil {
		t.Fatalf("do %s: %v\n%s", handle, err, stdout.String())
	}
	var result struct {
		Status string         `json:"status"`
		Next   string         `json:"next"`
		Meta   map[string]any `json:"meta"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "performed" || !strings.Contains(result.Next, "rep records controls "+tab.Handle) || result.Meta["handle"] != handle {
		t.Fatalf("do result: %s", stdout.String())
	}
	mouse := 0
	for _, request := range actCalls {
		var params struct {
			LeaseID string `json:"lease_id"`
			Owner   string `json:"owner"`
			Method  string `json:"method"`
		}
		decodeFakeBrowserParams(t, request, &params)
		if params.LeaseID == "" || params.Owner != selected.Identity() {
			t.Fatalf("act bypassed the execute lease: %+v", params)
		}
		if params.Method == "Input.dispatchMouseEvent" {
			mouse++
		}
	}
	if mouse != 3 {
		t.Fatalf("expected moved/pressed/released, got %d mouse events", mouse)
	}
	// A handle from another document never reaches the browser.
	actCalls = nil
	stale := "n_" + strings.Repeat("0", 6) + handle[strings.IndexByte(handle, '.'):]
	err = coreDo(command, []string{stale, tab.Handle}, "", nil, false)
	var problem *agentstate.Error
	if !errors.As(err, &problem) || problem.Code != "stale_document" || len(actCalls) != 0 {
		t.Fatalf("stale handle: %v (%d browser calls)", err, len(actCalls))
	}
	if err := coreDo(command, []string{handle}, "", nil, false); err == nil || !strings.Contains(err.Error(), "needs its tab") {
		t.Fatalf("handle without tab: %v", err)
	}
	if err := coreDo(command, []string{handle, tab.Handle, "value"}, "", nil, false); err == nil {
		t.Fatal("node handles must not accept a fill value")
	}
}
