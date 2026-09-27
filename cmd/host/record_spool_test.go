package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/store"
)

func startRecordSpoolHost(tb testing.TB) {
	tb.Helper()
	previousLimits, previousPath := activeCaptureLimits, dataPath
	tb.Setenv("REP_BRIDGE_DIR", tb.TempDir())
	if err := initializeCaptureSnapshots(); err != nil {
		tb.Fatal(err)
	}
	resetHostState()
	dataPath = filepath.Join(tb.TempDir(), "live.json")
	tb.Cleanup(func() {
		liveMu.Lock()
		cleanupRequestTransfersLocked()
		liveData.spool.close()
		liveData.spool = nil
		liveMu.Unlock()
		closeCaptureSnapshots()
		activeCaptureLimits, dataPath = previousLimits, previousPath
	})
}

func recordSequence(value uint64) *uint64 { return &value }

func acceptRecordMessage(tb testing.TB, message *Message) map[string]interface{} {
	tb.Helper()
	response, _ := handleMessage(message)
	if response["success"] != true {
		tb.Fatalf("%s failed: %#v", message.Action, response)
	}
	if message.CaptureSequence != nil {
		ack := captureAcknowledgement(message, response)
		if ack["action"] != "capture_ack" || ack["session_id"] != message.SessionID || ack["capture_sequence"] != *message.CaptureSequence || ack["success"] != true {
			tb.Fatalf("invalid acknowledgement: %#v", ack)
		}
	}
	return response
}

func beginRecordSpool(tb testing.TB, id string) {
	tb.Helper()
	acceptRecordMessage(tb, &Message{Action: "session_begin", SessionID: id, CaptureMode: "navigate", TabID: 17,
		Incremental: true, CaptureSequence: recordSequence(0), CaptureLimits: map[string]int64{"max_total_body_bytes": 65536}})
	if liveData.spool == nil || !browserCaptureActive {
		tb.Fatal("incremental session did not own an active spool")
	}
}

func readRecordSnapshot(tb testing.TB, id string) (captureSnapshotDescriptor, []byte, LiveData) {
	tb.Helper()
	params, _ := json.Marshal(map[string]string{"session_id": id})
	rpc, _ := handleCaptureRPC(RPCRequest{Method: "bridge.capture.get", Params: params})
	if rpc.Error != nil {
		tb.Fatal(rpc.Error)
	}
	var descriptor captureSnapshotDescriptor
	if err := json.Unmarshal(rpc.Result, &descriptor); err != nil {
		tb.Fatal(err)
	}
	raw, err := os.ReadFile(descriptor.Path)
	if err != nil {
		tb.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if !descriptor.Sealed || descriptor.SHA256 != hex.EncodeToString(digest[:]) || descriptor.Bytes != int64(len(raw)) {
		tb.Fatal("snapshot identity does not match its bytes")
	}
	var snapshot LiveData
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		tb.Fatal(err)
	}
	return descriptor, raw, snapshot
}

