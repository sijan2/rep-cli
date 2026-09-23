package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func startSnapshotHost(t *testing.T) {
	t.Helper()
	t.Setenv("REP_BRIDGE_DIR", t.TempDir())
	if err := initializeCaptureSnapshots(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeCaptureSnapshots)
	resetHostState()
}

func sealTestCapture(t *testing.T, id, requestID string) captureSnapshotDescriptor {
	t.Helper()
	handleMessage(&Message{Action: "session_begin", SessionID: id, URL: "https://example.test", TabID: 12, CaptureMode: "navigate"})
	handleMessage(&Message{Action: "add_many", SessionID: id, Requests: []Request{{ID: requestID, Method: "GET", URL: "https://example.test/" + requestID, TabID: 12, CaptureSource: "cdp"}}})
	response, _ := handleMessage(&Message{Action: "session_end", SessionID: id})
	if response["success"] != true {
		t.Fatalf("seal refused: %#v", response)
	}
	if err := sealCaptureSnapshot(); err != nil {
		t.Fatal(err)
	}
	responseRPC, handled := handleCaptureRPC(RPCRequest{ID: "get", Method: "bridge.capture.get", Params: json.RawMessage(fmt.Sprintf(`{"session_id":%q}`, id))})
	if !handled || responseRPC.Error != nil {
		t.Fatalf("get failed: %#v", responseRPC)
	}
	var descriptor captureSnapshotDescriptor
	if err := json.Unmarshal(responseRPC.Result, &descriptor); err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func TestCaptureSnapshotSurvivesNextCaptureAndRejectsLateUpdates(t *testing.T) {
	startSnapshotHost(t)
	first := sealTestCapture(t, "capture-a", "request-a")
	handleMessage(&Message{Action: "session_begin", SessionID: "capture-b", URL: "https://other.test", TabID: 23, CaptureMode: "navigate"})
	for _, message := range []*Message{
		{Action: "add_many", SessionID: "capture-a", Requests: []Request{{ID: "late-a"}}},
		{Action: "session_end", SessionID: "capture-a"},
		{Action: "session_end"},
	} {
		result, _ := handleMessage(message)
		if result["success"] != false {
			t.Fatalf("accepted cross-session message: %#v", message)
		}
	}
	handleMessage(&Message{Action: "add", Request: &Request{ID: "foreign-devtools", CaptureSource: "devtools"}})
	if liveData.SessionID != "capture-b" || len(liveData.Requests) != 0 || !browserCaptureActive {
		t.Fatalf("second capture was contaminated: %#v", liveData)
	}
	data, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot LiveData
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.SessionID != "capture-a" || len(snapshot.Requests) != 1 || snapshot.Requests[0].ID != "request-a" {
		t.Fatalf("first capture was replaced: %s", data)
	}
	info, err := os.Stat(first.Path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("snapshot permissions: %v %v", info, err)
	}
}

func TestCaptureSnapshotRetentionAndExpiryFailClosed(t *testing.T) {
	startSnapshotHost(t)
	first := sealTestCapture(t, "capture-0", "r-0")
	for i := 1; i <= captureSnapshotLimit; i++ {
		sealTestCapture(t, fmt.Sprintf("capture-%d", i), fmt.Sprintf("r-%d", i))
	}
	if _, err := os.Stat(first.Path); !os.IsNotExist(err) {
		t.Fatalf("oldest snapshot was retained: %v", err)
	}
	response, _ := handleCaptureRPC(RPCRequest{ID: "get", Method: "bridge.capture.get", Params: json.RawMessage(`{"session_id":"capture-0"}`)})
	if response.Error == nil || response.Error.Code != "capture_unavailable" {
		t.Fatalf("expired lookup did not fail closed: %#v", response)
	}
	captureSnapshots.Lock()
	entry := captureSnapshots.entries["capture-32"]
	entry.CreatedAt = time.Now().Add(-2 * captureSnapshotTTL)
	captureSnapshots.entries[entry.SessionID] = entry
	captureSnapshots.Unlock()
	response, _ = handleCaptureRPC(RPCRequest{ID: "get", Method: "bridge.capture.get", Params: json.RawMessage(`{"session_id":"capture-32"}`)})
	if response.Error == nil || response.Error.Code != "capture_unavailable" {
		t.Fatalf("old snapshot was exposed: %#v", response)
	}
}

func TestCaptureSnapshotRPCRejectsPathsAndUnknownFields(t *testing.T) {
	startSnapshotHost(t)
	for _, params := range []string{`{"session_id":"../../live"}`, `{"session_id":"ok","path":"/tmp/live.json"}`, `{"session_id":""}`} {
		response, handled := handleCaptureRPC(RPCRequest{ID: "get", Method: "bridge.capture.get", Params: json.RawMessage(params)})
		if !handled || response.Error == nil || response.Error.Code != "invalid_argument" {
			t.Fatalf("unsafe lookup accepted: %s", params)
		}
	}
	response, handled := handleCaptureRPC(RPCRequest{ID: "caps", Method: "bridge.capture.capabilities"})
	if !handled || response.Error != nil || !strings.Contains(string(response.Result), `"immutable_snapshots":true`) || !strings.Contains(string(response.Result), captureSnapshots.host) {
		t.Fatalf("capability negotiation failed: %#v", response)
	}
}

func TestActiveCaptureRejectsResetRaces(t *testing.T) {
	startSnapshotHost(t)
	handleMessage(&Message{Action: "session_begin", SessionID: "owned-capture", CaptureMode: "navigate", TabID: 12})
	handleMessage(&Message{Action: "add_many", SessionID: "owned-capture", Requests: []Request{{ID: "owned-request", TabID: 12, CaptureSource: "cdp"}}})
	for _, action := range []string{"sync", "clear", "ambient_begin", "ambient_resume", "ambient_end", "session_begin"} {
		response, flush := handleMessage(&Message{Action: action, SessionID: "unrelated", Requests: []Request{{ID: "foreign"}}})
		if response["success"] != false || flush {
			t.Fatalf("active capture reset accepted: %s %#v", action, response)
		}
		if liveData.SessionID != "owned-capture" || len(liveData.Requests) != 1 || liveData.Requests[0].ID != "owned-request" || !browserCaptureActive {
			t.Fatalf("reset changed capture: %s %#v", action, liveData)
		}
	}
	response, _ := handleMessage(&Message{Action: "session_end", SessionID: "owned-capture"})
	if response["success"] != true {
		t.Fatalf("owner could not finish capture: %#v", response)
	}
	if err := sealCaptureSnapshot(); err != nil {
		t.Fatal(err)
	}
	response, flush := handleMessage(&Message{Action: "clear"})
	if response["success"] != true || !flush {
		t.Fatalf("reset stayed blocked after capture: %#v", response)
	}
	sealed, _ := handleCaptureRPC(RPCRequest{ID: "get", Method: "bridge.capture.get", Params: json.RawMessage(`{"session_id":"owned-capture"}`)})
	if sealed.Error != nil {
		t.Fatalf("post-capture reset removed sealed evidence: %#v", sealed)
	}
}
