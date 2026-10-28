package cmd

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/store"
)

func TestCaptureCoverageEmptyAndLegacyRemainUnknown(t *testing.T) {
	empty := buildCaptureCoverage(store.Export{}, "no_capture")
	if empty.Capture.State != "no_capture" || !empty.Projection.Complete || empty.Observed.Records != 0 || !strings.Contains(empty.SiteCoverage, "unvisited") {
		t.Fatalf("empty capture was mistaken for complete site coverage: %+v", empty)
	}
	legacy := buildCaptureCoverage(store.Export{Requests: []store.Request{
		{Response: &store.Response{}},
		{Response: &store.Response{Body: "retained"}},
		{Response: &store.Response{}, ResponseBodyCapture: &store.BodyCapture{State: "complete"}},
		{Response: &store.Response{Body: "prefix"}, ResponseBodyTruncated: true},
	}}, "saved")
	if legacy.ResponseBodies.States["unknown"] != 2 || legacy.ResponseBodies.States["complete"] != 1 || legacy.ResponseBodies.States["partial"] != 1 || legacy.ResponseBodies.DeclaredEmpty != 1 || legacy.ResponseBodies.MetadataMissing != 3 {
		t.Fatalf("empty, legacy, and partial bodies conflated: %+v", legacy.ResponseBodies)
	}
	if legacy.ResponseBodies.StoredRepresentationBytes != 14 || legacy.ResponseBodies.DeclaredCapturedBytes != 0 || legacy.Capture.State != "gaps_reported" {
		t.Fatalf("legacy bytes or collection loss concealed: %+v", legacy)
	}
}

func TestCaptureCoverageKeepsPageControlledEvidenceAndLoss(t *testing.T) {
	expected := 1
	request := store.Request{RecordKind: "webtransport", URL: "https://PRIVATE.test/?token=PRIVATE", CaptureSource: "cdp",
		Stream: &store.StreamCapture{Version: 1, Protocol: "webtransport", Source: "page_api", State: "open",
			Metadata: json.RawMessage(`{"PRIVATE":"PRIVATE"}`),
			Events: []store.StreamEvent{{Sequence: 1, Kind: "gap", Reason: "PRIVATE"},
				{Sequence: 2, Kind: "chunk", Payload: "PRIVATE", Bytes: 2, Truncated: true}},
			Capture: store.StreamCoverage{State: "partial", Scope: "PRIVATE", Reason: "PRIVATE", Limitations: []string{"PRIVATE"},
				CapturedEvents: 2, ObservedEvents: 5, DroppedEvents: 3, CapturedBytes: 2, ObservedBytes: 8}}}
	export := store.Export{Requests: []store.Request{request}, SessionID: "PRIVATE", CaptureDigest: "PRIVATE",
		BrowserSession: &store.BrowserSession{ExpectedRequests: &expected, ReceivedRequests: 1, DroppedRequests: 2,
			FinishedAt: "PRIVATE", CaptureError: "PRIVATE", CaptureWarnings: []string{"request_limit", "PRIVATE"},
			CaptureStats:  map[string]int64{"dropped_requests": 4, "protocol_observer_dropped_events": 3, "PRIVATE": 100},
			CaptureLimits: map[string]int64{"max_requests": 1, "PRIVATE": 100}}}
	report := buildCaptureCoverage(export, "saved")
	if len(report.ProtocolEvidence) != 1 || report.ProtocolEvidence[0].Source != "page_api" || report.ProtocolEvidence[0].Trust != "page_controlled" || report.ProtocolEvidence[0].Visibility != "instrumented_api_calls_only" {
		t.Fatalf("page-controlled source was elevated to browser or wire proof: %+v", report.ProtocolEvidence)
	}
	if report.Streams.DroppedEvents != 3 || report.Streams.RetainedGapEvents != 1 || report.Streams.TruncatedEvents != 1 || report.Streams.Open != 1 || report.Streams.MetadataInvalid != 0 || report.Streams.WithLimitations != 1 {
		t.Fatalf("stream gaps hidden: %+v", report.Streams)
	}
	if report.Observed.HTTPRequests != 0 || len(report.ResponseBodies.States) != 0 || report.Capture.DroppedRecords != 4 || report.Capture.WarningsTotal != 2 || report.Capture.State != "gaps_reported" {
		t.Fatalf("records or capture counters conflated: %+v", report)
	}
	if report.Projection.Complete || report.Projection.MetadataDetailsOmitted != 3 || report.Capture.WarningsOmitted != 1 {
		t.Fatalf("projection omissions missing: %+v", report.Projection)
	}
	encoded, err := json.Marshal(report)
	if err != nil || strings.Contains(string(encoded), "PRIVATE") {
		t.Fatalf("metadata report disclosed raw collector fields: %s %v", encoded, err)
	}
}

