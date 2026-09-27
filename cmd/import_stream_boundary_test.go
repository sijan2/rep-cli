package cmd

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/repplus/rep-cli/internal/store"
)

func TestImportPreservesStreamCaptureProvenance(t *testing.T) {
	summaryTestScope(t, "boundary", "import")
	path := filepath.Join(t.TempDir(), "capture.json")
	export := store.Export{Version: "2.0", SessionID: "captured-session", CaptureDigest: "fixture-digest", BrowserSession: &store.BrowserSession{CaptureWarnings: []string{"event_limit"}, CaptureStats: map[string]int64{"dropped_events": 7}}, Requests: []store.Request{{ID: "h_import_stream", RecordKind: "websocket", URL: "ws://fixture.test/socket", Stream: &store.StreamCapture{Version: 1, Protocol: "websocket", State: "interrupted", ConnectionID: "capture:ws", Capture: store.StreamCoverage{State: "partial", Reason: "event_limit"}}}}}
	for _, protocol := range []string{"webtransport", "webrtc"} {
		export.Requests = append(export.Requests, *apiStreamFixture(protocol))
	}
	writeSummaryCapture(t, path, export)
	previous := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = previous })
	if err := importCmd.RunE(importCmd, []string{path}); err != nil {
		t.Fatal(err)
	}
	sessions, exists, err := store.LoadSessionsLog()
	if err != nil || !exists || len(sessions) != 1 {
		t.Fatalf("import archive: %v %t %d", err, exists, len(sessions))
	}
	saved := sessions[0]
	if saved.CaptureSessionID != export.SessionID || saved.CaptureDigest != export.CaptureDigest || saved.BrowserSession == nil || saved.BrowserSession.CaptureStats["dropped_events"] != 7 || saved.Requests[0].Stream == nil || saved.Requests[0].Stream.Capture.State != "partial" {
		t.Fatalf("import discarded evidence: %+v", saved)
	}
	if len(saved.Requests) != len(export.Requests) {
		t.Fatal("typed records were lost during import")
	}
	for index, request := range export.Requests {
		if saved.Requests[index].RecordKind != request.RecordKind || !reflect.DeepEqual(saved.Requests[index].Stream, request.Stream) {
			t.Fatalf("import lost %s payload/provenance fields", request.RecordKind)
		}
	}
}
