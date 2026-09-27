package contextview

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/store"
)

func TestContextDistinguishesPartialWebSocketCoverageAndDetectsMessageChanges(t *testing.T) {
	request := store.Request{ID: "h_stream_context", RecordKind: "websocket", URL: "wss://fixture.test/socket?token=QUERY_PRIVATE", ResourceType: "websocket", Timestamp: 1,
		Response: &store.Response{Status: 101}, ResponseBodyCapture: &store.BodyCapture{State: "not_applicable"},
		Stream: &store.StreamCapture{Version: 1, Protocol: "websocket", State: "interrupted", ConnectionID: "PRIVATE_CONNECTION",
			Events:  []store.StreamEvent{{Sequence: 1, Kind: "message", Payload: "PAYLOAD_PRIVATE", PayloadEncoding: "utf-8", Bytes: 15}},
			Capture: store.StreamCoverage{State: "partial", Reason: "event_limit", CapturedEvents: 1, ObservedEvents: 3, DroppedEvents: 2, CapturedBytes: 15, ObservedBytes: 99}}}
	cache := t.TempDir()
	view, raw := buildView(t, testInput(request), Options{CacheDir: cache})
	group := view.Groups[0]
	if group.RecordKind != "websocket" || group.Method != "" || group.StreamDroppedEvents != 2 || group.StreamCapturedEvents != 1 || group.StreamObservedEvents != 3 || !reflect.DeepEqual(group.StreamCaptureStates, []Count{{Value: "partial", Count: 1}}) || !reflect.DeepEqual(group.Protocols, []Count{{Value: "websocket", Count: 1}}) {
		t.Fatalf("stream coverage hidden by HTTP body metadata: %+v", group)
	}
	if strings.Contains(string(raw), "PRIVATE") {
		t.Fatalf("context leaked stream payload or identity: %s", raw)
	}
	request.Stream.Events[0].Payload = "CHANGED_PRIVATE"
	next, _ := buildView(t, testInput(request), Options{CacheDir: cache, Since: view.Cursor})
	if next.NoChange || next.Totals.Updated != 1 {
		t.Fatal("message-only changes failed to advance context delta")
	}
}

func TestContextAPIStreamsExposeScopedCoverageWithoutPageMetadata(t *testing.T) {
	for _, protocol := range []string{"webtransport", "webrtc"} {
		t.Run(protocol, func(t *testing.T) {
			request := store.Request{ID: "h_" + protocol, RecordKind: protocol, URL: "https://fixture.test/session", ResourceType: protocol,
				Stream: &store.StreamCapture{Version: 1, Protocol: protocol, Source: "page_api", Clock: "performance_now_seconds", State: "closed", PayloadSemantics: "application_messages",
					Metadata: json.RawMessage(`{"parent_peer_id":"PARENT_PRIVATE","observer_trust":"page_controlled"}`),
					Events:   []store.StreamEvent{{Sequence: 1, Kind: "message", Payload: "PAYLOAD_PRIVATE", ChannelID: "CHANNEL_PRIVATE", Metadata: json.RawMessage(`{"label":"LABEL_PRIVATE"}`)}},
					Capture:  store.StreamCoverage{State: "complete", Scope: "instrumented_api_calls", Limitations: []string{"LIMITATION_PRIVATE"}, CapturedEvents: 1, ObservedEvents: 1}}}
			cache := t.TempDir()
			view, raw := buildView(t, testInput(request), Options{CacheDir: cache})
			group := view.Groups[0]
			if group.RecordKind != protocol || group.Method != "" || group.StreamWithLimitations != 1 || group.StreamMetadataOmitted != 1 || !reflect.DeepEqual(group.StreamSources, []Count{{Value: "page_api", Count: 1}}) || !reflect.DeepEqual(group.StreamScopes, []Count{{Value: "instrumented_api_calls", Count: 1}}) || !reflect.DeepEqual(group.StreamPayloadSemantics, []Count{{Value: "application_messages", Count: 1}}) || !reflect.DeepEqual(group.Protocols, []Count{{Value: protocol, Count: 1}}) {
				t.Fatalf("scoped stream coverage missing: %+v", group)
			}
			if strings.Contains(string(raw), "PRIVATE") {
				t.Fatalf("context exposed page-controlled details: %s", raw)
			}
			request.Stream.Events[0].Metadata = json.RawMessage(`{"changed":true}`)
			next, _ := buildView(t, testInput(request), Options{CacheDir: cache, Since: view.Cursor})
			if next.NoChange || next.Totals.Updated != 1 {
				t.Fatal("metadata-only update was not detected")
			}
			request.Stream.Source = "PRIVATE_SOURCE"
			request.Stream.Capture.Scope = "PRIVATE_SCOPE"
			request.Stream.PayloadSemantics = "PRIVATE_SEMANTICS"
			_, raw = buildView(t, testInput(request), Options{CacheDir: cache})
			if strings.Contains(string(raw), "PRIVATE") {
				t.Fatal("unknown collector labels escaped the bounded vocabulary")
			}
		})
	}
}

