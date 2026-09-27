package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

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

func streamView(t *testing.T, r *store.Request, options streamOptions) (map[string]interface{}, string, error) {
	t.Helper()
	previousEnvelope, previousRaw := forceEnvelope, rawJSON
	forceEnvelope, rawJSON = false, true
	defer func() { forceEnvelope, rawJSON = previousEnvelope, previousRaw }()
	cmd := newStreamCommand()
	var buffer bytes.Buffer
	cmd.SetOut(&buffer)
	err := renderStream(cmd, r, options)
	var out map[string]interface{}
	if err == nil {
		if decodeErr := json.Unmarshal(buffer.Bytes(), &out); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if buffer.Len() > options.MaxBytes {
			t.Fatalf("output budget exceeded: %d > %d", buffer.Len(), options.MaxBytes)
		}
	}
	return out, buffer.String(), err
}

func TestStreamMetadataPagesAcknowledgeOnlyReturnedEvents(t *testing.T) {
	r := streamFixture(30, strings.Repeat("PRIVATE_PAYLOAD", 1000))
	options := streamOptions{After: -1, Event: -1, Events: 20, MaxBytes: 1024}
	seen := 0
	for seen < 30 {
		out, raw, err := streamView(t, r, options)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, "PRIVATE_PAYLOAD") {
			t.Fatal("payload leaked into metadata page")
		}
		events := out["events"].([]interface{})
		if len(events) == 0 {
			t.Fatal("cursor did not progress")
		}
		for _, item := range events {
			event := item.(map[string]interface{})
			seen++
			if event["sequence"] != float64(seen) || event["payload_omitted"] != true {
				t.Fatalf("event skipped or loss hidden: %v", event)
			}
		}
		options.After = int64(out["next_after"].(float64))
		if options.After != int64(seen) {
			t.Fatal("acknowledged an omitted event")
		}
	}
}

func TestStreamLifecycleIsNotAnEmptyPayload(t *testing.T) {
	for _, kind := range []string{"created", "open", "closed", "stats", "error", "gap"} {
		t.Run(kind, func(t *testing.T) {
			r := streamFixture(1, "")
			r.Stream.Events[0].Kind = kind
			r.Stream.Capture.State, r.Stream.Capture.Reason = "partial", "fixture"
			_, _, err := streamView(t, r, streamOptions{After: -1, Event: 1, Events: 20, MaxBytes: 4096})
			if err == nil || !strings.Contains(err.Error(), "no captured payload") {
				t.Fatalf("%s was interpreted as an empty payload: %v", kind, err)
			}
		})
	}
	r := streamFixture(1, "")
	out, _, err := streamView(t, r, streamOptions{After: -1, Event: 1, Events: 20, MaxBytes: 4096})
	if err != nil || out["payload_complete"] != true {
		t.Fatalf("real empty message was not retained: %v %v", out, err)
	}
}

func TestStreamPayloadBytePagesReconstructOriginal(t *testing.T) {
	payload := strings.Repeat("\"é\n💾", 400)
	r := streamFixture(1, payload)
	options := streamOptions{Event: 1, After: -1, Events: 20, MaxBytes: 1024, Head: 0}
	var reconstructed []byte
	for options.Offset < len(payload) {
		out, _, err := streamView(t, r, options)
		if err != nil {
			t.Fatal(err)
		}
		data := []byte(out["body"].(string))
		if out["encoding"] == "base64" {
			data, err = base64.StdEncoding.DecodeString(string(data))
			if err != nil {
				t.Fatal(err)
			}
		}
		next := int(out["next_offset"].(float64))
		if next <= options.Offset || next-options.Offset != len(data) {
			t.Fatal("payload offset does not describe delivered bytes")
		}
		reconstructed = append(reconstructed, data...)
		options.Offset = next
	}
	if string(reconstructed) != payload {
		t.Fatal("payload pages changed original bytes")
	}
}

func TestStreamBinaryArtifactAndPartialCoverage(t *testing.T) {
	summaryTestScope(t, "streams", "agent")
	r := streamFixture(1, "abc")
	raw := []byte{0, 255, 128}
	r.Stream.Events[0].Opcode = 2
	r.Stream.Events[0].Payload = base64.StdEncoding.EncodeToString(raw)
	r.Stream.Events[0].PayloadEncoding = "base64"
	options := streamOptions{Event: 1, After: -1, Events: 20, MaxBytes: 2048, Save: true}
	out, _, err := streamView(t, r, options)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out["path"].(string))
	if err != nil || !bytes.Equal(data, raw) {
		t.Fatalf("binary artifact corrupted: %v", err)
	}
	stat, _ := os.Stat(out["path"].(string))
	if stat.Mode().Perm() != 0600 {
		t.Fatal("artifact is not private")
	}
	r.Stream.Capture.State, r.Stream.Capture.Reason = "partial", "capture_ended_before_connection_closed"
	options.Save, options.RequireComplete = false, true
	if _, _, err := streamView(t, r, options); err == nil {
		t.Fatal("partial stream passed require-complete")
	}
	options.RequireComplete = false
	if _, _, err := streamView(t, r, options); err != nil {
		t.Fatal("partial bytes should remain available", err)
	}
}

