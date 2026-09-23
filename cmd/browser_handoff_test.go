package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

type snapshotBridgeFixture struct {
	reference browserSnapshotReference
	err       error
	calls     []string
}

func (fixture *snapshotBridgeFixture) Call(_ context.Context, method string, params interface{}, out interface{}) error {
	fixture.calls = append(fixture.calls, method)
	if fixture.err != nil {
		return fixture.err
	}
	var value interface{}
	switch method {
	case "bridge.capture.capabilities":
		value = map[string]interface{}{"schema": 1, "immutable_snapshots": true, "host_instance": "fixture-host", "request_chunks": true, "max_snapshot_bytes": browserSnapshotMaxBytes}
	case "bridge.capture.get":
		value = fixture.reference
	default:
		return errors.New("unexpected browser operation")
	}
	data, _ := json.Marshal(value)
	return json.Unmarshal(data, out)
}

func configureCaptureTestScope(t *testing.T, task string) scope.Scope {
	t.Helper()
	scope.Reset()
	selected, err := scope.Configure(scope.Options{Workspace: "capture-tests", Task: task, WorkspaceSet: true, TaskSet: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scope.Reset)
	return selected
}

func createBrowserSnapshotFixture(t *testing.T, id, requestID string) *snapshotBridgeFixture {
	t.Helper()
	export := store.Export{
		Version: "2.0", SessionID: id, ExportedAt: time.Now().UTC().Format(time.RFC3339Nano),
		BrowserSession: &store.BrowserSession{Browser: "arc", TabID: 12, CaptureMode: "navigate", FinishedAt: time.Now().UTC().Format(time.RFC3339Nano)},
		Requests:       []store.Request{{ID: requestID, Method: "GET", URL: "https://" + requestID + ".test", TabID: 12, Response: &store.Response{Status: 200, Body: "private-" + requestID}}},
	}
	content, err := json.Marshal(export)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	return &snapshotBridgeFixture{reference: browserSnapshotReference{Schema: 1, SessionID: id, HostInstance: "fixture-host", Path: path, SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(content)), Requests: 1, Sealed: true}}
}