func TestContextLabelsMissingStreamCoverageUnknownAndHTTPProtocol(t *testing.T) {
	ws := store.Request{ID: "h_missing", RecordKind: "websocket", URL: ""}
	http := store.Request{ID: "h_http", Method: "GET", URL: "https://fixture.test", Response: &store.Response{Status: 200, Protocol: "h3"}}
	view, _ := buildView(t, testInput(ws, http), Options{CacheDir: t.TempDir()})
	for _, group := range view.Groups {
		if group.RecordKind == "websocket" {
			if !reflect.DeepEqual(group.StreamCaptureStates, []Count{{Value: "unknown", Count: 1}}) || !reflect.DeepEqual(group.Statuses, []Count{{Value: "not_observed", Count: 1}}) {
				t.Fatalf("missing observations appeared complete/pending HTTP: %+v", group)
			}
		} else if !reflect.DeepEqual(group.Protocols, []Count{{Value: "h3", Count: 1}}) {
			t.Fatalf("HTTP protocol missing: %+v", group)
		}
	}
}

func TestContextRetainsWebTransportLifecycleScope(t *testing.T) {
	request := store.Request{ID: "h_wt_lifecycle", RecordKind: "webtransport", URL: "https://fixture.test/transport",
		Stream: &store.StreamCapture{Version: 1, Protocol: "webtransport", Source: "cdp_lifecycle", PayloadSemantics: "unavailable", State: "closed",
			Capture: store.StreamCoverage{State: "partial", Reason: "payload_capture_unavailable", Scope: "connection_lifecycle", CapturedEvents: 3, ObservedEvents: 3}}}
	view, _ := buildView(t, testInput(request), Options{CacheDir: t.TempDir()})
	group := view.Groups[0]
	if !reflect.DeepEqual(group.StreamScopes, []Count{{Value: "connection_lifecycle", Count: 1}}) || !reflect.DeepEqual(group.StreamCaptureStates, []Count{{Value: "partial", Count: 1}}) || !reflect.DeepEqual(group.StreamPayloadSemantics, []Count{{Value: "unavailable", Count: 1}}) {
		t.Fatalf("lifecycle-only observation lost its scope or payload limit: %+v", group)
	}
}

func TestContextSeparatesMediaRecordingFromPacketOrMessageEvidence(t *testing.T) {
	request := store.Request{ID: "h_media", RecordKind: "webrtc_media", ResourceType: "webrtc_media", URL: "https://fixture.test/session",
		Stream: &store.StreamCapture{Version: 1, Protocol: "webrtc_media", Source: "browser_media_recorder", PayloadSemantics: "reencoded_media", State: "closed",
			Metadata: json.RawMessage(`{"track_id":"TRACK_PRIVATE","mime_type":"video/webm"}`),
			Events:   []store.StreamEvent{{Sequence: 1, Kind: "chunk", Payload: "PRIVATE", PayloadEncoding: "base64"}},
			Capture:  store.StreamCoverage{State: "complete", Scope: "recorded_media_interval", CapturedEvents: 1, ObservedEvents: 1}}}
	view, raw := buildView(t, testInput(request), Options{CacheDir: t.TempDir()})
	group := view.Groups[0]
	if group.RecordKind != "webrtc_media" || !reflect.DeepEqual(group.StreamSources, []Count{{Value: "browser_media_recorder", Count: 1}}) ||
		!reflect.DeepEqual(group.StreamScopes, []Count{{Value: "recorded_media_interval", Count: 1}}) ||
		!reflect.DeepEqual(group.StreamPayloadSemantics, []Count{{Value: "reencoded_media", Count: 1}}) ||
		!reflect.DeepEqual(group.Protocols, []Count{{Value: "webrtc_media", Count: 1}}) || strings.Contains(string(raw), "PRIVATE") {
		t.Fatalf("media scope or bounded disclosure lost: %s", raw)
	}
}
