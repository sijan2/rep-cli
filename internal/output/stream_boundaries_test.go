package output

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/store"
)

func TestFormattedStreamRetainsCoverageAndControlsPayloadDisclosure(t *testing.T) {
	request := store.Request{ID: "h_stream_output", RecordKind: "websocket", SourceSessionID: "child", FrameID: "frame", LoaderID: "loader", MonotonicTimestamp: 1, CompletionMonotonicTimestamp: 2,
		Stream: &store.StreamCapture{Version: 1, Protocol: "websocket", State: "interrupted", ConnectionID: "capture:ws", Capture: store.StreamCoverage{State: "partial", Reason: "event_limit", CapturedEvents: 1, ObservedEvents: 2, DroppedEvents: 1}, Events: []store.StreamEvent{{Sequence: 1, Kind: "message", Payload: "PAYLOAD_PRIVATE", PayloadEncoding: "utf-8", Bytes: 15}}}}
	for _, mode := range []store.OutputMode{store.OutputMeta, store.OutputCompact, store.OutputFull} {
		out := FormatRequest(&request, mode)
		if out.RecordKind != "websocket" || out.Stream == nil || out.Stream.Capture.State != "partial" || out.SourceSessionID != "child" || out.CompletionMonotonicTimestamp != 2 {
			t.Fatalf("lost stream/source metadata in %s: %+v", mode, out)
		}
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		if mode == store.OutputFull {
			if !strings.Contains(string(raw), "PAYLOAD_PRIVATE") || out.Stream.EventsOmitted {
				t.Fatal("full output silently lost messages")
			}
		} else if strings.Contains(string(raw), "PAYLOAD_PRIVATE") || !out.Stream.EventsOmitted {
			t.Fatal("bounded output leaked messages or hid omission")
		}
	}
}

func TestFormattedAPIStreamsPreserveFullMetadataAndDiscloseCompactOmission(t *testing.T) {
	for _, protocol := range []string{"webtransport", "webrtc"} {
		request := store.Request{ID: "h_" + protocol, RecordKind: protocol, Stream: &store.StreamCapture{
			Version: 1, Protocol: protocol, Source: "page_api", Clock: "performance_now_seconds", PayloadSemantics: "application_messages", State: "closed", ConnectionID: "connection",
			Metadata: json.RawMessage(`{"parent_peer_id":"METADATA_PRIVATE","observer_trust":"page_controlled"}`),
			Capture:  store.StreamCoverage{State: "complete", Scope: "instrumented_api_calls", Limitations: []string{"workers_not_observed"}, CapturedEvents: 1, ObservedEvents: 1},
			Events:   []store.StreamEvent{{Sequence: 1, Kind: "message", ChannelID: "channel", Metadata: json.RawMessage(`{"label":"EVENT_PRIVATE"}`), Reason: "fixture", Payload: "PAYLOAD_PRIVATE", PayloadEncoding: "utf-8", Bytes: 15}},
		}}
		for _, mode := range []store.OutputMode{store.OutputMeta, store.OutputCompact, store.OutputFull} {
			out := FormatRequest(&request, mode)
			if out.Stream.Source != "page_api" || out.Stream.Clock != "performance_now_seconds" || out.Stream.PayloadSemantics != "application_messages" || !reflect.DeepEqual(out.Stream.Capture, request.Stream.Capture) {
				t.Fatalf("%s %s lost source or scope: %+v", protocol, mode, out.Stream)
			}
			raw, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			if mode == store.OutputFull {
				if out.Stream.MetadataOmitted || out.Stream.EventsOmitted || !reflect.DeepEqual(out.Stream.Metadata, request.Stream.Metadata) || !reflect.DeepEqual(out.Stream.Events, request.Stream.Events) {
					t.Fatal("full output lost original metadata or events")
				}
			} else if !out.Stream.MetadataOmitted || !out.Stream.EventsOmitted || strings.Contains(string(raw), "PRIVATE") {
				t.Fatal("compact output leaked omitted evidence or hid omission")
			}
		}
	}
}

func TestFormattedResponsePreservesConnectionAndSecurityMetadata(t *testing.T) {
	no := false
	request := store.Request{Response: &store.Response{Status: 200, Body: "BODY_PRIVATE", Protocol: "h3", RemoteIPAddress: "127.0.0.1", RemotePort: 443, ConnectionID: 4, ConnectionReused: &no, FromDiskCache: &no, Timing: map[string]float64{"requestTime": 1}, SecurityDetails: json.RawMessage(`{"protocol":"TLS 1.3"}`), MonotonicTimestamp: 1.5}}
	out := FormatRequest(&request, store.OutputMeta)
	if out.Response.Protocol != "h3" || out.Response.RemotePort != 443 || out.Response.ConnectionReused == nil || *out.Response.ConnectionReused || out.Response.MonotonicTimestamp != 1.5 || out.Response.Body != "" || len(out.Response.SecurityDetails) == 0 {
		t.Fatalf("response metadata lost: %+v", out.Response)
	}
}