func TestScopedBrowserCaptureUsesExactSnapshotAndArchivesAcrossTasks(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("REPLIVE_PATH", "")
	firstScope := configureCaptureTestScope(t, "first")
	base, _ := scope.GlobalDataDir()
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(base, "live.json")
	global := []byte(`{"session_id":"unrelated-ebay","requests":[{"id":"ebay"}]}`)
	if err := os.WriteFile(globalPath, global, 0600); err != nil {
		t.Fatal(err)
	}
	first := createBrowserSnapshotFixture(t, "capture-first", "first")
	handoff, err := prepareBrowserCapture(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]interface{}{"session_id": "capture-first", "requests": 1}
	if err := finishBrowserCapture(context.Background(), first, result, handoff, "first task", false); err != nil {
		t.Fatal(err)
	}
	if result["saved_session_id"] != "capture-capture-first" {
		t.Fatalf("capture was not automatically archived: %#v", result)
	}
	if result["workspace"] != "capture-tests" || result["task"] != "first" || result["capture_snapshot_verified"] != true {
		t.Fatalf("capture ownership is missing: %#v", result)
	}
	redacted := make(map[string]interface{}, len(result))
	for key, value := range result {
		redacted[key] = value
	}
	redactSensitiveBrowserResult(redacted, "open")
	if redacted["workspace"] != "capture-tests" || redacted["task"] != "first" || redacted["capture_snapshot_verified"] != true || redacted["saved_session_id"] != result["saved_session_id"] {
		t.Fatalf("redaction removed capture ownership: %#v", redacted)
	}
	firstData, err := os.ReadFile(firstScope.LivePath)
	if err != nil || !strings.Contains(string(firstData), "private-first") || strings.Contains(string(firstData), "ebay") {
		t.Fatalf("wrong scoped capture: %s %v", firstData, err)
	}
	secondScope := configureCaptureTestScope(t, "second")
	second := createBrowserSnapshotFixture(t, "capture-second", "second")
	handoff, err = prepareBrowserCapture(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if err := finishBrowserCapture(context.Background(), second, map[string]interface{}{"session_id": "capture-second", "requests": 1}, handoff, "second task", false); err != nil {
		t.Fatal(err)
	}
	secondData, err := os.ReadFile(secondScope.LivePath)
	if err != nil || strings.Contains(string(secondData), "private-first") || !strings.Contains(string(secondData), "private-second") {
		t.Fatalf("task scopes mixed: %s %v", secondData, err)
	}
	secondStore, err := store.Load()
	if err != nil || len(secondStore.Sessions) != 1 || secondStore.Sessions[0].CaptureSessionID != "capture-second" {
		t.Fatalf("archive scopes mixed: %#v %v", secondStore, err)
	}
	configureCaptureTestScope(t, "first")
	firstStore, err := store.Load()
	if err != nil || len(firstStore.Sessions) != 1 || firstStore.Sessions[0].CaptureSessionID != "capture-first" || firstStore.Sessions[0].CaptureDigest != first.reference.SHA256 {
		t.Fatalf("first capture provenance lost: %#v %v", firstStore, err)
	}
	next := createBrowserSnapshotFixture(t, "capture-first-next", "first-next")
	if err := finishBrowserCapture(context.Background(), next, map[string]interface{}{"session_id": "capture-first-next", "requests": 1}, handoff, "followup", false); err != nil {
		t.Fatal(err)
	}
	firstStore, err = store.Load()
	if err != nil || len(firstStore.Sessions) != 2 {
		t.Fatalf("prior capture was not retained: %#v %v", firstStore, err)
	}
	archivedFirst := firstStore.GetSession("capture-capture-first")
	if archivedFirst == nil || len(archivedFirst.Requests) != 1 || archivedFirst.Requests[0].Response.Body != "private-first" {
		t.Fatal("earlier request body became unavailable after a later capture")
	}
	unchanged, _ := os.ReadFile(globalPath)
	if string(unchanged) != string(global) {
		t.Fatalf("task capture overwrote global live: %s", unchanged)
	}
}

func TestScopedCapturePreflightRejectsOldHostBeforeNavigation(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("REPLIVE_PATH", "")
	configureCaptureTestScope(t, "legacy")
	server := newFakeBrowserBridgeServer(t, func(request bridge.RPCRequest) fakeBrowserBridgeReply {
		if request.Method == "bridge.ping" {
			return fakeBrowserBridgeReply{result: map[string]interface{}{"connected": true}}
		}
		return fakeBrowserBridgeReply{rpcErr: &bridge.RPCError{Code: "unknown_method", Message: "unsupported"}}
	})
	command := &cobra.Command{}
	command.SetContext(context.Background())
	err := runBrowserOpen(command, "https://example.test", browserOpenFlags{Browser: "arc", TabID: -1, Timeout: time.Second})
	if err == nil {
		t.Fatal("old host accepted scoped capture")
	}
	requests := server.Requests()
	if len(requests) != 2 || requests[0].Method != "bridge.ping" || requests[1].Method != "bridge.capture.capabilities" {
		t.Fatalf("browser operation happened before capability negotiation: %#v", requests)
	}
}

func TestExactCaptureValidationNeverFallsBackToLive(t *testing.T) {
	for _, scenario := range []string{"wrong-session", "wrong-host", "wrong-digest", "wrong-count", "unsealed", "expired", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("REPLIVE_PATH", "")
			selected := configureCaptureTestScope(t, "validation")
			fixture := createBrowserSnapshotFixture(t, "expected", "expected")
			switch scenario {
			case "wrong-session":
				fixture.reference.SessionID = "other"
			case "wrong-host":
				fixture.reference.HostInstance = "other"
			case "wrong-digest":
				fixture.reference.SHA256 = strings.Repeat("0", 64)
			case "wrong-count":
				fixture.reference.Requests = 2
			case "unsealed":
				fixture.reference.Sealed = false
			case "expired":
				fixture.err = errors.New("capture_unavailable")
			case "oversized":
				fixture.reference.Bytes = browserSnapshotMaxBytes + 1
			}
			err := finishBrowserCapture(context.Background(), fixture, map[string]interface{}{"session_id": "expected", "requests": 1}, browserCaptureHandoff{Scoped: true, Immutable: true, HostInstance: "fixture-host"}, "", false)
			if err == nil {
				t.Fatal("invalid exact capture accepted")
			}
			if _, err := os.Stat(selected.LivePath); !os.IsNotExist(err) {
				t.Fatalf("invalid capture published: %v", err)
			}
		})
	}
}

func TestScopedAmbientCaptureRejectedBeforeBridgeUse(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	configureCaptureTestScope(t, "ambient")
	if err := runBrowserWatch(&cobra.Command{}, "start"); err == nil {
		t.Fatal("scoped ambient all-tab capture was accepted")
	}
}

