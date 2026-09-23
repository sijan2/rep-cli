package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

type boundedExportWriter struct {
	bytes.Buffer
	maxWrite int
}

func (writer *boundedExportWriter) Write(data []byte) (int, error) {
	if len(data) > writer.maxWrite {
		return 0, fmt.Errorf("unexpected whole-capture write of %d bytes", len(data))
	}
	return writer.Buffer.Write(data)
}

func TestExportStreamingLargeRoundTripAndMetadata(t *testing.T) {
	text := strings.Repeat("café 雪 😀\n", 100000)
	binary := bytes.Repeat([]byte{0, 255, 128, 13, 10}, 150000)
	expected := 2
	export := Export{
		Version: "2.0", ExportedAt: "2026-09-20T01:02:03Z", SessionID: "fixture-owned", CaptureDigest: strings.Repeat("d", 64),
		BrowserSession: &BrowserSession{Browser: "arc", TabID: 3, StartedAt: "start", FinishedAt: "finish", ExpectedRequests: &expected, ReceivedRequests: 2, CaptureWarnings: []string{"related_targets_unavailable"}},
		Requests: []Request{
			{ID: "text", Method: "POST", URL: "https://fixture.invalid/data", Body: text, Response: &Response{Status: 200, Headers: HeaderMap{"content-type": {"text/plain"}}, Body: text}, ResponseBodyCapture: &BodyCapture{State: "complete", CapturedBytes: int64(len(text)), Source: "cdp-stream"}, NetworkState: "complete"},
			{ID: "binary", Response: &Response{Status: 200, Body: base64.StdEncoding.EncodeToString(binary)}, ResponseEncoding: "base64", ResponseBodyCapture: &BodyCapture{State: "complete", CapturedBytes: int64(len(binary)), Encoding: "base64"}},
		},
	}
	// No single write should contain the complete capture: this cap admits each
	// request but rejects a whole-export encoding buffer.
	writer := &boundedExportWriter{maxWrite: 4 << 20}
	if err := EncodeExport(writer, export); err != nil {
		t.Fatal(err)
	}
	var standard Export
	if err := json.Unmarshal(writer.Bytes(), &standard); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(standard, export) {
		t.Fatal("standard JSON decoder lost encoded metadata")
	}
	loaded, err := DecodeExport(bytes.NewReader(writer.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, export) {
		t.Fatal("streaming decoder lost body bytes or provenance")
	}
}

func TestExportStreamingConsumesWhitespaceForIntegrityHash(t *testing.T) {
	raw := []byte("{\"version\":\"2.0\",\"requests\":[]}\n" + strings.Repeat(" \t\r\n", 20000))
	digest := sha256.New()
	export, err := DecodeExport(io.TeeReader(bytes.NewReader(raw), digest))
	if err != nil || export.Version != "2.0" {
		t.Fatalf("valid export failed: %v", err)
	}
	expected := sha256.Sum256(raw)
	if !bytes.Equal(digest.Sum(nil), expected[:]) {
		t.Fatal("trailing whitespace was not included in stream hash")
	}
}

func TestExportStreamingRejectsPartialAndAmbiguousData(t *testing.T) {
	for _, raw := range []string{
		`{"version":"2.0","requests":[{"id":"first","response":{"body":"complete"}},{"id":"second","response":{"body":"cut`,
		`{"requests":[{"id":"first"}]`,
		`{"requests":[]} {"requests":[]}`,
		`{"requests":[]} trailing`,
		`{"requests":[],"requests":[]}`,
		`{"session_id":"first","session_id":"other","requests":[]}`,
		`{"requests":{}}`,
		`null`,
	} {
		export, err := DecodeExport(strings.NewReader(raw))
		if err == nil {
			t.Fatalf("invalid capture accepted: %q", raw)
		}
		if !reflect.DeepEqual(export, Export{}) {
			t.Fatalf("partial capture returned on error: %#v", export)
		}
	}
}

func TestExportStreamingLegacyNullAndFutureMetadata(t *testing.T) {
	for _, requests := range []string{"null", "[]"} {
		raw := `{"version":"2.0","future":{"nested":[true,null,{"number":123456789012345678901234567890}]},"requests":` + requests + `}`
		export, err := DecodeExport(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if (export.Requests == nil) != (requests == "null") {
			t.Fatal("legacy nil/empty requests changed")
		}
		var encoded bytes.Buffer
		if err = EncodeExport(&encoded, export); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(encoded.String(), `"requests":`+requests) {
			t.Fatal("nil/empty representation was lost")
		}
	}
}

type failedExportWriter struct{}

func (failedExportWriter) Write([]byte) (int, error) { return 0, errors.New("injected disk failure") }

func TestExportStreamingPropagatesWriterFailure(t *testing.T) {
	err := EncodeExport(failedExportWriter{}, Export{Version: "2.0", Requests: []Request{{ID: "r", Body: strings.Repeat("x", 2<<20)}}})
	if err == nil || !strings.Contains(err.Error(), "injected disk failure") {
		t.Fatalf("writer failure was lost: %v", err)
	}
}
