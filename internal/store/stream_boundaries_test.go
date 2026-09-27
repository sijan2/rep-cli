package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func boundaryWebSocket() Request {
	masked := false
	return Request{ID: "h_ws_boundary", OriginalID: "child:ws", RecordKind: "websocket", URL: "wss://fixture.test/socket", Timestamp: 100,
		FrameID: "frame", SourceSessionID: "child", MonotonicTimestamp: 1.5, CompletionMonotonicTimestamp: 2.5,
		Response:            &Response{Status: 101, Protocol: "http/1.1", ConnectionReused: &masked, SecurityDetails: json.RawMessage(`{"protocol":"TLS 1.3"}`)},
		ResponseBodyCapture: &BodyCapture{State: "not_applicable", Reason: "websocket_messages"},
		Stream: &StreamCapture{Version: 1, Protocol: "websocket", ConnectionID: "capture:child:ws", State: "interrupted",
			Events:  []StreamEvent{{Sequence: 1, Kind: "message", Direction: "received", Timestamp: 2, Opcode: 2, Mask: &masked, Payload: "AP8=", PayloadEncoding: "base64", Bytes: 2}},
			Capture: StreamCoverage{State: "partial", Reason: "capture_deadline", CapturedEvents: 1, ObservedEvents: 1, CapturedBytes: 2, ObservedBytes: 2}}}
}

func boundaryAPIStream(protocol string) Request {
	semantics, eventKind := "application_chunks", "chunk"
	if protocol == "webrtc" {
		semantics, eventKind = "application_messages", "message"
	}
	return Request{ID: "h_" + protocol + "_boundary", RecordKind: protocol, URL: "https://fixture.test/session", ResourceType: protocol, Timestamp: 100,
		SourceSessionID: "frame-session", FrameID: "frame", CaptureSource: "cdp",
		Stream: &StreamCapture{Version: 1, Protocol: protocol, ConnectionID: "capture:" + protocol, State: "closed", Source: "page_api", Clock: "performance_now_seconds", PayloadSemantics: semantics,
			Metadata: json.RawMessage(`{"parent_peer_id":"peer-private","time_origin_ms":1700000000000,"observer_trust":"page_controlled"}`),
			Events: []StreamEvent{{Sequence: 1, Kind: eventKind, Direction: "received", ChannelID: "channel-private", Timestamp: 0.125,
				Metadata: json.RawMessage(`{"operation":"read","stream_type":"bidirectional"}`), Reason: "application_read", Payload: "AP8=", PayloadEncoding: "base64", Bytes: 2}},
			Capture: StreamCoverage{State: "complete", Scope: "instrumented_api_calls", Limitations: []string{"page_controlled_observer", "workers_not_observed"},
				CapturedEvents: 1, ObservedEvents: 1, CapturedBytes: 2, ObservedBytes: 2}}}
}

func TestStreamEvidenceSurvivesExportAndSavedSessionBoundaries(t *testing.T) {
	setupScopeStore(t)
	expected := 3
	export := Export{Version: "2.0", SessionID: "capture-boundary", CaptureDigest: strings.Repeat("a", 64), Requests: []Request{boundaryWebSocket(), boundaryAPIStream("webtransport"), boundaryAPIStream("webrtc")},
		BrowserSession: &BrowserSession{ExpectedRequests: &expected, ReceivedRequests: expected, CaptureWarnings: []string{"event_limit"}, CaptureStats: map[string]int64{"dropped_events": 3}, CaptureLimits: map[string]int64{"max_events": 1}}}
	var encoded bytes.Buffer
	if err := EncodeExport(&encoded, export); err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeExport(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, export) {
		t.Fatal("export round trip lost stream, protocol, clock, or coverage evidence")
	}
	session := Session{ID: "boundary", HashID: "s_boundary", Timestamp: 1, Requests: decoded.Requests, BrowserSession: decoded.BrowserSession, CaptureSessionID: decoded.SessionID, CaptureDigest: decoded.CaptureDigest}
	if err := AppendSessionLog(&session); err != nil {
		t.Fatal(err)
	}
	loaded, exists, err := LoadSessionsLog()
	if err != nil || !exists || len(loaded) != 1 {
		t.Fatalf("read saved session: %v %t %d", err, exists, len(loaded))
	}
	if !reflect.DeepEqual(loaded[0], session) {
		t.Fatal("saved JSONL round trip lost typed stream or capture provenance")
	}
	indexed, handled, err := LoadIndexedSession(session.HashID)
	if err != nil || !handled || indexed == nil || len(indexed.Requests) != len(session.Requests) {
		t.Fatalf("indexed stream retrieval failed: %v", err)
	}
	for i := range indexed.Requests {
		if !reflect.DeepEqual(indexed.Requests[i].Stream, session.Requests[i].Stream) {
			t.Fatalf("indexed archive lost %s evidence", session.Requests[i].RecordKind)
		}
	}
}