func TestIncrementalSpoolSealsExactHTTPAndWebSocketEvidence(t *testing.T) {
	startRecordSpoolHost(t)
	beginRecordSpool(t, "ordered")
	var requests []Request
	if err := json.Unmarshal([]byte(`[
		{"id":"http","method":"POST","url":"https://fixture.test/data","body":"request 雪","headers":{"content-type":"text/plain"},"capture_source":"cdp","timestamp":1000,"frame_id":"frame","loader_id":"loader","source_session_id":"child","monotonic_timestamp":10.25,"response_monotonic_timestamp":10.5,"completion_monotonic_timestamp":10.75,"initiator_details":{"type":"script"},"request_body_capture":{"state":"complete","source":"cdp","captured_bytes":11},"response":{"status":200,"body":"café\n雪","headers":{"set-cookie":["one=1","two=2"]},"protocol":"h2","remote_ip_address":"127.0.0.1","remote_port":443,"connection_id":7,"connection_reused":false,"from_disk_cache":false,"from_service_worker":true,"timing":{"requestTime":10.25,"receiveHeadersEnd":1.5},"security_details":{"protocol":"TLS 1.3"},"security_state":"secure","encoded_data_length":17,"monotonic_timestamp":10.5},"response_body_capture":{"state":"complete","source":"cdp-stream","captured_bytes":9}},
		{"id":"ws","record_kind":"websocket","method":"GET","url":"wss://fixture.test/socket","capture_source":"cdp","timestamp":1001,"stream":{"version":1,"protocol":"websocket","connection_id":"socket-1","state":"closed","events":[{"sequence":1,"kind":"message","direction":"sent","timestamp":11.25,"opcode":1,"payload":"hello 雪","payload_encoding":"utf-8","bytes":9},{"sequence":2,"kind":"message","direction":"received","timestamp":11.5,"opcode":2,"payload":"AAH/","payload_encoding":"base64","bytes":3},{"sequence":3,"kind":"closed","timestamp":12,"bytes":0}],"capture":{"state":"complete","captured_events":3,"observed_events":3,"captured_bytes":12,"observed_bytes":12,"dropped_events":0}}}
	]`), &requests); err != nil {
		t.Fatal(err)
	}
	acceptRecordMessage(t, &Message{Action: "add_many", SessionID: "ordered", CaptureSequence: recordSequence(1), Requests: requests})
	for _, metadata := range liveData.Requests {
		if metadata.Body != "" || metadata.Headers != nil || metadata.InitiatorDetails != nil || (metadata.Response != nil && (metadata.Response.Body != "" || metadata.Response.Headers != nil || metadata.Response.Timing != nil || metadata.Response.SecurityDetails != nil)) || (metadata.Stream != nil && metadata.Stream.Events != nil) {
			t.Fatalf("payload retained in metadata index: %s", metadata.ID)
		}
	}
	if err := saveLiveData(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dataPath); !os.IsNotExist(err) {
		t.Fatalf("active incremental capture published a live view: %v", err)
	}
	expected := len(requests)
	acceptRecordMessage(t, &Message{Action: "session_end", SessionID: "ordered", CaptureSequence: recordSequence(2), ExpectedRequests: &expected,
		CaptureStats: map[string]int64{"exported_records": 2}, CaptureWarnings: []string{"related_targets_unavailable"}})
	if err := sealCaptureSnapshot(); err != nil {
		t.Fatal(err)
	}
	descriptor, _, saved := readRecordSnapshot(t, "ordered")
	if descriptor.Requests != 2 || !reflect.DeepEqual(saved.Requests, requests) {
		t.Fatalf("sealed evidence changed:\nwant %#v\ngot  %#v", requests, saved.Requests)
	}
	if saved.Browser.ExpectedRequests == nil || *saved.Browser.ExpectedRequests != 2 || saved.Browser.ReceivedRequests != 2 || saved.Browser.CaptureStats["exported_records"] != 2 || saved.Browser.CaptureLimits["max_total_body_bytes"] != 65536 || len(saved.Browser.CaptureWarnings) != 1 {
		t.Fatalf("capture accounting was lost: %#v", saved.Browser)
	}
}

func TestIncrementalSpoolAcceptsSequencedRequestFragments(t *testing.T) {
	startRecordSpoolHost(t)
	beginRecordSpool(t, "fragmented")
	want := Request{ID: "fragment", Method: "GET", URL: "https://fixture.test/data", CaptureSource: "cdp", Response: &Response{Status: 200, Body: strings.Repeat("雪", requestChunkBytes)}}
	messages := transferMessages(t, "fragmented", want)
	for index, message := range messages {
		message.CaptureSequence = recordSequence(uint64(index + 1))
		acceptRecordMessage(t, message)
		if index < len(messages)-1 && len(liveData.Requests) != 0 {
			t.Fatal("partial record became visible")
		}
	}
	if len(requestTransfers) != 0 || len(liveData.Requests) != 1 || liveData.Requests[0].Response.Body != "" {
		t.Fatal("fragment transfer did not leave only spooled evidence and metadata")
	}
	expected := 1
	acceptRecordMessage(t, &Message{Action: "session_end", SessionID: "fragmented", CaptureSequence: recordSequence(uint64(len(messages) + 1)), ExpectedRequests: &expected})
	if err := sealCaptureSnapshot(); err != nil {
		t.Fatal(err)
	}
	_, _, saved := readRecordSnapshot(t, "fragmented")
	if len(saved.Requests) != 1 || !reflect.DeepEqual(saved.Requests[0], want) {
		t.Fatal("fragmented record changed during spooling")
	}
}