func TestCaptureCoverageReportsOnlyObservedInventory(t *testing.T) {
	request := func(url, kind, page string) store.Request {
		return store.Request{URL: url, ResourceType: kind, PageURL: page, CaptureSource: "cdp", NetworkState: "complete",
			Response:            &store.Response{Status: 200, Body: "ok"},
			ResponseBodyCapture: &store.BodyCapture{State: "complete", CapturedBytes: 2},
			RequestBodyCapture:  &store.BodyCapture{State: "complete"}}
	}
	expected := 4
	export := store.Export{Requests: []store.Request{
		request("https://example.test/page#one", "document", ""),
		request("https://example.test/app.mjs?v=1", "script", "https://example.test/page#two"),
		request("https://example.test/app.mjs?v=1", "script", "https://example.test/page"),
		request("https://example.test/app.mjs.map", "fetch", "https://example.test/page"),
	}, BrowserSession: &store.BrowserSession{ExpectedRequests: &expected, ReceivedRequests: 4, FinishedAt: "done"}}
	report := buildCaptureCoverage(export, "live")
	if report.Observed.Pages != 1 || report.Observed.Scripts != 2 || report.Observed.UniqueScripts != 1 || report.Observed.HTTPRequests != 4 || report.Observed.Responses != 4 {
		t.Fatalf("incorrect observed inventory: %+v", report.Observed)
	}
	if report.Capture.State != "no_reported_gaps" || !report.Projection.Complete || report.ResponseBodies.DeclaredCapturedBytes != 8 || report.RequestBodies.DeclaredEmpty != 4 || report.ResponseBodies.PayloadValidation != "not_checked" {
		t.Fatalf("declared coverage lost: %+v", report)
	}
	if report.SiteCoverage == "complete" || report.WireEvidence == "complete" {
		t.Fatal("complete metadata was elevated to complete site or wire coverage")
	}
}

func TestCaptureCoverageDetectsInconsistentMetadata(t *testing.T) {
	expected, observed := 10, int64(1)
	report := buildCaptureCoverage(store.Export{Requests: []store.Request{
		{Response: &store.Response{Body: "three"}, ResponseBodyCapture: &store.BodyCapture{State: "complete", CapturedBytes: 8}},
		{Response: &store.Response{Body: "ok"}, ResponseBodyCapture: &store.BodyCapture{State: "complete", CapturedBytes: 2, ObservedBytes: &observed}},
		{RecordKind: "webrtc", Stream: &store.StreamCapture{Version: 1, Protocol: "webrtc", State: "closed",
			Capture: store.StreamCoverage{State: "complete", CapturedEvents: 1, ObservedEvents: 1}}},
	}, BrowserSession: &store.BrowserSession{ExpectedRequests: &expected, ReceivedRequests: 3, FinishedAt: "done",
		CaptureStats: map[string]int64{"dropped_events": -1}}}, "saved")
	if report.ResponseBodies.MetadataInvalid != 2 || report.Streams.MetadataInvalid != 1 || report.Capture.MetadataInvalid != 2 || report.Capture.State != "unknown" {
		t.Fatalf("inconsistent metadata accepted: %+v", report)
	}
	if report.ResponseBodies.States["complete"] != 0 || report.Streams.States["complete"] != 0 {
		t.Fatal("internally inconsistent records counted as complete")
	}
}

func TestCaptureCoverageScriptClassificationRejectsStreamAndUnknownKinds(t *testing.T) {
	for _, kind := range []string{"websocket", "webtransport", "webrtc", "webrtc_media", "unknown"} {
		request := store.Request{RecordKind: kind, URL: "https://example.test/socket.js", ResourceType: "script",
			Response: &store.Response{Headers: store.HeaderMap{"content-type": {"application/javascript"}}}}
		if captureIsScript(&request) {
			t.Errorf("%s record was classified as an HTTP script", kind)
		}
	}
	if captureIsScript(nil) {
		t.Fatal("absent request classified as a script")
	}
	for _, kind := range []string{"", "http"} {
		if !captureIsScript(&store.Request{RecordKind: kind, URL: "https://example.test/module.mjs?v=1"}) {
			t.Errorf("%q HTTP record was not classified as a script", kind)
		}
	}
}

