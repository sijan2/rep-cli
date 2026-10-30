package commands

import (
	"encoding/json"

	"github.com/repplus/rep-cli/internal/store"
)

func streamFixture(count int, payload string) *store.Request {
	r := &store.Request{ID: "websocket-fixture", RecordKind: "websocket", URL: "ws://127.0.0.1/fixture", CaptureSource: "cdp",
		Stream: &store.StreamCapture{Version: 1, Protocol: "websocket", ConnectionID: "capture:1", State: "closed"}}
	for i := 1; i <= count; i++ {
		r.Stream.Events = append(r.Stream.Events, store.StreamEvent{Sequence: int64(i), Kind: "message", Direction: "received", Opcode: 1, Payload: payload, PayloadEncoding: "utf-8", Bytes: int64(len(payload))})
	}
	r.Stream.Capture = store.StreamCoverage{State: "complete", CapturedEvents: int64(count), ObservedEvents: int64(count), CapturedBytes: int64(count * len(payload)), ObservedBytes: int64(count * len(payload))}
	return r
}

func apiStreamFixture(protocol string) *store.Request {
	r := streamFixture(1, "hello✓")
	r.ID, r.RecordKind, r.ResourceType, r.URL = protocol+"-fixture", protocol, protocol, "https://fixture.test/session"
	r.Stream.Protocol, r.Stream.Source, r.Stream.Clock = protocol, "page_api", "performance_now_seconds"
	r.Stream.PayloadSemantics = "application_messages"
	r.Stream.Metadata = json.RawMessage(`{"parent_peer_id":"parent-peer","observer_trust":"page_controlled","time_origin_ms":1700000000000}`)
	r.Stream.Capture.Scope = "instrumented_api_calls"
	r.Stream.Capture.Limitations = []string{"page_controlled_observer", "workers_not_observed"}
	r.Stream.Events[0].ChannelID = "channel-1"
	r.Stream.Events[0].Reason = "application_read"
	r.Stream.Events[0].Metadata = json.RawMessage(`{"stream_type":"bidirectional","operation":"read"}`)
	r.Stream.Events[0].Opcode = 0
	if protocol == "webtransport" {
		r.Stream.PayloadSemantics = "application_chunks"
		r.Stream.Events[0].Kind = "chunk"
	}
	return r
}