func TestIncrementalSpoolRejectsMissingReorderedAndRepeatedSequences(t *testing.T) {
	for _, kind := range []string{"missing", "gap", "repeated"} {
		t.Run(kind, func(t *testing.T) {
			startRecordSpoolHost(t)
			beginRecordSpool(t, "sequence")
			request := Request{ID: "r", Method: "GET", URL: "https://fixture.test/", CaptureSource: "cdp"}
			message := &Message{Action: "add", SessionID: "sequence", Request: &request}
			switch kind {
			case "gap":
				message.CaptureSequence = recordSequence(2)
			case "repeated":
				message.CaptureSequence = recordSequence(1)
				acceptRecordMessage(t, message)
			}
			response, _ := handleMessage(message)
			if response["success"] != false || liveData.Browser.CaptureError == "" {
				t.Fatalf("invalid sequence was not retained as a failure: %#v", response)
			}
			expected := len(liveData.Requests)
			response, _ = handleMessage(&Message{Action: "session_end", SessionID: "sequence", CaptureSequence: recordSequence(liveData.nextCaptureSequence), ExpectedRequests: &expected})
			if response["success"] != false {
				t.Fatal("a later end erased the sequence failure")
			}
			if err := sealCaptureSnapshot(); err == nil {
				t.Fatal("capture with a sequence gap was sealed")
			}
		})
	}
}

func TestIncrementalSpoolAbortOwnershipAndRecovery(t *testing.T) {
	for _, state := range []string{"active", "sequence-failed", "ended", "rejected-end"} {
		t.Run(state, func(t *testing.T) {
			startRecordSpoolHost(t)
			beginRecordSpool(t, "owner")
			foreign, _ := handleMessage(&Message{Action: "session_abort", SessionID: "foreign", CaptureSequence: recordSequence(99)})
			if foreign["success"] != false || !browserCaptureActive || liveData.Browser.CaptureError != "" {
				t.Fatal("foreign abort changed the owner's capture")
			}
			var sealed captureSnapshotDescriptor
			switch state {
			case "sequence-failed":
				handleMessage(&Message{Action: "add", SessionID: "owner", CaptureSequence: recordSequence(8), Request: &Request{ID: "r"}})
			case "ended", "rejected-end":
				expected := 0
				if state == "rejected-end" {
					expected = 1
				}
				response, _ := handleMessage(&Message{Action: "session_end", SessionID: "owner", CaptureSequence: recordSequence(1), ExpectedRequests: &expected})
				if (response["success"] == true) != (state == "ended") {
					t.Fatalf("unexpected end response: %#v", response)
				}
				if state == "ended" {
					if err := sealCaptureSnapshot(); err != nil {
						t.Fatal(err)
					}
					sealed, _, _ = readRecordSnapshot(t, "owner")
				}
			}
			response := acceptRecordMessage(t, &Message{Action: "session_abort", SessionID: "owner", CaptureSequence: recordSequence(100), CaptureError: "caller stopped"})
			if browserCaptureActive || !browserCaptureSealed || len(requestTransfers) != 0 {
				t.Fatal("owned abort left active transport state")
			}
			if state == "ended" {
				after, _, _ := readRecordSnapshot(t, "owner")
				if response["already_ended"] != true || liveData.Browser.CaptureError != "" || after.SHA256 != sealed.SHA256 {
					t.Fatal("late abort changed already sealed evidence")
				}
			} else if liveData.Browser.CaptureError == "" {
				t.Fatal("abort erased incomplete capture evidence")
			}
			beginRecordSpool(t, "next-owner")
			if liveData.Browser.CaptureError != "" || len(liveData.Requests) != 0 {
				t.Fatal("failed prior owner contaminated the next capture")
			}
		})
	}
}