func TestStreamRejectsCorruptCoveragePayloadAndOrdering(t *testing.T) {
	options := streamOptions{Event: 1, After: -1, Events: 20, MaxBytes: 2048}
	for _, mutate := range []func(*store.Request){
		func(r *store.Request) {
			r.Stream.Events[0].PayloadEncoding = "base64"
			r.Stream.Events[0].Payload = "not base64"
		},
		func(r *store.Request) { r.Stream.Events[0].Payload = "size mismatch" },
		func(r *store.Request) { r.Stream.Capture.CapturedEvents = 9 },
		func(r *store.Request) { r.Stream.Events[1].Sequence = 1 },
		func(r *store.Request) { r.Stream.Capture.DroppedEvents = 1 },
		func(r *store.Request) { r.Stream.Events[0].Truncated = true },
		func(r *store.Request) { r.Stream.Events[0].Sequence = 5; r.Stream.Events[1].Sequence = 6 },
		func(r *store.Request) { r.Stream.State = "interrupted" },
		func(r *store.Request) { r.Stream.Capture.Reason = "event_limit" },
		func(r *store.Request) { r.Stream.Capture.State = "partial"; r.Stream.Capture.ObservedEvents = -1 },
	} {
		r := streamFixture(2, "abc")
		mutate(r)
		if _, _, err := streamView(t, r, options); err == nil {
			t.Fatal("corrupt stream evidence accepted")
		}
	}
}

func TestStreamInfoAndEmptyPayloadRemainExplicit(t *testing.T) {
	r := streamFixture(1, "")
	options := streamOptions{Event: -1, After: -1, Events: 20, MaxBytes: 2048, Info: true}
	_, raw, err := streamView(t, r, options)
	if err != nil || strings.Contains(raw, "\"events\":") {
		t.Fatalf("info included events: %s %v", raw, err)
	}
	options.Event, options.Info = 1, false
	out, _, err := streamView(t, r, options)
	if err != nil || out["body"] != "" || out["view_complete"] != true {
		t.Fatalf("empty payload unknown: %v %v", out, err)
	}
	if err := renderCapturedBody(newStreamCommand(), r); err == nil || !strings.Contains(err.Error(), "rep stream") {
		t.Fatal("body did not direct message retrieval")
	}
}

func TestStreamRequireCompleteValidatesAllPayloadsWithoutReturningThem(t *testing.T) {
	r := streamFixture(2, "abc")
	options := streamOptions{Event: -1, After: -1, Events: 20, MaxBytes: 2048, Info: true, RequireComplete: true}
	out, _, err := streamView(t, r, options)
	if err != nil || out["payload_validation"] != "encoding_and_length_verified" {
		t.Fatalf("missing complete validation: %v %v", out, err)
	}
	r.Stream.Events[1].PayloadEncoding, r.Stream.Events[1].Payload = "base64", "?corrupt"
	if _, _, err := streamView(t, r, options); err == nil {
		t.Fatal("info require-complete accepted corrupt retained payload")
	}
	options.RequireComplete = false
	out, _, err = streamView(t, r, options)
	if err != nil || out["payload_validation"] != "not_checked" {
		t.Fatal("descriptor reads should disclose unvalidated payloads", err)
	}
}

