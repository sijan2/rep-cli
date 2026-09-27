package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testStore(t testing.TB) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func testRun(t testing.TB, s *Store) Run {
	t.Helper()
	run, err := s.BeginRun("Compare two local renderings", "Record results", map[string]string{"build": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestJournalRetainsPendingFailedAndUnknownEvidence(t *testing.T) {
	s, taskDir := testStore(t)
	run := testRun(t, s)
	start, err := s.BeginOperation(run.ID, Operation{Kind: "browser.navigate", Input: json.RawMessage(`{"tab":7}`)})
	if err != nil {
		t.Fatal(err)
	}
	// A separate process can recover the pending record even with no finish.
	reopened, err := Open(taskDir)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reopened.ListOperations(run.ID, "", 10)
	if err != nil || len(page.Operations) != 1 || page.Operations[0].Status != StatusPending || page.Operations[0].Verification != "unverified" {
		t.Fatalf("pending lost: %+v %v", page, err)
	}
	finish, err := reopened.FinishOperation(run.ID, start.ID, Operation{Status: StatusUnknown, Verification: "unknown", ModelClaim: "DONE", Error: "observation unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	if finish.ParentID != start.ID || finish.Status != StatusUnknown || finish.Verification != "unknown" || finish.Kind != start.Kind {
		t.Fatalf("invalid finish: %+v", finish)
	}
	failed, err := s.RecordOperation(run.ID, Operation{Kind: "diagnostic.trial", Status: StatusFailed, Error: "collector exited"})
	if err != nil {
		t.Fatal(err)
	}
	page, err = s.ListOperations(run.ID, start.ID, 10)
	if err != nil || len(page.Operations) != 2 || page.Total != 3 || page.Next != failed.ID || page.HasMore {
		t.Fatalf("bad journal page: %+v %v", page, err)
	}
	original, err := s.LoadOperation(run.ID, start.ID)
	if err != nil || original.Status != StatusPending || original.Phase != "started" {
		t.Fatalf("start was rewritten: %+v %v", original, err)
	}
	if _, err := s.FinishOperation(run.ID, start.ID, Operation{}); err == nil {
		t.Fatal("accepted finish without outcome")
	}
	if _, err := s.RecordOperation(run.ID, Operation{Kind: "diagnostic", Status: "success"}); err == nil {
		t.Fatal("accepted ambiguous status")
	}
}

func TestTaskIsolationAndIdentifierValidation(t *testing.T) {
	s, _ := testStore(t)
	other, _ := testStore(t)
	run := testRun(t, s)
	another := testRun(t, s)
	if _, err := other.LoadRun(run.ID); err == nil {
		t.Fatal("run crossed task boundaries")
	}
	op, err := s.RecordOperation(run.ID, Operation{Kind: "observe", Status: StatusCompleted})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListOperations(another.ID, op.ID, 1); err == nil {
		t.Fatal("accepted another run's cursor")
	}
	if _, err := s.RecordOperation(another.ID, Operation{Kind: "observe", Status: StatusCompleted, ParentID: op.ID}); err == nil {
		t.Fatal("accepted another run's operation parent")
	}
	for _, id := range []string{"../run.json", "", run.ID + "/../", strings.ToUpper(run.ID)} {
		if _, err := s.LoadRun(id); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
}

func TestConcurrentWritersAndCursorPages(t *testing.T) {
	s, taskDir := testStore(t)
	run := testRun(t, s)
	const workers = 8
	const perWorker = 5
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			other, err := Open(taskDir)
			if err != nil {
				errs <- err
				return
			}
			for i := 0; i < perWorker; i++ {
				_, err := other.RecordOperation(run.ID, Operation{Kind: "fixture", Status: StatusCompleted, Metadata: map[string]any{"worker": worker, "item": i}})
				if err != nil {
					errs <- err
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	sequence := uint64(0)
	for {
		page, err := s.ListOperations(run.ID, cursor, 7)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != workers*perWorker || len(page.Operations) > 7 {
			t.Fatalf("bad page: %+v", page)
		}
		for _, op := range page.Operations {
			sequence++
			if op.Sequence != sequence || seen[op.ID] {
				t.Fatalf("lost/duplicate sequence: %+v", op)
			}
			seen[op.ID] = true
		}
		if !page.HasMore {
			break
		}
		if page.Next == cursor {
			t.Fatal("cursor failed to progress")
		}
		cursor = page.Next
	}
	if len(seen) != workers*perWorker {
		t.Fatalf("got %d records", len(seen))
	}
}

func TestPageByteBudgetNeverSkipsRecords(t *testing.T) {
	s, _ := testStore(t)
	run := testRun(t, s)
	for i := 0; i < 9; i++ {
		_, err := s.RecordOperation(run.ID, Operation{Kind: "large-metadata", Status: StatusCompleted, Metadata: map[string]any{"text": strings.Repeat("x", 50000)}})
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListOperations(run.ID, "", 100)
	if err != nil || !page.HasMore || len(page.Operations) >= 9 {
		t.Fatalf("byte limit not applied: %d %v", len(page.Operations), err)
	}
	page2, err := s.ListOperations(run.ID, page.Next, 100)
	if err != nil || page2.HasMore || len(page.Operations)+len(page2.Operations) != 9 {
		t.Fatalf("records skipped at byte boundary: %+v %v", page2, err)
	}
	if page2.Operations[0].Sequence != page.Operations[len(page.Operations)-1].Sequence+1 {
		t.Fatal("cursor skipped unread record")
	}
	if _, err := s.RecordOperation(run.ID, Operation{Kind: "oversized", Status: StatusCompleted, Metadata: map[string]any{"text": strings.Repeat("x", MaxRecordBytes)}}); err == nil {
		t.Fatal("accepted oversized record")
	}
	last, err := s.ListOperations(run.ID, page2.Next, 100)
	if err != nil || last.Total != 9 || len(last.Operations) != 0 {
		t.Fatalf("rejected record changed index: %+v %v", last, err)
	}
}

func TestIndexedReadsDoNotLoadUnrequestedHistory(t *testing.T) {
	s, taskDir := testStore(t)
	run := testRun(t, s)
	op, err := s.RecordOperation(run.ID, Operation{Kind: "first", Status: StatusCompleted})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RecordOperation(run.ID, Operation{Kind: "second", Status: StatusCompleted})
	if err != nil {
		t.Fatal(err)
	}
	// An unrelated corrupt old manifest must not be read by Open or an exact
	// lookup. Cursor metadata is read only when that cursor is explicitly used.
	dir, _ := s.runDir(run.ID)
	if err := os.WriteFile(filepath.Join(dir, "operations", op.ID+".json"), []byte("bad JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(taskDir)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.LoadOperation(run.ID, second.ID)
	if err != nil || loaded.ID != second.ID {
		t.Fatalf("exact lookup loaded unrelated history: %v", err)
	}
	page, err := reopened.ListOperations(run.ID, second.ID, 1)
	if err != nil || len(page.Operations) != 0 {
		t.Fatalf("cursor scanned old history: %+v %v", page, err)
	}
}

func TestInterruptedIndexFailsWithoutDiscardingRecord(t *testing.T) {
	s, _ := testStore(t)
	run := testRun(t, s)
	op, err := s.BeginOperation(run.ID, Operation{Kind: "capture"})
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.runDir(run.ID)
	f, err := os.OpenFile(filepath.Join(dir, "operations.idx"), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("op_partial")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := s.ListOperations(run.ID, "", 10); err == nil || !strings.Contains(err.Error(), "incomplete evidence index") {
		t.Fatalf("index damage became success: %v", err)
	}
	if _, err := s.LoadOperation(run.ID, op.ID); err != nil {
		t.Fatalf("record lost: %v", err)
	}
}

func TestImportPreservesBytesProvenanceAndUnknownCoverage(t *testing.T) {
	s, taskDir := testStore(t)
	run := testRun(t, s)
	source := filepath.Join(t.TempDir(), "trace.bin")
	data := []byte{0, 255, 128, 13, 10, 0, 19}
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := s.ImportArtifact(run.ID, ImportSpec{Path: source, Collector: "fixture", Build: "b1", Device: "d1", Trial: "baseline", Metadata: map[string]any{"clock": "monotonic"}})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	if a.SHA256 != hex.EncodeToString(hash[:]) || a.Size != int64(len(data)) || a.Coverage.State != "unknown" || a.Build != "b1" || a.Trial != "baseline" {
		t.Fatalf("bad artifact: %+v", a)
	}
	if err := s.VerifyArtifact(run.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("changed original"), 0600); err != nil {
		t.Fatal(err)
	}
	rangeBytes, err := s.ReadArtifact(run.ID, a.ID, 1, 3)
	if err != nil || !bytes.Equal(rangeBytes.Data, data[1:4]) || rangeBytes.NextOffset != 4 || !rangeBytes.HasMore {
		t.Fatalf("bad range: %+v %v", rangeBytes, err)
	}
	all, err := s.ReadArtifact(run.ID, a.ID, 0, MaxReadBytes)
	if err != nil || !bytes.Equal(all.Data, data) || all.HasMore {
		t.Fatalf("source edit mutated archive: %+v %v", all, err)
	}
	if _, err := s.ReadArtifact(run.ID, a.ID, 0, MaxReadBytes+1); err == nil {
		t.Fatal("accepted unbounded read")
	}
	for _, path := range []string{s.dir, filepath.Join(s.dir, "runs"), filepath.Join(s.dir, "blobs")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("directory is not private: %s %v", path, err)
		}
	}
	info, err := os.Stat(filepath.Join(taskDir, filepath.FromSlash(a.Path)))
	if err != nil || info.Mode().Perm() != 0400 {
		t.Fatalf("blob is not private/read-only: %v", err)
	}
	dir, _ := s.runDir(run.ID)
	info, err = os.Stat(filepath.Join(dir, "run.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("manifest is not private: %v", err)
	}
}

func TestSameBytesKeepDistinctTrialManifestsAndDetectTampering(t *testing.T) {
	s, taskDir := testStore(t)
	run := testRun(t, s)
	other := testRun(t, s)
	source := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(source, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := s.ImportArtifact(run.ID, ImportSpec{Path: source, Trial: "first", Coverage: Coverage{State: "partial", Reason: "stopped"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.ImportArtifact(run.ID, ImportSpec{Path: source, Trial: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || a.SHA256 != b.SHA256 || a.Path != b.Path || a.Trial == b.Trial || b.Coverage.State != "unknown" {
		t.Fatalf("imports collapsed distinct evidence: %+v %+v", a, b)
	}
	if _, err := s.RecordOperation(other.ID, Operation{Kind: "capture", Status: StatusFailed, ArtifactIDs: []string{a.ID}}); err == nil {
		t.Fatal("artifact crossed run boundaries")
	}
	page, err := s.ListArtifacts(run.ID, "", 1)
	if err != nil || len(page.Artifacts) != 1 || !page.HasMore {
		t.Fatalf("bad artifact page: %+v %v", page, err)
	}
	page2, err := s.ListArtifacts(run.ID, page.Next, 1)
	if err != nil || len(page2.Artifacts) != 1 || page2.HasMore || page2.Artifacts[0].ID != b.ID {
		t.Fatalf("bad continuation: %+v %v", page2, err)
	}
	path := filepath.Join(taskDir, filepath.FromSlash(a.Path))
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyArtifact(run.ID, a.ID); err == nil {
		t.Fatal("accepted changed artifact")
	}
	if _, err := s.ImportArtifact(run.ID, ImportSpec{Path: source}); err == nil {
		t.Fatal("reused corrupt content-addressed blob")
	}
}

func TestEmptyArtifactAndNonRegularSource(t *testing.T) {
	s, _ := testStore(t)
	run := testRun(t, s)
	source := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(source, nil, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := s.ImportArtifact(run.ID, ImportSpec{Path: source})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.ReadArtifact(run.ID, a.ID, 0, 1)
	if err != nil || len(r.Data) != 0 || r.HasMore || a.Coverage.State != "unknown" {
		t.Fatalf("empty bytes implied coverage: %+v %v", r, err)
	}
	if _, err := s.ImportArtifact(run.ID, ImportSpec{Path: filepath.Dir(source)}); err == nil {
		t.Fatal("accepted directory import")
	}
	if _, err := s.ImportArtifact(run.ID, ImportSpec{Path: source, Coverage: Coverage{State: "proven"}}); err == nil {
		t.Fatal("accepted unsupported coverage declaration")
	}
}

func TestRunPagination(t *testing.T) {
	s, _ := testStore(t)
	var runs []Run
	for i := 0; i < 4; i++ {
		runs = append(runs, testRun(t, s))
	}
	page, err := s.ListRunPage("", 2)
	if err != nil || len(page.Runs) != 2 || !page.HasMore || page.Next != runs[1].ID {
		t.Fatalf("bad first page: %+v %v", page, err)
	}
	page, err = s.ListRunPage(page.Next, 2)
	if err != nil || len(page.Runs) != 2 || page.HasMore || page.Runs[0].ID != runs[2].ID {
		t.Fatalf("bad next page: %+v %v", page, err)
	}
	latest, err := s.ListRuns(2)
	if err != nil || len(latest) != 2 || latest[0].ID != runs[3].ID {
		t.Fatalf("bad latest runs: %+v %v", latest, err)
	}
	if _, err := s.ListRuns(MaxLimit + 1); err == nil {
		t.Fatal("accepted unbounded page")
	}
}

func BenchmarkEvidenceOpen(b *testing.B) {
	s, dir := testStore(b)
	for i := 0; i < 100; i++ {
		testRun(b, s)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Open(dir); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEvidenceTailPage(b *testing.B) {
	s, _ := testStore(b)
	run := testRun(b, s)
	cursor := ""
	for i := 0; i < 1000; i++ {
		op, err := s.RecordOperation(run.ID, Operation{Kind: "fixture", Status: StatusCompleted})
		if err != nil {
			b.Fatal(err)
		}
		if i == 989 {
			cursor = op.ID
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page, err := s.ListOperations(run.ID, cursor, 10)
		if err != nil || len(page.Operations) != 10 {
			b.Fatal(fmt.Sprintf("tail: %v", err))
		}
	}
}

func BenchmarkEvidenceRecordOperation(b *testing.B) {
	s, _ := testStore(b)
	run := testRun(b, s)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.RecordOperation(run.ID, Operation{Kind: "fixture", Status: StatusCompleted}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEvidenceImport(b *testing.B) {
	s, _ := testStore(b)
	run := testRun(b, s)
	source := filepath.Join(b.TempDir(), "trace.bin")
	if err := os.WriteFile(source, bytes.Repeat([]byte{42}, 1<<20), 0600); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(1 << 20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.ImportArtifact(run.ID, ImportSpec{Path: source}); err != nil {
			b.Fatal(err)
		}
	}
}
