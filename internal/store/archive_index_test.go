package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/scope"
)

func resetArchiveIndexCache() {
	archiveCache.Lock()
	archiveCache.path = ""
	archiveCache.index = nil
	archiveCache.Unlock()
}

func archiveFixture(id, hash string, ts int64, body string) Session {
	return Session{ID: id, HashID: hash, Timestamp: ts, Requests: []Request{{ID: "request", Method: "GET", URL: "https://fixture.test/data", Response: &Response{Status: 200, Body: body}}}}
}

func TestArchiveIndexPreservesClearDuplicateAndLatestSemantics(t *testing.T) {
	setupScopeStore(t)
	t.Cleanup(resetArchiveIndexCache)
	fixtures := []Session{
		archiveFixture("old", "s-old", 5, "old"),
		archiveFixture("duplicate", "s-duplicate", 25, "eligible duplicate"),
		archiveFixture("duplicate", "s-duplicate", 10, "earlier duplicate"),
		archiveFixture("same-id", "s-one", 30, "one"),
		archiveFixture("same-id", "s-two", 30, "two"),
		archiveFixture("boundary", "s-boundary", 20, "boundary"),
	}
	for i := range fixtures {
		if err := AppendSessionLog(&fixtures[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := AppendSessionClear(time.UnixMilli(20)); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := LoadSessionsLog()
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range loaded {
		indexed, handled, err := LoadIndexedSession(session.HashID)
		if err != nil || !handled || indexed == nil || indexed.ID != session.ID || indexed.Requests[0].Response.Body != session.Requests[0].Response.Body {
			t.Fatalf("indexed behavior differs for %s: %+v %v %v", session.HashID, indexed, handled, err)
		}
		if indexed.Requests[0].Domain != "fixture.test" || indexed.Requests[0].SemanticID == "" {
			t.Fatal("computed request fields missing")
		}
	}
	for _, selector := range []string{"old", "s-old", "absent"} {
		session, handled, err := LoadIndexedSession(selector)
		if err != nil || !handled || session != nil {
			t.Fatalf("inactive session exposed: %s %+v %v", selector, session, err)
		}
	}
	for _, selector := range []string{"same-id", "s-", "same"} {
		if _, handled, err := LoadIndexedSession(selector); !handled || err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("ambiguous selector accepted: %s %v", selector, err)
		}
	}
	for _, selector := range []string{"latest", "last"} {
		session, handled, err := LoadIndexedSession(selector)
		if err != nil || !handled || session.HashID != loaded[len(loaded)-1].HashID {
			t.Fatalf("latest changed: %+v %v", session, err)
		}
	}
	boundary, _, err := LoadIndexedSession("boundary")
	if err != nil || boundary == nil {
		t.Fatalf("clear cutoff equality changed: %v", err)
	}
}

func TestArchiveIndexExactHashWinsOverOtherSessionID(t *testing.T) {
	setupScopeStore(t)
	t.Cleanup(resetArchiveIndexCache)
	a := archiveFixture("a", "shared-selector", 1, "hash wins")
	b := archiveFixture("shared-selector", "other-hash", 2, "ID loses")
	for _, s := range []*Session{&a, &b} {
		if err := AppendSessionLog(s); err != nil {
			t.Fatal(err)
		}
	}
	got, handled, err := LoadIndexedSession("shared-selector")
	if err != nil || !handled || got == nil || got.Requests[0].Response.Body != "hash wins" {
		t.Fatalf("hash precedence lost: %+v %v", got, err)
	}
}

func TestArchiveIndexRebuildsLegacyMetadataAndRetainsNilRequests(t *testing.T) {
	setupScopeStore(t)
	t.Cleanup(resetArchiveIndexCache)
	if err := EnsureStoreDir(); err != nil {
		t.Fatal(err)
	}
	path, _ := GetSessionsFilePath()
	data := `{"action":"session","timestamp":7,"session":{"id":"legacy","timestamp":0,"requests":null,"future":{"nested":[true,null,"escaped \\\" value"]}}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	legacy, _, err := LoadSessionsLog()
	if err != nil {
		t.Fatal(err)
	}
	got, handled, err := LoadIndexedSession("legacy")
	if err != nil || !handled || got == nil || got.HashID != legacy[0].HashID || len(got.Requests) != 0 {
		t.Fatalf("legacy behavior changed: %+v %v", got, err)
	}
	if _, err := os.Stat(archiveIndexPath(path)); err != nil {
		t.Fatalf("cold lookup did not persist rebuildable index: %v", err)
	}
}

func TestArchiveIndexInvalidatesAfterAppendReplacementAndCacheDamage(t *testing.T) {
	setupScopeStore(t)
	t.Cleanup(resetArchiveIndexCache)
	a := archiveFixture("first", "s-first", 1, "one")
	if err := AppendSessionLog(&a); err != nil {
		t.Fatal(err)
	}
	path, _ := GetSessionsFilePath()
	if _, _, err := LoadIndexedSession("first"); err != nil {
		t.Fatal(err)
	}
	// An append by an older binary does not know the sidecar format.
	b := archiveFixture("second", "s-second", 2, "two")
	if err := appendJSONL(path, sessionLogEntry{Action: "session", Session: &b}); err != nil {
		t.Fatal(err)
	}
	got, handled, err := LoadIndexedSession("latest")
	if err != nil || !handled || got.HashID != b.HashID {
		t.Fatalf("unindexed append missed: %+v %v", got, err)
	}
	// Replacing the log, even at the same path, cannot reuse old offsets.
	replacement := filepath.Join(filepath.Dir(path), "replacement.jsonl")
	c := archiveFixture("third", "s-third", 3, "new")
	encoded, err := json.Marshal(sessionLogEntry{Action: "session", Session: &c})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	got, _, err = LoadIndexedSession("latest")
	if err != nil || got == nil || got.HashID != c.HashID {
		t.Fatalf("inode replacement missed: %+v %v", got, err)
	}
	resetArchiveIndexCache()
	if err := os.WriteFile(archiveIndexPath(path), []byte("invalid cache"), 0600); err != nil {
		t.Fatal(err)
	}
	got, _, err = LoadIndexedSession("third")
	if err != nil || got == nil || got.HashID != c.HashID {
		t.Fatalf("damaged cache prevented valid read: %+v %v", got, err)
	}
}

func TestArchiveIndexRejectsIncompleteSourceTail(t *testing.T) {
	for _, tail := range []string{`{`, `{"action":"session"`, `{"action":"session","session":{"requests":[`, `{"action":"clear","timestamp":`} {
		t.Run(fmt.Sprintf("length-%d", len(tail)), func(t *testing.T) {
			setupScopeStore(t)
			t.Cleanup(resetArchiveIndexCache)
			s := archiveFixture("valid", "s-valid", 1, "body")
			if err := AppendSessionLog(&s); err != nil {
				t.Fatal(err)
			}
			path, _ := GetSessionsFilePath()
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString(tail); err != nil {
				t.Fatal(err)
			}
			_ = f.Close()
			if got, handled, err := LoadIndexedSession("valid"); !handled || err == nil || got != nil {
				t.Fatalf("partial source became success: %+v %v", got, err)
			}
		})
	}
}

func TestArchiveIndexAppendUpdateKeepsCacheAndLogInAgreement(t *testing.T) {
	setupScopeStore(t)
	t.Cleanup(resetArchiveIndexCache)
	s := archiveFixture("first", "s-first", 1, "body")
	if err := AppendSessionLog(&s); err != nil {
		t.Fatal(err)
	}
	path, _ := GetSessionsFilePath()
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := archiveFileStamp(before)
	if err != nil {
		t.Fatal(err)
	}
	index := readArchiveIndex(path, stamp)
	if index == nil || len(index.Entries) != 1 {
		t.Fatal("first append did not index")
	}
	s = archiveFixture("second", "s-second", 2, "body 2")
	if err := AppendSessionLog(&s); err != nil {
		t.Fatal(err)
	}
	if err := AppendSessionClear(time.UnixMilli(2)); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stamp, err = archiveFileStamp(after)
	if err != nil {
		t.Fatal(err)
	}
	index = readArchiveIndex(path, stamp)
	if index == nil || len(index.Entries) != 3 {
		t.Fatal("append hook did not update index")
	}
	if index.Entries[0].Length <= 0 || index.Entries[1].Offset != index.Entries[0].Length || index.Entries[2].Action != "clear" {
		t.Fatalf("bad offsets: %+v", index.Entries)
	}
	resetArchiveIndexCache()
	disk := readArchiveIndex(path, stamp)
	if !reflect.DeepEqual(disk, index) {
		t.Fatal("memory and disk index differ")
	}
	info, err := os.Stat(archiveIndexPath(path))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("index is not private: %v", err)
	}
}

func TestArchiveIndexConcurrentAppendAndRead(t *testing.T) {
	setupScopeStore(t)
	t.Cleanup(resetArchiveIndexCache)
	first := archiveFixture("first", "s-first", 1, "seed")
	if err := AppendSessionLog(&first); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errors := make(chan error, 20)
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			s := archiveFixture(fmt.Sprintf("run-%d", i), fmt.Sprintf("s-%d", i), int64(i+2), "body")
			errors <- AppendSessionLog(&s)
		}(i)
		go func() {
			defer wg.Done()
			s, handled, err := LoadIndexedSession("first")
			if err == nil && (!handled || s == nil || s.HashID != "s-first") {
				err = fmt.Errorf("concurrent read lost seed")
			}
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	latest, _, err := LoadIndexedSession("latest")
	if err != nil || latest == nil || latest.HashID != "s-9" {
		t.Fatalf("append lost latest: %+v %v", latest, err)
	}
}

func TestArchiveIndexMissingLogAndBoundedMetadataFallback(t *testing.T) {
	setupScopeStore(t)
	t.Cleanup(resetArchiveIndexCache)
	if s, handled, err := LoadIndexedSession("missing"); err != nil || handled || s != nil {
		t.Fatalf("legacy fallback unavailable: %v %v", handled, err)
	}
	s := archiveFixture(strings.Repeat("x", maxArchiveIndexBytes), "s-large-id", 1, "small payload")
	if err := AppendSessionLog(&s); err != nil {
		t.Fatal(err)
	}
	if got, handled, err := LoadIndexedSession("s-large-id"); err != nil || handled || got != nil {
		t.Fatalf("oversized cache was retained: %v %v", handled, err)
	}
}

func BenchmarkArchiveIndexedLookup(b *testing.B) {
	b.Setenv("XDG_DATA_HOME", b.TempDir())
	b.Setenv("REP_WORKSPACE", "")
	b.Setenv("REP_TASK", "")
	scope.Reset()
	b.Cleanup(scope.Reset)
	b.Cleanup(resetArchiveIndexCache)
	if _, err := scope.Configure(scope.Options{Global: true}); err != nil {
		b.Fatal(err)
	}
	if err := EnsureStoreDir(); err != nil {
		b.Fatal(err)
	}
	path, _ := GetSessionsFilePath()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		b.Fatal(err)
	}
	writer := bufio.NewWriter(f)
	for i := 0; i < 100; i++ {
		s := archiveFixture(fmt.Sprintf("run-%d", i), fmt.Sprintf("s-%d", i), int64(i+1), strings.Repeat("x", 64<<10))
		if err := encodeSessionEntry(writer, &s); err != nil {
			b.Fatal(err)
		}
	}
	target := archiveFixture("target", "s-target", 101, "only these bytes are requested")
	if err := encodeSessionEntry(writer, &target); err != nil {
		b.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		b.Fatal(err)
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
	if _, _, err := LoadIndexedSession("s-target"); err != nil {
		b.Fatal(err)
	}
	b.Run("indexed", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			got, handled, err := LoadIndexedSession("s-target")
			if err != nil || !handled || got == nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("indexed_from_disk", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resetArchiveIndexCache()
			got, handled, err := LoadIndexedSession("s-target")
			if err != nil || !handled || got == nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("all_sessions", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, _, err := LoadSessionsLog(); err != nil {
				b.Fatal(err)
			}
		}
	})
}
