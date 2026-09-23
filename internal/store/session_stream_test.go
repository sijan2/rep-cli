package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSessionStreamRoundTripLargeRequestsAndMetadata(t *testing.T) {
	setupScopeStore(t)
	body := strings.Repeat("café 雪 😀\n", 100000)
	expected := 2
	session := Session{ID: "large", HashID: "s-large", Timestamp: 100, Note: "metadata preserved", CaptureSessionID: "capture-owned", CaptureDigest: "fixture-digest", BrowserSession: &BrowserSession{CaptureMode: "navigate", ExpectedRequests: &expected, ReceivedRequests: 2}, Requests: []Request{
		{ID: "first", Response: &Response{Body: body}, ResponseBodyCapture: &BodyCapture{State: "complete", CapturedBytes: int64(len(body))}},
		{ID: "second", Body: body, RequestBodyCapture: &BodyCapture{State: "complete", CapturedBytes: int64(len(body))}},
	}}
	if err := AppendSessionLog(&session); err != nil {
		t.Fatal(err)
	}
	path, _ := GetSessionsFilePath()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(raw, []byte{'\n'}) != 1 {
		t.Fatal("entry is no longer a single JSONL record")
	}
	var standard sessionLogEntry
	if err = json.Unmarshal(raw, &standard); err != nil {
		t.Fatal(err)
	}
	if standard.Session.Requests[0].Response.Body != body || standard.Session.CaptureDigest != session.CaptureDigest {
		t.Fatal("standard JSON reader lost data")
	}
	loaded, exists, err := LoadSessionsLog()
	if err != nil || !exists || len(loaded) != 1 {
		t.Fatalf("load: %v %v %d", err, exists, len(loaded))
	}
	if loaded[0].Requests[1].Body != body || loaded[0].BrowserSession.ExpectedRequests == nil || *loaded[0].BrowserSession.ExpectedRequests != 2 || loaded[0].Requests[0].ResponseBodyCapture.CapturedBytes != int64(len(body)) {
		t.Fatal("streaming reader lost request bytes or provenance")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("archive permissions are not private")
	}
}

func TestSessionAppendStagesFailedEntriesWithoutChangingArchive(t *testing.T) {
	setupScopeStore(t)
	if err := AppendSessionLog(&Session{ID: "existing", HashID: "s-existing", Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	path, _ := GetSessionsFilePath()
	before, _ := os.ReadFile(path)
	err := appendJSONLStream(path, func(writer io.Writer) error {
		_, _ = io.WriteString(writer, `{"action":"session"`)
		return errors.New("injected serialization error")
	})
	if err == nil {
		t.Fatal("injected failure was ignored")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed entry changed durable archive")
	}
	temps, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".rep-session-*.tmp"))
	if len(temps) > 0 {
		t.Fatal("staged files leaked")
	}
}

func TestConcurrentSessionAppendsRemainWholeRecords(t *testing.T) {
	setupScopeStore(t)
	var group sync.WaitGroup
	errors := make(chan error, 12)
	for index := 0; index < 12; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			errors <- AppendSessionLog(&Session{ID: fmt.Sprintf("session-%d", index), HashID: fmt.Sprintf("s-%d", index), Timestamp: int64(index + 1), Requests: []Request{{ID: "r", Response: &Response{Body: strings.Repeat(fmt.Sprintf("%d", index), 100000)}}}})
		}(index)
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	loaded, _, err := LoadSessionsLog()
	if err != nil || len(loaded) != 12 {
		t.Fatalf("concurrent appends lost records: %d %v", len(loaded), err)
	}
	for index, session := range loaded {
		if session.Requests[0].Response.Body != strings.Repeat(fmt.Sprintf("%d", index), 100000) {
			t.Fatal("concurrent append mixed request bytes")
		}
	}
}

func TestSessionStreamReadsLegacyAndRejectsIncompleteTail(t *testing.T) {
	setupScopeStore(t)
	path, _ := GetSessionsFilePath()
	if err := EnsureStoreDir(); err != nil {
		t.Fatal(err)
	}
	legacy := `{"action":"session","session":{"id":"old","hash_id":"s-old","timestamp":1,"requests":null,"future":{"ignored":true}}}` + "\n"
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := LoadSessionsLog()
	if err != nil || len(loaded) != 1 || len(loaded[0].Requests) != 0 {
		t.Fatalf("legacy log rejected: %v", err)
	}
	file, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = file.WriteString(`{"action":"session","session":{"requests":[`)
	_ = file.Close()
	if loaded, _, err = LoadSessionsLog(); err == nil || loaded != nil {
		t.Fatal("partial archive tail was silently accepted")
	}
}