func TestCaptureCoverageBodyMetadataCannotOverrideContradictoryEvidence(t *testing.T) {
	tests := []struct {
		name    string
		request store.Request
		state   string
		invalid int
		loss    int
	}{
		{"complete_truncated", store.Request{Response: &store.Response{Body: "ok"}, ResponseBodyTruncated: true,
			ResponseBodyCapture: &store.BodyCapture{State: "complete", CapturedBytes: 2}}, "unknown", 1, 1},
		{"complete_error", store.Request{Response: &store.Response{}, ResponseBodyError: "PRIVATE",
			ResponseBodyCapture: &store.BodyCapture{State: "complete"}}, "unknown", 1, 1},
		{"complete_missing_response", store.Request{ResponseBodyCapture: &store.BodyCapture{State: "complete"}}, "unknown", 1, 0},
		{"unrecognized_state", store.Request{Response: &store.Response{}, ResponseBodyCapture: &store.BodyCapture{State: "PRIVATE"}}, "unknown", 1, 0},
		{"missing_state", store.Request{Response: &store.Response{}, ResponseBodyCapture: &store.BodyCapture{}}, "unknown", 1, 0},
		{"explicit_unknown", store.Request{Response: &store.Response{}, ResponseBodyCapture: &store.BodyCapture{State: "unknown"}}, "unknown", 0, 0},
		{"not_applicable_with_bytes", store.Request{Response: &store.Response{Body: "ok"},
			ResponseBodyCapture: &store.BodyCapture{State: "not_applicable", CapturedBytes: 2}}, "unknown", 1, 0},
		{"unavailable_with_bytes", store.Request{Response: &store.Response{Body: "ok"},
			ResponseBodyCapture: &store.BodyCapture{State: "unavailable", CapturedBytes: 2}}, "unknown", 1, 0},
		{"not_applicable_error", store.Request{Response: &store.Response{}, ResponseBodyError: "PRIVATE",
			ResponseBodyCapture: &store.BodyCapture{State: "not_applicable"}}, "unknown", 1, 1},
		{"partial_with_loss", store.Request{Response: &store.Response{Body: "ok"}, ResponseBodyTruncated: true,
			ResponseBodyCapture: &store.BodyCapture{State: "partial", CapturedBytes: 2}}, "partial", 0, 1},
		{"legacy_error", store.Request{Response: &store.Response{}, ResponseBodyError: "PRIVATE"}, "unavailable", 0, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var counts captureBodyCounts
			captureAccumulateBody(&counts, &test.request, false)
			if counts.States[test.state] != 1 || counts.MetadataInvalid != test.invalid || counts.LegacyLossSignals != test.loss || counts.DeclaredEmpty != 0 || counts.PayloadValidation != "not_checked" {
				t.Fatalf("contradictory or unknown metadata was upgraded: %+v", counts)
			}
			report := buildCaptureCoverage(store.Export{Requests: []store.Request{test.request}}, "saved")
			if test.loss > 0 && report.Capture.State != "gaps_reported" {
				t.Fatalf("invalid metadata hid explicit loss: %+v", report.Capture)
			}
		})
	}
	// Incoming failure flags cannot invalidate an independently captured request.
	request := store.Request{Body: "ok", ResponseBodyTruncated: true, ResponseBodyError: "PRIVATE",
		RequestBodyCapture: &store.BodyCapture{State: "complete", CapturedBytes: 2}}
	var outgoing captureBodyCounts
	captureAccumulateBody(&outgoing, &request, true)
	if outgoing.States["complete"] != 1 || outgoing.MetadataInvalid != 0 || outgoing.LegacyLossSignals != 0 {
		t.Fatalf("response flags tainted request body metadata: %+v", outgoing)
	}
}

