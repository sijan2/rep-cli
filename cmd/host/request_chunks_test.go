package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/store"
)

func transferMessages(t *testing.T, session string, request Request) []*Message {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	var messages []*Message
	for offset := 0; offset < len(raw); offset += requestChunkBytes {
		end := min(len(raw), offset+requestChunkBytes)
		messages = append(messages, &Message{Action: "request_chunk", SessionID: session, TransferID: "transfer-1", Sequence: len(messages), TotalBytes: int64(len(raw)), SHA256: hash, Data: base64.StdEncoding.EncodeToString(raw[offset:end])})
	}
	return append(messages, &Message{Action: "request_end", SessionID: session, TransferID: "transfer-1", Chunks: len(messages), TotalBytes: int64(len(raw)), SHA256: hash})
}

func TestChunkedRequestPreservesLargeUnicodeBodyAndProvenance(t *testing.T) {
	startSnapshotHost(t)
	handleMessage(&Message{Action: "session_begin", SessionID: "large-body", CaptureMode: "navigate"})
	body := strings.Repeat("chunked café 雪 😀\n", 80000)
	request := Request{ID: "large", Method: "POST", URL: "https://fixture.test/data", Body: strings.Repeat("request payload", 20000), Response: &Response{Status: 200, Body: body}, CaptureSource: "cdp", NetworkState: "complete", ResponseBodyCapture: &store.BodyCapture{State: "complete", Source: "getResponseBody", CapturedBytes: int64(len(body))}}
	messages := transferMessages(t, "large-body", request)
	for index, message := range messages {
		response, _ := handleMessage(message)
		if response["success"] != true {
			t.Fatalf("message %d failed: %#v", index, response)
		}
		if index < len(messages)-1 && len(liveData.Requests) != 0 {
			t.Fatal("partial request was published")
		}
	}
	expected := 1
	response, _ := handleMessage(&Message{Action: "session_end", SessionID: "large-body", ExpectedRequests: &expected})
	if response["success"] != true {
		t.Fatalf("end failed: %#v", response)
	}
	if err := sealCaptureSnapshot(); err != nil {
		t.Fatal(err)
	}
	rpc, _ := handleCaptureRPC(RPCRequest{Method: "bridge.capture.get", Params: json.RawMessage(`{"session_id":"large-body"}`)})
	if rpc.Error != nil {
		t.Fatal(rpc.Error)
	}
	var descriptor captureSnapshotDescriptor
	if err := json.Unmarshal(rpc.Result, &descriptor); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(descriptor.Path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if descriptor.SHA256 != hex.EncodeToString(sum[:]) || descriptor.Bytes != int64(len(data)) {
		t.Fatal("streamed snapshot digest/length mismatch")
	}
	var saved store.Export
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Requests) != 1 || saved.Requests[0].Response.Body != body || saved.Requests[0].Body != request.Body || saved.Requests[0].ResponseBodyCapture.CapturedBytes != int64(len(body)) {
		t.Fatal("full request or body provenance was lost")
	}
	if saved.BrowserSession.ExpectedRequests == nil || *saved.BrowserSession.ExpectedRequests != 1 || saved.BrowserSession.ReceivedRequests != 1 {
		t.Fatalf("missing transport accounting: %#v", saved.BrowserSession)
	}
	partials, _ := filepath.Glob(filepath.Join(captureSnapshots.dir, ".request-*.partial"))
	if len(partials) > 0 {
		t.Fatalf("spool files leaked: %v", partials)
	}
}

func TestChunkFailuresNeverPublishVerifiedCapture(t *testing.T) {
	for _, kind := range []string{"out-of-order", "duplicate", "corrupt", "short", "metadata", "missing-end", "missing-record", "foreign-session"} {
		t.Run(kind, func(t *testing.T) {
			startSnapshotHost(t)
			handleMessage(&Message{Action: "session_begin", SessionID: "broken", CaptureMode: "navigate"})
			messages := transferMessages(t, "broken", Request{ID: "large", Response: &Response{Body: strings.Repeat("x", requestChunkBytes+100)}, CaptureSource: "cdp"})
			switch kind {
			case "out-of-order":
				messages[0], messages[1] = messages[1], messages[0]
			case "duplicate":
				messages = append(messages[:1], append([]*Message{messages[0]}, messages[1:]...)...)
			case "corrupt":
				raw, _ := base64.StdEncoding.DecodeString(messages[0].Data)
				raw[20] ^= 1
				messages[0].Data = base64.StdEncoding.EncodeToString(raw)
			case "short":
				messages = messages[:1]
			case "metadata":
				messages[1].TotalBytes++
			case "missing-end":
				messages = messages[:len(messages)-1]
			case "missing-record":
				messages = nil
			case "foreign-session":
				messages[0].SessionID = "another"
				messages = messages[:1]
			}
			for _, message := range messages {
				handleMessage(message)
			}
			expected := 1
			response, _ := handleMessage(&Message{Action: "session_end", SessionID: "broken", ExpectedRequests: &expected})
			if response["success"] != false {
				t.Fatalf("broken transfer succeeded: %#v", response)
			}
			if err := sealCaptureSnapshot(); err == nil {
				t.Fatal("broken transfer sealed")
			}
			rpc, _ := handleCaptureRPC(RPCRequest{Method: "bridge.capture.get", Params: json.RawMessage(`{"session_id":"broken"}`)})
			if rpc.Error == nil || rpc.Error.Code != "capture_incomplete" {
				t.Fatalf("missing precise failure: %#v", rpc)
			}
			partials, _ := filepath.Glob(filepath.Join(captureSnapshots.dir, ".request-*.partial"))
			if len(partials) > 0 {
				t.Fatal("partial spool leaked")
			}
		})
	}
}