func TestIncrementalSpoolShortReadCannotPublish(t *testing.T) {
	startRecordSpoolHost(t)
	prior := sealTestCapture(t, "prior", "retained")
	priorBytes, err := os.ReadFile(prior.Path)
	if err != nil {
		t.Fatal(err)
	}
	beginRecordSpool(t, "short-spool")
	acceptRecordMessage(t, &Message{Action: "add", SessionID: "short-spool", CaptureSequence: recordSequence(1), Request: &Request{ID: "r", Response: &Response{Body: "must survive intact"}}})
	expected := 1
	acceptRecordMessage(t, &Message{Action: "session_end", SessionID: "short-spool", CaptureSequence: recordSequence(2), ExpectedRequests: &expected})
	if err := liveData.spool.writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := liveData.spool.file.Truncate(liveData.spool.bytes - 3); err != nil {
		t.Fatal(err)
	}
	if err := sealCaptureSnapshot(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short spool did not fail closed: %v", err)
	}
	if _, exists := captureSnapshots.entries["short-spool"]; exists {
		t.Fatal("short spool published a snapshot descriptor")
	}
	name := sha256.Sum256([]byte("short-spool"))
	if _, err := os.Stat(filepath.Join(captureSnapshots.dir, hex.EncodeToString(name[:])+".json")); !os.IsNotExist(err) {
		t.Fatalf("short spool published snapshot bytes: %v", err)
	}
	partials, _ := filepath.Glob(filepath.Join(captureSnapshots.dir, ".rep-capture-*.tmp"))
	if len(partials) != 0 {
		t.Fatal("failed snapshot left temporary output")
	}
	after, err := os.ReadFile(prior.Path)
	if err != nil || string(after) != string(priorBytes) {
		t.Fatal("failed snapshot changed prior evidence")
	}
}

func TestIncrementalSpoolFlushFailureCannotPublish(t *testing.T) {
	startRecordSpoolHost(t)
	beginRecordSpool(t, "flush-failure")
	acceptRecordMessage(t, &Message{Action: "add", SessionID: "flush-failure", CaptureSequence: recordSequence(1), Request: &Request{ID: "r", Response: &Response{Body: "pending buffered bytes"}}})
	if liveData.spool.writer.Buffered() == 0 {
		t.Fatal("fixture requires buffered bytes before sealing")
	}
	expected := 1
	acceptRecordMessage(t, &Message{Action: "session_end", SessionID: "flush-failure", CaptureSequence: recordSequence(2), ExpectedRequests: &expected})
	if err := liveData.spool.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sealCaptureSnapshot(); err == nil {
		t.Fatal("buffer flush failure published a sealed snapshot")
	}
	if _, exists := captureSnapshots.entries["flush-failure"]; exists {
		t.Fatal("buffer flush failure published a snapshot descriptor")
	}
	files, _ := filepath.Glob(filepath.Join(captureSnapshots.dir, "*.json"))
	if len(files) != 0 {
		t.Fatal("buffer flush failure left published snapshot bytes")
	}
}

func TestIncrementalSpoolMergesDuplicateWithoutLosingPayload(t *testing.T) {
	startRecordSpoolHost(t)
	beginRecordSpool(t, "revision")
	request := Request{ID: "r", Method: "POST", URL: "https://fixture.test/", CaptureSource: "cdp", Body: "request", Headers: store.HeaderMap{"x-request": {"one"}},
		CompletionMonotonicTimestamp: 4.5, Response: &Response{Status: 200, Body: "original response", Protocol: "h2", Timing: map[string]float64{"receiveHeadersEnd": 5}},
		ResponseBodyCapture: &store.BodyCapture{State: "complete", CapturedBytes: 17}}
	acceptRecordMessage(t, &Message{Action: "add", SessionID: "revision", CaptureSequence: recordSequence(1), Request: &request})
	firstBytes := liveData.spool.bytes
	update := Request{ID: "r", Method: request.Method, URL: request.URL, CaptureSource: "cdp", Response: &Response{Status: 201}}
	acceptRecordMessage(t, &Message{Action: "add", SessionID: "revision", CaptureSequence: recordSequence(2), Request: &update})
	if len(liveData.Requests) != 1 || liveData.spool.bytes <= firstBytes || len(liveData.spool.entries) != 1 {
		t.Fatal("revision duplicated the request or omitted disk accounting")
	}
	expected := 1
	acceptRecordMessage(t, &Message{Action: "session_end", SessionID: "revision", CaptureSequence: recordSequence(3), ExpectedRequests: &expected})
	if err := sealCaptureSnapshot(); err != nil {
		t.Fatal(err)
	}
	_, _, saved := readRecordSnapshot(t, "revision")
	got := saved.Requests[0]
	if got.Response.Status != 201 || got.Response.Body != request.Response.Body || got.Body != request.Body || got.Response.Protocol != "h2" || got.CompletionMonotonicTimestamp != 4.5 || !reflect.DeepEqual(got.Headers, request.Headers) || !reflect.DeepEqual(got.Response.Timing, request.Response.Timing) || !reflect.DeepEqual(got.ResponseBodyCapture, request.ResponseBodyCapture) {
		t.Fatalf("revision lost known evidence: %#v", got)
	}
}