func TestScopedCaptureBoundsThousandRequestPreviewAndRetainsEveryBody(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("REPLIVE_PATH", "")
	selected := configureCaptureTestScope(t, "large")
	fixture := createBrowserSnapshotFixture(t, "large-capture", "placeholder")
	data, err := os.ReadFile(fixture.reference.Path)
	if err != nil {
		t.Fatal(err)
	}
	var export store.Export
	if err := json.Unmarshal(data, &export); err != nil {
		t.Fatal(err)
	}
	export.Requests = make([]store.Request, 1000)
	for index := range export.Requests {
		export.Requests[index] = store.Request{ID: fmt.Sprintf("h_%016x", index+1), Method: "GET", URL: fmt.Sprintf("https://example.test/items/%d", index), TabID: 12, Response: &store.Response{Status: 200, Body: fmt.Sprintf("private-response-%d", index)}}
	}
	data, err = json.Marshal(export)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.reference.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	fixture.reference.SHA256, fixture.reference.Bytes, fixture.reference.Requests = hex.EncodeToString(digest[:]), int64(len(data)), 1000
	handoff, err := prepareBrowserCapture(context.Background(), fixture)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]interface{}{"session_id": "large-capture", "requests": 1000}
	if err := finishBrowserCapture(context.Background(), fixture, result, handoff, "large fixture", false); err != nil {
		t.Fatal(err)
	}
	descriptors := result["captured_requests"].([]browserCapturedRequest)
	if len(descriptors) != 16 || descriptors[0].Sequence != 1 || descriptors[15].Sequence != 16 || result["captured_requests_total"] != 1000 || result["captured_requests_omitted"] != 984 || result["captured_requests_complete"] != false {
		t.Fatalf("capture preview is not bounded and explicit: %#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > 8192 || strings.Contains(string(encoded), "private-response") {
		t.Fatalf("capture preview leaked or grew: %d bytes %v", len(encoded), err)
	}
	live, err := loadLiveExport(selected.LivePath)
	if err != nil || len(live.Requests) != 1000 {
		t.Fatalf("live evidence was truncated: %v", err)
	}
	saved, err := store.Load()
	if err != nil || len(saved.Sessions) != 1 || len(saved.Sessions[0].Requests) != 1000 || saved.Sessions[0].Requests[999].Response.Body != "private-response-999" {
		t.Fatalf("archive evidence was truncated: %v", err)
	}
	redactSensitiveBrowserResult(result, "open")
	if result["captured_requests_total"] != 1000 || result["captured_requests_omitted"] != 984 || result["captured_requests_complete"] != false || result["saved_hash_id"] == nil {
		t.Fatalf("redaction lost omission evidence: %#v", result)
	}

	if _, err := scope.Configure(scope.Options{Global: true}); err != nil {
		t.Fatal(err)
	}
	handoff, err = prepareBrowserCapture(context.Background(), fixture)
	if err != nil {
		t.Fatal(err)
	}
	globalResult := map[string]interface{}{"session_id": "large-capture", "requests": 1000}
	if err := finishBrowserCapture(context.Background(), fixture, globalResult, handoff, "", false); err != nil {
		t.Fatal(err)
	}
	if len(globalResult["captured_requests"].([]browserCapturedRequest)) != 1000 {
		t.Fatal("explicit global capture changed its descriptor semantics")
	}
	if _, exists := globalResult["captured_requests_omitted"]; exists {
		t.Fatal("scoped preview metadata appeared in global capture")
	}
}

func TestScopedTerminalOutcomeBoundsListsAndRetainsTerminalEvidence(t *testing.T) {
	ids := make([]string, 1000)
	for index := range ids {
		ids[index] = fmt.Sprintf("h_%016x", index+1)
	}
	outcome := &browserTerminalOutcome{
		Kind: "redirect_chain", Completed: true, TerminalResponseReceived: true,
		SourceRequestID: ids[0], TerminalRequestID: ids[999], TerminalStatus: 200,
		RedirectHops: 999, RequestIDs: append([]string(nil), ids...),
		LaterFormFailures: 1000, LaterFormFailureIDs: append([]string(nil), ids...),
	}
	result := map[string]interface{}{"terminal_outcome": outcome}
	boundScopedBrowserCaptureResult(result)
	redactSensitiveBrowserResult(result, "open")
	bounded, ok := result["terminal_outcome"].(*browserTerminalOutcome)
	if !ok || bounded.TerminalRequestID != ids[999] || bounded.SourceRequestID != ids[0] || !bounded.Completed || bounded.TerminalStatus != 200 || bounded.RedirectHops != 999 {
		t.Fatalf("terminal evidence was lost: %#v", result)
	}
	if len(bounded.RequestIDs) != 16 || bounded.RequestIDsTotal != 1000 || bounded.RequestIDsOmitted != 984 || len(bounded.LaterFormFailureIDs) != 16 || bounded.LaterFormFailureIDsTotal != 1000 || bounded.LaterFormFailureIDsOmitted != 984 || bounded.LaterFormFailures != 1000 {
		t.Fatalf("terminal lists were not explicitly bounded: %#v", bounded)
	}
}
