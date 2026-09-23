package bodyview

import (
	"encoding/json"
	"strings"
	"testing"
)

func selectionOptions() Options { return Options{Format: "raw", Records: 20, Complete: true} }

func TestJSONPointerSkipsLargeUnrelatedDataAndValidatesTail(t *testing.T) {
	options := selectionOptions()
	options.HasPointer = true
	options.Pointer = "/a~1b/~0key/1"
	data := []byte(`{"unrelated":"` + strings.Repeat("x", 2<<20) + `","a/b":{"~key":[null,{"answer":42}]}}`)
	result, err := Select(data, options)
	if err != nil || string(result.Data) != `{"answer":42}` || !result.SourceValidated {
		t.Fatalf("selection: %s %v", result.Data, err)
	}
	for _, bad := range [][]byte{append(data, []byte(` {}`)...), data[:len(data)-1], []byte(`{"a/b":{"~key":[1,2]},"a/b":{"~key":[3,4]}}`)} {
		if _, err := Select(bad, options); err == nil {
			t.Fatal("invalid/truncated/ambiguous JSON accepted")
		}
	}
	options.Pointer = "/a~2b"
	if _, err := Select(data, options); err == nil {
		t.Fatal("invalid pointer accepted")
	}
}

func TestNDJSONPagesAndIncompleteTrailingRecord(t *testing.T) {
	options := selectionOptions()
	options.Format = "ndjson"
	options.Records = 1
	options.Complete = false
	data := []byte("{\"n\":1}\r\n{\"n\":2}\n{\"n\":")
	first, err := Select(data, options)
	if err != nil || !first.More || first.NextRecord != 1 {
		t.Fatalf("first: %+v %v", first, err)
	}
	options.RecordOffset = first.NextRecord
	second, err := Select(data, options)
	if err != nil || second.More || second.Records != 1 || second.PendingBytes != 5 || second.SourceValidated {
		t.Fatalf("second: %+v %v", second, err)
	}
	options.Complete = true
	if _, err := Select(data, options); err == nil {
		t.Fatal("malformed complete NDJSON silently accepted")
	}
	large := []byte(`{"data":"` + strings.Repeat("x", 1<<20) + "\"}\n")
	options.RecordOffset = 0
	result, err := Select(large, options)
	if err != nil || result.Records != 1 {
		t.Fatalf("large NDJSON hit scanner token ceiling: %v", err)
	}
}

func TestSSEUsesApplicationBoundariesAndRetainsIncompleteTail(t *testing.T) {
	options := selectionOptions()
	options.Format = "sse"
	options.Records = 1
	data := []byte("\ufeff: keepalive\r\nid: cursor-1\r\nevent: delta\r\ndata: first\r\ndata: second\r\n\r\ndata: third\r\n\r\ndata: unfinished\n")
	first, err := Select(data, options)
	if err != nil || !first.More || first.Records != 1 {
		t.Fatalf("first: %+v %v", first, err)
	}
	var events []eventRecord
	if err := json.Unmarshal(first.Data, &events); err != nil {
		t.Fatal(err)
	}
	if events[0].Data != "first\nsecond" || events[0].Event != "delta" || events[0].ID != "cursor-1" {
		t.Fatalf("wrong SSE assembly: %+v", events)
	}
	options.RecordOffset = first.NextRecord
	second, err := Select(data, options)
	if err != nil || second.More || second.PendingBytes == 0 || second.Records != 1 {
		t.Fatalf("tail: %+v %v", second, err)
	}
	_ = json.Unmarshal(second.Data, &events)
	if events[0].ID != "cursor-1" || events[0].Event != "message" || events[0].Data != "third" {
		t.Fatalf("SSE id inheritance/event reset: %+v", events)
	}
}

func TestSearchReturnsByteOffsetsAndDeterministicPages(t *testing.T) {
	options := selectionOptions()
	options.Find = "needle"
	options.Records = 1
	data := []byte("éneedle needle")
	first, err := Select(data, options)
	if err != nil || !first.More {
		t.Fatalf("search: %+v %v", first, err)
	}
	var matches []textMatch
	_ = json.Unmarshal(first.Data, &matches)
	if matches[0].Offset != 2 {
		t.Fatalf("expected byte offset: %+v", matches)
	}
	options.RecordOffset = 1
	second, err := Select(data, options)
	if err != nil || second.More {
		t.Fatal(err)
	}
	_ = json.Unmarshal(second.Data, &matches)
	if matches[0].Offset != 9 {
		t.Fatalf("second: %+v", matches)
	}
}