func TestIncrementalSpoolBudgetsFailBeforePublishingRecord(t *testing.T) {
	for _, kind := range []string{"request-count", "request-bytes", "revision-bytes", "buffered-bytes"} {
		t.Run(kind, func(t *testing.T) {
			startRecordSpoolHost(t)
			beginRecordSpool(t, "limited")
			request := Request{ID: "one", Method: "GET", URL: "https://fixture.test/", CaptureSource: "cdp", Response: &Response{Status: 200, Body: strings.Repeat("x", 300)}}
			encoded, _ := json.Marshal(request)
			sequence := uint64(1)
			wantCount := 0
			switch kind {
			case "request-count":
				activeCaptureLimits.requests = 1
				acceptRecordMessage(t, &Message{Action: "add", SessionID: "limited", CaptureSequence: recordSequence(sequence), Request: &request})
				request.ID = "two"
				sequence++
				wantCount = 1
			case "request-bytes":
				activeCaptureLimits.requestBytes = int64(len(encoded) - 1)
			case "revision-bytes", "buffered-bytes":
				activeCaptureLimits.snapshotBytes = int64(len(encoded)*2 - 1)
				acceptRecordMessage(t, &Message{Action: "add", SessionID: "limited", CaptureSequence: recordSequence(sequence), Request: &request})
				if kind == "buffered-bytes" {
					if liveData.spool.writer.Buffered() == 0 {
						t.Fatal("fixture requires first record to remain in the buffer")
					}
					request.ID = "two"
				}
				sequence++
				wantCount = 1
			}
			before := liveData.spool.bytes
			response, _ := handleMessage(&Message{Action: "add", SessionID: "limited", CaptureSequence: recordSequence(sequence), Request: &request})
			if response["success"] != false || liveData.Browser.CaptureError == "" || len(liveData.Requests) != wantCount || liveData.spool.bytes != before {
				t.Fatalf("budget failure published data or lost its reason: %#v", response)
			}
			handleMessage(&Message{Action: "session_end", SessionID: "limited", CaptureSequence: recordSequence(sequence + 1), ExpectedRequests: &wantCount})
			if err := sealCaptureSnapshot(); err == nil {
				t.Fatal("budget failure sealed as complete")
			}
		})
	}
}

// These measure host record ingestion only. They exclude transport, body
// collection, sealing, fsync, archive publication, and restart rehydration.
func BenchmarkCaptureRecordIngestion(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		for _, incremental := range []bool{false, true} {
			mode := "legacy"
			if incremental {
				mode = "incremental"
			}
			b.Run(fmt.Sprintf("%s/%d", mode, count), func(b *testing.B) {
				startRecordSpoolHost(b)
				requests := make([]Request, count)
				for i := range requests {
					requests[i] = Request{ID: fmt.Sprintf("record-%d", i), Method: "GET", URL: "https://fixture.test/data", CaptureSource: "cdp", Response: &Response{Status: 200, Body: strings.Repeat("x", 128)}}
				}
				b.ReportAllocs()
				b.ReportMetric(float64(count), "records/op")
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					liveData.spool.close()
					resetHostState()
					begin := &Message{Action: "session_begin", SessionID: "benchmark", Incremental: incremental}
					if incremental {
						begin.CaptureSequence = recordSequence(0)
					}
					acceptRecordMessage(b, begin)
					b.StartTimer()
					for index := range requests {
						message := &Message{Action: "add", SessionID: "benchmark", Request: &requests[index]}
						if incremental {
							message.CaptureSequence = recordSequence(uint64(index + 1))
						}
						response, _ := handleMessage(message)
						if response["success"] != true {
							b.Fatalf("ingestion failed: %#v", response)
						}
					}
				}
			})
		}
	}
}