func TestAPIStreamMetadataAndIdentityRemainScoped(t *testing.T) {
	for _, protocol := range []string{"webtransport", "webrtc"} {
		t.Run(protocol, func(t *testing.T) {
			request := boundaryAPIStream(protocol)
			meta := request.GetMeta()
			if meta.RecordKind != protocol || meta.Protocol != protocol || meta.BodyType != "not_applicable" || meta.StreamSource != "page_api" || meta.StreamClock != "performance_now_seconds" || meta.StreamPayloadSemantics != request.Stream.PayloadSemantics || meta.StreamCapture.Scope != "instrumented_api_calls" || !meta.StreamMetadataOmitted || !meta.StreamEventsOmitted {
				t.Fatalf("stream metadata lost scope or omissions: %+v", meta)
			}
			encoded, err := json.Marshal(meta)
			if err != nil || bytes.Contains(encoded, []byte("peer-private")) || bytes.Contains(encoded, []byte("AP8=")) {
				t.Fatal("lightweight metadata exposed arbitrary metadata or bytes", err)
			}
			originalHash := RequestHash(&request)
			request.Stream.Metadata = json.RawMessage(`{"updated":true}`)
			request.Stream.Events[0].Metadata = json.RawMessage(`{"updated":true}`)
			if RequestHash(&request) != originalHash {
				t.Fatal("new evidence changed the connection identity")
			}
			request.Stream.Source = "cdp_lifecycle"
			if RequestHash(&request) == originalHash {
				t.Fatal("independent collectors collapsed into one connection")
			}
		})
	}
}

func TestStreamMetadataSeparatesMessagesFromHTTPBodies(t *testing.T) {
	request := boundaryWebSocket()
	meta := request.GetMeta()
	if meta.RecordKind != "websocket" || meta.Protocol != "websocket" || meta.StreamState != "interrupted" || meta.StreamCapture == nil || meta.StreamCapture.State != "partial" || meta.BodyType != "not_applicable" || meta.BodySize != 0 {
		t.Fatalf("ambiguous stream metadata: %+v", meta)
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("AP8=")) || bytes.Contains(raw, []byte("payload")) {
		t.Fatal("lightweight metadata exposed payload")
	}
	http := Request{Response: &Response{Protocol: "h3", Status: 200, Body: "ok"}}
	if http.GetMeta().Protocol != "h3" {
		t.Fatal("HTTP transport protocol omitted")
	}
}

func TestTypedStreamHashUsesConnectionIdentity(t *testing.T) {
	first := boundaryWebSocket()
	second := boundaryWebSocket()
	second.ID, second.Stream.ConnectionID = "h_second", "capture:other:ws"
	if RequestHash(&first) == RequestHash(&second) {
		t.Fatal("independent sockets sharing URL and timestamp collapsed")
	}
	second.Stream.ConnectionID = first.Stream.ConnectionID
	if RequestHash(&first) != RequestHash(&second) {
		t.Fatal("same connection identity changed with an incidental request alias")
	}
	second.RecordKind = "other"
	if RequestHash(&first) == RequestHash(&second) {
		t.Fatal("record kinds share a hash namespace")
	}
	legacy := Request{Method: "GET", URL: "https://fixture.test", Body: "", Timestamp: 123}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("GET|https://fixture.test||123")))
	if RequestHash(&legacy) != want {
		t.Fatal("legacy HTTP identity changed")
	}
}