func TestAPIStreamReadersPreserveScopeMetadataAndExactBytes(t *testing.T) {
	for _, protocol := range []string{"webtransport", "webrtc"} {
		t.Run(protocol, func(t *testing.T) {
			r := apiStreamFixture(protocol)
			options := streamOptions{Event: -1, After: -1, Events: 20, MaxBytes: 4096, RequireComplete: true}
			out, raw, err := streamView(t, r, options)
			if err != nil {
				t.Fatal("static limitations should allow complete declared scope", err)
			}
			if out["protocol"] != protocol || out["source"] != "page_api" || out["capture_source"] != "cdp" || out["clock"] != "performance_now_seconds" || !strings.Contains(out["clock_domain"].(string), "within one realm") || out["payload_semantics"] != r.Stream.PayloadSemantics {
				t.Fatalf("collector provenance lost: %+v", out)
			}
			if out["metadata"].(map[string]interface{})["parent_peer_id"] != "parent-peer" || out["capture"].(map[string]interface{})["scope"] != "instrumented_api_calls" {
				t.Fatal("scope/relationship metadata lost")
			}
			event := out["events"].([]interface{})[0].(map[string]interface{})
			if event["channel_id"] != "channel-1" || event["reason"] != "application_read" || event["metadata"].(map[string]interface{})["operation"] != "read" || event["payload_omitted"] != true || strings.Contains(raw, "hello✓") {
				t.Fatalf("event descriptor lost provenance or disclosed payload: %+v", event)
			}
			options.Event = 1
			out, _, err = streamView(t, r, options)
			if err != nil || out["body"] != "hello✓" || out["view_complete"] != true || out["payload_validation"] != "encoding_and_length_verified" {
				t.Fatalf("exact text message not retained: %+v %v", out, err)
			}
			bytes := []byte{0, 255, 128, 4}
			r.Stream.Events[0].Payload = base64.StdEncoding.EncodeToString(bytes)
			r.Stream.Events[0].PayloadEncoding = "base64"
			r.Stream.Events[0].Bytes = int64(len(bytes))
			r.Stream.Capture.CapturedBytes, r.Stream.Capture.ObservedBytes = int64(len(bytes)), int64(len(bytes))
			options.Offset, options.Head = 1, 2
			out, _, err = streamView(t, r, options)
			if err != nil || out["body"] != base64.StdEncoding.EncodeToString(bytes[1:3]) || out["next_offset"] != float64(3) || out["view_complete"] != false {
				t.Fatalf("exact binary chunk range not retained: %+v %v", out, err)
			}
			if err := renderCapturedBody(newStreamCommand(), r); err == nil || !strings.Contains(err.Error(), "rep stream") {
				t.Fatal("body did not redirect typed protocol evidence")
			}
		})
	}
}

func TestAPIStreamGapsAndLifecycleOnlyCaptureCannotClaimPayloadCompleteness(t *testing.T) {
	options := streamOptions{Event: -1, After: -1, Events: 20, MaxBytes: 4096, Info: true, RequireComplete: true}
	for _, protocol := range []string{"webtransport", "webrtc"} {
		r := apiStreamFixture(protocol)
		r.Stream.Events = append(r.Stream.Events, store.StreamEvent{Sequence: 2, Kind: "gap", Reason: "unsupported_payload"})
		r.Stream.Capture.CapturedEvents, r.Stream.Capture.ObservedEvents = 2, 2
		if _, _, err := streamView(t, r, options); err == nil {
			t.Fatal("complete metadata concealed an explicit observation gap")
		}
		r.Stream.Capture.State, r.Stream.Capture.Reason = "partial", "unsupported_payload"
		if _, _, err := streamView(t, r, options); err == nil {
			t.Fatal("partial API observation passed require-complete")
		}
		read := options
		read.RequireComplete, read.Info, read.Event = false, false, 1
		if _, _, err := streamView(t, r, read); err != nil {
			t.Fatal("earlier captured message became unreadable after a gap", err)
		}
		read.Event = 2
		if _, _, err := streamView(t, r, read); err == nil {
			t.Fatal("a gap was represented as an observed empty payload")
		}
	}
	r := apiStreamFixture("webtransport")
	r.Stream.Source, r.Stream.Clock, r.Stream.PayloadSemantics = "cdp_lifecycle", "cdp_monotonic_seconds", "unavailable"
	r.Stream.Events = []store.StreamEvent{{Sequence: 1, Kind: "created"}, {Sequence: 2, Kind: "closed"}}
	r.Stream.Capture = store.StreamCoverage{State: "partial", Reason: "payload_capture_unavailable", Scope: "lifecycle", CapturedEvents: 2, ObservedEvents: 2}
	options.RequireComplete = false
	out, _, err := streamView(t, r, options)
	if err != nil || out["payload_semantics"] != "unavailable" || out["source"] != "cdp_lifecycle" || !strings.Contains(out["clock_domain"].(string), "CDP") {
		t.Fatalf("lifecycle-only provenance was lost: %+v %v", out, err)
	}
	options.RequireComplete = true
	if _, _, err := streamView(t, r, options); err == nil {
		t.Fatal("lifecycle-only observation passed require-complete")
	}
	options.Info, options.RequireComplete, options.Event = false, false, 1
	if _, _, err := streamView(t, r, options); err == nil {
		t.Fatal("unavailable payload was returned as verified empty bytes")
	}
}
