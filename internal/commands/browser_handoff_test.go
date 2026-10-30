package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"os"
	"path/filepath"

	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
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