func TestCaptureRequestLimitIsExplicitAndConfigurable(t *testing.T) {
	t.Setenv("REP_CAPTURE_MAX_REQUESTS", "2")
	startSnapshotHost(t)
	t.Cleanup(func() { activeCaptureLimits = defaultCaptureLimits() })
	handleMessage(&Message{Action: "session_begin", SessionID: "overflow"})
	for i := 0; i < 3; i++ {
		handleMessage(&Message{Action: "add", SessionID: "overflow", Request: &Request{ID: fmt.Sprintf("r-%d", i), CaptureSource: "cdp"}})
	}
	expected := 3
	response, _ := handleMessage(&Message{Action: "session_end", SessionID: "overflow", ExpectedRequests: &expected})
	if response["success"] != false || liveData.Browser.DroppedRequests != 1 || !strings.Contains(liveData.Browser.CaptureError, "REP_CAPTURE_MAX_REQUESTS") {
		t.Fatalf("overflow was silent: %#v %#v", response, liveData.Browser)
	}
}

func TestSnapshotLimitFailsAtomically(t *testing.T) {
	t.Setenv("REP_CAPTURE_MAX_SNAPSHOT_BYTES", "1024")
	startSnapshotHost(t)
	t.Cleanup(func() { activeCaptureLimits = defaultCaptureLimits() })
	handleMessage(&Message{Action: "session_begin", SessionID: "oversized"})
	handleMessage(&Message{Action: "add", SessionID: "oversized", Request: &Request{ID: "r", Response: &Response{Body: strings.Repeat("x", 2048)}, CaptureSource: "cdp"}})
	handleMessage(&Message{Action: "session_end", SessionID: "oversized"})
	if err := sealCaptureSnapshot(); err == nil || !strings.Contains(err.Error(), "REP_CAPTURE_MAX_SNAPSHOT_BYTES") {
		t.Fatalf("unexpected limit result: %v", err)
	}
	files, _ := os.ReadDir(captureSnapshots.dir)
	if len(files) != 0 {
		t.Fatalf("failed snapshot left files: %v", files)
	}
}

func TestRequestMergePreservesKnownBodyEvidence(t *testing.T) {
	old := Request{ID: "r", Response: &Response{Status: 200, Body: "cGF5bG9hZA=="}, ResponseEncoding: "base64", ResponseBodyTruncated: true, ResponseBodyError: "body-limit", ResponseBodyCapture: &store.BodyCapture{State: "partial", Reason: "body_limit", CapturedBytes: 7}}
	merged := mergeRequest(old, Request{ID: "r", Response: &Response{Status: 200}})
	if merged.Response.Body != old.Response.Body || merged.ResponseEncoding != "base64" || !merged.ResponseBodyTruncated || merged.ResponseBodyCapture != old.ResponseBodyCapture || merged.ResponseBodyError != "body-limit" {
		t.Fatalf("late metadata lost body evidence: %#v", merged)
	}
	empty := mergeRequest(old, Request{ID: "r", Response: &Response{Status: 204}, ResponseBodyCapture: &store.BodyCapture{State: "complete", CapturedBytes: 0}})
	if empty.Response.Body != "" || empty.ResponseEncoding != "" || empty.ResponseBodyTruncated {
		t.Fatalf("verified empty body inherited obsolete bytes: %#v", empty)
	}
}

func TestCaptureLimitsRejectInvalidConfiguration(t *testing.T) {
	for _, name := range []string{"REP_CAPTURE_MAX_REQUEST_BYTES", "REP_CAPTURE_MAX_SNAPSHOT_BYTES", "REP_CAPTURE_TOTAL_SNAPSHOT_BYTES", "REP_CAPTURE_MAX_REQUESTS"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "invalid")
			if err := configureCaptureLimits(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