func TestCaptureCoveragePreservesArchiveSourceAndBoundsMetadata(t *testing.T) {
	expected := 1
	session := &store.Session{CaptureSessionID: "session", CaptureDigest: "digest", Requests: []store.Request{{URL: "https://example.test"}},
		BrowserSession: &store.BrowserSession{ExpectedRequests: &expected, ReceivedRequests: 1, FinishedAt: "done"}}
	export := captureCoverageExport(session)
	if export.BrowserSession != session.BrowserSession || export.SessionID != session.CaptureSessionID || export.CaptureDigest != session.CaptureDigest || len(export.Requests) != 1 {
		t.Fatal("archive conversion dropped collector provenance")
	}
	for i := 0; i < 10000; i++ {
		export.BrowserSession.CaptureWarnings = append(export.BrowserSession.CaptureWarnings, strings.Repeat("PRIVATE", 100))
	}
	report := buildCaptureCoverage(export, strings.Repeat("PRIVATE", 100))
	encoded, err := json.Marshal(report)
	if err != nil || len(encoded) > 8192 || strings.Contains(string(encoded), "PRIVATE") || report.Capture.WarningsTotal != 10000 || report.Capture.WarningsOmitted != 10000 || report.Source != "unknown" {
		t.Fatalf("unbounded or secret-bearing metadata report: bytes=%d error=%v", len(encoded), err)
	}
	var total int64 = math.MaxInt64 - 1
	captureAdd(&total, 5)
	if total != math.MaxInt64 {
		t.Fatal("collector counters overflowed")
	}
}

func TestCaptureCoverageAllCollectorLabelsRemainBounded(t *testing.T) {
	export := store.Export{BrowserSession: &store.BrowserSession{FinishedAt: "done",
		CaptureWarnings: []string{"total_body_limit", "body_limit", "request_limit", "event_limit", "related_targets_unavailable",
			"late_metadata_after_export", "request_metadata_limit", "download_metadata_limit", "late_download_after_export",
			"websocket_identity_conflict", "websocket_event_after_close", "browser_payload_decode_failed",
			"protocol_observer_setup_failed", "protocol_observer_unknown_context", "protocol_observer_context_unavailable",
			"protocol_observer_context_limit", "protocol_event_after_close", "execution_context_destroyed", "target_detached"},
		CaptureStats: map[string]int64{}, CaptureLimits: map[string]int64{},
	}}
	for _, key := range []string{"retained_body_bytes", "observed_events", "dropped_events", "dropped_requests", "exported_records",
		"protocol_payloads", "webrtc_media", "protocol_observer_reported_contexts", "protocol_observer_destroyed_contexts",
		"protocol_observer_dropped_events", "protocol_observer_observed_bytes", "protocol_observer_captured_bytes"} {
		export.BrowserSession.CaptureStats[key] = math.MaxInt64
	}
	for _, key := range []string{"max_body_bytes", "max_total_body_bytes", "max_requests", "max_events", "max_native_backlog_bytes"} {
		export.BrowserSession.CaptureLimits[key] = math.MaxInt64
	}
	for _, protocol := range []string{"websocket", "webtransport", "webrtc", "webrtc_media"} {
		for _, source := range []string{"cdp", "page_api", "cdp_lifecycle", "browser_media_recorder", "PRIVATE"} {
			export.Requests = append(export.Requests, store.Request{RecordKind: protocol,
				Stream: &store.StreamCapture{Version: 1, Protocol: protocol, Source: source, State: "open",
					Capture: store.StreamCoverage{State: "partial", CapturedBytes: math.MaxInt64, ObservedBytes: math.MaxInt64,
						ObservedEvents: math.MaxInt64, DroppedEvents: math.MaxInt64}}})
		}
	}
	for _, state := range []string{"complete", "partial", "pending", "unavailable", "not_applicable", "PRIVATE"} {
		export.Requests = append(export.Requests, store.Request{CaptureSource: "cdp", Response: &store.Response{},
			RequestBodyCapture: &store.BodyCapture{State: state}, ResponseBodyCapture: &store.BodyCapture{State: state}})
	}
	expected := len(export.Requests)
	export.BrowserSession.ExpectedRequests, export.BrowserSession.ReceivedRequests = &expected, expected
	report := buildCaptureCoverage(export, "saved")
	encoded, err := json.Marshal(report)
	if err != nil || len(encoded) > 8192 || strings.Contains(string(encoded), "PRIVATE") || !report.Streams.CountersSaturated || report.Projection.ProtocolGroupsOmitted == 0 {
		t.Fatalf("maximal metadata broke bounds or hid saturation: bytes=%d error=%v report=%+v", len(encoded), err, report)
	}
	if report.Projection.Complete || len(report.Capture.WarningCodes) > 12 || len(report.ProtocolEvidence) > 12 {
		t.Fatalf("bounded detail omissions concealed: %+v", report.Projection)
	}
}
