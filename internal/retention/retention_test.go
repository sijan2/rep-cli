package retention

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/store"
	"golang.org/x/sys/unix"
)

var gcTestNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

func gcTestRequest(id, target, contentType string, ageDays int) store.Request {
	body := strings.Repeat("payload-{}\\\"\n", 400)
	sum := sha256.Sum256([]byte(body))
	return store.Request{ID: id, Method: "GET", URL: target, Timestamp: gcTestNow.Add(-time.Duration(ageDays) * 24 * time.Hour).UnixMilli(),
		Response:            &store.Response{Status: 200, Headers: store.HeaderMap{"Content-Type": {contentType}}, Body: body},
		ResponseBodyCapture: &store.BodyCapture{State: "complete", CapturedBytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:]), Source: "fixture"}}
}

func gcTestWrite(t *testing.T, dir, name string, value any) []byte {
	t.Helper()
	raw, err := sonic.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

func gcTestSession(t *testing.T, dir string, requests []store.Request) []byte {
	t.Helper()
	gcTestWrite(t, dir, "store.json", map[string]any{"primary_domains": map[string]bool{"target.test": true}})
	return gcTestWrite(t, dir, "sessions.jsonl", map[string]any{"action": "session", "session": store.Session{ID: "archive", HashID: "s_archive1234", Timestamp: gcTestNow.UnixMilli(), Requests: requests}})
}

func gcTestPlan(t *testing.T, dir string, maxBytes int64, days int) *RetentionPlan {
	t.Helper()
	plan, err := RetentionBuildPlan(dir, RetentionOptions{MaxBytes: maxBytes, KeepDays: days, Now: gcTestNow})
	if err != nil {
		t.Fatal(err)
	}
	for _, skip := range plan.Report.Skipped {
		if strings.Contains(skip.Reason, "writer_check_unavailable") {
			t.Skip("lsof writer inspection unavailable")
		}
	}
	return plan
}

func TestGCRetentionClassificationAndAge(t *testing.T) {
	tests := []struct {
		name, url, contentType, class string
		days                          int
		selected                      bool
	}{
		{"font extension", "https://target.test/font.woff2", "application/octet-stream", RetentionAmbient, 2, true},
		{"image content type", "https://target.test/download?id=5", "image/png", RetentionAmbient, 2, true},
		{"script content type", "https://target.test/chunk", "application/javascript", RetentionAmbient, 2, true},
		{"third party", "https://analytics.test/event", "application/json", RetentionAmbient, 2, true},
		{"primary old", "https://target.test/account", "application/json", RetentionPrimary, 31, true},
		{"primary recent", "https://target.test/account", "application/json", RetentionPrimary, 2, false},
		{"primary subdomain", "https://api.target.test/account", "text/html", RetentionPrimary, 2, false},
		{"hostname boundary", "https://eviltarget.test/account", "application/json", RetentionAmbient, 2, true},
		{"recent asset", "https://target.test/style.css", "text/css", RetentionAmbient, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			original := gcTestSession(t, dir, []store.Request{gcTestRequest("h_0123456789", tt.url, tt.contentType, tt.days)})
			plan := gcTestPlan(t, dir, 1<<30, 30)
			usage := plan.Report.Classes[tt.class]
			if usage.Records != 1 || (usage.PlannedRecords == 1) != tt.selected {
				t.Fatalf("classification/age: %+v", plan.Report)
			}
			current, err := os.ReadFile(filepath.Join(dir, "sessions.jsonl"))
			if err != nil || !bytes.Equal(original, current) {
				t.Fatal("dry run changed source")
			}
		})
	}
}

func TestGCEvidenceProtection(t *testing.T) {
	tests := []struct {
		name, path  string
		value       any
		sessionNote string
	}{
		{"note ref", "notes.jsonl", map[string]any{"action": "note", "note": map[string]any{"refs": []string{"h_0123456789"}}}, ""},
		{"deleted note stays pinned", "notes.jsonl", map[string]any{"action": "note", "note": map[string]any{"refs": []string{"h_0123456789"}}}, ""},
		{"note body ref", "notes.json", map[string]any{"notes": []any{map[string]any{"content": "See h_0123456789 for the fault."}}}, ""},
		{"finding prefix", "findings.jsonl", map[string]any{"refs": []string{"012345"}}, ""},
		{"semantic ID", "finding/one.json", map[string]any{"request": "0123_GET_200"}, ""},
		{"probe record", "probes.jsonl", map[string]any{"evidence": []string{"h_0123456789"}}, ""},
		{"evidence archive", "evidence/v1/runs/run_fixture/operations/op_fixture.json", map[string]any{"references": []any{map[string]any{"kind": "capture_archive", "id": "s_archive1234"}}}, ""},
		{"original ID", "notes.jsonl", map[string]any{"refs": []string{"legacy-original"}}, ""},
		{"session note", "", nil, "Observations from this occurrence"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			request := gcTestRequest("h_0123456789", "https://target.test/image.png", "image/png", 90)
			request.OriginalID = "legacy-original"
			if tt.name == "semantic ID" {
				tt.value = map[string]any{"request": string(store.GenerateSemanticID(&request))}
			}
			original := gcTestSession(t, dir, []store.Request{request})
			if tt.sessionNote != "" {
				original = gcTestWrite(t, dir, "sessions.jsonl", map[string]any{"action": "session", "session": store.Session{ID: "archive", Note: tt.sessionNote, Requests: []store.Request{request}}})
			}
			if tt.path != "" {
				gcTestWrite(t, dir, tt.path, tt.value)
			}
			if tt.name == "deleted note stays pinned" {
				file, err := os.OpenFile(filepath.Join(dir, tt.path), os.O_WRONLY|os.O_APPEND, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = file.WriteString("{\"action\":\"clear\",\"timestamp\":1}\n")
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			plan := gcTestPlan(t, dir, 0, 0)
			if plan.Report.Classes[RetentionEvidence].Records != 1 || len(plan.Report.Evictions) != 0 {
				t.Fatalf("evidence scheduled: %+v", plan.Report)
			}
			if _, err := RetentionApply(plan); err != nil {
				t.Fatal(err)
			}
			current, _ := os.ReadFile(filepath.Join(dir, "sessions.jsonl"))
			if !bytes.Equal(original, current) {
				t.Fatal("raw evidence occurrence changed")
			}
		})
	}
}

func TestGCBudgetEvictsAmbientFirst(t *testing.T) {
	dir := t.TempDir()
	gcTestSession(t, dir, []store.Request{
		gcTestRequest("h_primary123", "https://target.test/api", "application/json", 0),
		gcTestRequest("h_ambient123", "https://other.test/event", "application/json", 0),
	})
	status, err := RetentionBuildPlan(dir, RetentionOptions{MaxBytes: 1 << 30, KeepDays: 30, Now: gcTestNow, Status: true})
	if err != nil {
		t.Fatal(err)
	}
	plan := gcTestPlan(t, dir, status.Report.TotalBytes-1000, 30)
	if plan.Report.Classes[RetentionAmbient].PlannedRecords != 1 || plan.Report.Classes[RetentionPrimary].PlannedRecords != 0 {
		t.Fatalf("ambient priority lost: %+v", plan.Report)
	}
	plan = gcTestPlan(t, dir, 0, 30)
	if plan.Report.Classes[RetentionPrimary].PlannedRecords != 1 || plan.Report.Classes[RetentionAmbient].PlannedRecords != 1 || plan.Report.BudgetUnmetBytes == 0 {
		t.Fatal("budget pressure or unavoidable metadata not reported")
	}
}

func TestGCApplyPreservesMetadataEmptyAndCompleteness(t *testing.T) {
	dir := t.TempDir()
	request := gcTestRequest("h_evicted123", "https://target.test/api", "application/json", 50)
	request.Body = strings.Repeat("outgoing", 400)
	request.RequestBodyCapture = &store.BodyCapture{State: "partial", CapturedBytes: int64(len(request.Body)), Reason: "original_limit"}
	empty := gcTestRequest("h_empty12345", "https://target.test/empty", "application/json", 50)
	empty.Response.Body = ""
	empty.ResponseBodyCapture = &store.BodyCapture{State: "complete", CapturedBytes: 0}
	gcTestWrite(t, dir, "store.json", map[string]any{"primary_domains": map[string]bool{"target.test": true}})
	raw, _ := sonic.Marshal(request)
	// The request array deliberately precedes archive identity and time.
	original := []byte(fmt.Sprintf("{\"action\":\"session\",\"future_log_field\":{\"keep\":true},\"session\":{\"requests\":[%s,%s],\"id\":\"archive\",\"hash_id\":\"s_archivetest\",\"capture_digest\":\"original-digest\",\"timestamp\":%d}}\n", raw, gcTestJSON(t, empty), gcTestNow.UnixMilli()))
	if err := os.WriteFile(filepath.Join(dir, "sessions.jsonl"), original, 0600); err != nil {
		t.Fatal(err)
	}
	plan := gcTestPlan(t, dir, 1<<30, 30)
	report, err := RetentionApply(plan)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := os.ReadFile(filepath.Join(dir, "sessions.jsonl"))
	if report.ReclaimedBytes != int64(len(original)-len(current)) || report.ReclaimedBytes != report.PlannedReclaimed {
		t.Fatalf("inaccurate disk accounting: %+v", report)
	}
	var entry struct {
		Future  map[string]bool `json:"future_log_field"`
		Session store.Session   `json:"session"`
	}
	if err := sonic.Unmarshal(current, &entry); err != nil {
		t.Fatal(err)
	}
	updated := entry.Session.Requests[0]
	if updated.Body != "" || updated.Response.Body != "" || updated.Response.Status != 200 || updated.URL != request.URL || updated.Timestamp != request.Timestamp || entry.Session.CaptureDigest != "original-digest" || !entry.Future["keep"] {
		t.Fatal("metadata lost or bodies retained")
	}
	for _, capture := range []*store.BodyCapture{updated.RequestBodyCapture, updated.ResponseBodyCapture} {
		if capture == nil || capture.State != "unavailable" || !strings.Contains(capture.Reason, "retention_evicted") || capture.CapturedBytes != 0 || capture.SHA256 != "" {
			t.Fatalf("invalid eviction descriptor: %+v", capture)
		}
	}
	if entry.Session.Requests[1].ResponseBodyCapture.State != "complete" || !bytes.Contains(current, gcTestJSON(t, empty)) {
		t.Fatal("verified empty body changed")
	}
	if !bytes.Contains(current, []byte(request.ResponseBodyCapture.SHA256)) {
		t.Fatal("original body provenance lost")
	}
	info, _ := os.Stat(filepath.Join(dir, "sessions.jsonl"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("rewrite permissions")
	}
	second := gcTestPlan(t, dir, 1<<30, 0)
	if len(second.Report.Evictions) != 0 {
		t.Fatal("already evicted bodies selected again")
	}
}

func gcTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := sonic.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestGCConcurrentChangesAndActiveCapture(t *testing.T) {
	for _, kind := range []string{"locked log", "writable descriptor", "staged append", "changed source", "new evidence", "changed note", "live capture"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			original := gcTestSession(t, dir, []store.Request{gcTestRequest("h_request123", "https://target.test/style.css", "text/css", 30)})
			path := filepath.Join(dir, "sessions.jsonl")
			plan := gcTestPlan(t, dir, 0, 0)
			switch kind {
			case "locked log":
				file, err := os.OpenFile(path, os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
					t.Fatal(err)
				}
				defer unix.Flock(int(file.Fd()), unix.LOCK_UN)
			case "writable descriptor":
				file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
			case "staged append":
				if err := os.WriteFile(filepath.Join(dir, ".rep-session-active.tmp"), []byte("in progress"), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed source":
				original = append(original, []byte("{\"action\":\"clear\",\"timestamp\":1}\n")...)
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
			case "new evidence":
				gcTestWrite(t, dir, "notes.jsonl", map[string]any{"refs": []string{"h_request123"}})
			case "changed note":
				gcTestWrite(t, dir, "notes.jsonl", map[string]any{"refs": []string{"h_unrelated"}})
				plan = gcTestPlan(t, dir, 0, 0)
				gcTestWrite(t, dir, "notes.jsonl", map[string]any{"refs": []string{"h_request123"}})
			case "live capture":
				original = gcTestWrite(t, dir, "live.json", store.Export{Requests: []store.Request{gcTestRequest("h_live123456", "https://target.test/api", "application/json", 50)}})
				path = filepath.Join(dir, "live.json")
				plan = gcTestPlan(t, dir, 0, 0)
			}
			report, err := RetentionApply(plan)
			if kind == "new evidence" || kind == "changed note" {
				if err == nil {
					t.Fatal("reference change was not rejected")
				}
			} else if err != nil {
				t.Fatal(err)
			} else if len(report.Skipped) == 0 {
				t.Fatal("concurrent file not reported skipped")
			}
			current, _ := os.ReadFile(path)
			if !bytes.Equal(original, current) {
				t.Fatal("active/changed/protected file changed")
			}
		})
	}
}

func TestGCFailClosedReferencesAndSymlinks(t *testing.T) {
	for _, kind := range []string{"corrupt notes", "evidence directory symlink", "capture symlink", "partial log"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			original := gcTestSession(t, dir, []store.Request{gcTestRequest("h_test123456", "https://target.test/a.js", "text/javascript", 60)})
			path := filepath.Join(dir, "sessions.jsonl")
			switch kind {
			case "corrupt notes":
				if err := os.WriteFile(filepath.Join(dir, "notes.jsonl"), []byte("{broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "evidence directory symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(dir, "evidence")); err != nil {
					t.Fatal(err)
				}
			case "capture symlink":
				external := filepath.Join(t.TempDir(), "sessions.jsonl")
				if err := os.WriteFile(external, original, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, path); err != nil {
					t.Fatal(err)
				}
			case "partial log":
				original = original[:len(original)-4]
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := RetentionBuildPlan(dir, RetentionOptions{MaxBytes: 0, KeepDays: 0, Now: gcTestNow})
			if kind == "corrupt notes" || kind == "evidence directory symlink" {
				if err == nil {
					t.Fatal("cannot establish references but planned eviction")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if len(plan.Report.Evictions) != 0 || len(plan.Report.Skipped) == 0 {
					t.Fatal("unsafe capture selected")
				}
				if _, err := RetentionApply(plan); err != nil {
					t.Fatal(err)
				}
			}
			current, _ := os.ReadFile(path)
			if !bytes.Equal(original, current) {
				t.Fatal("unsafe source changed")
			}
		})
	}
}

func TestGCStreamingLargePhysicalLine(t *testing.T) {
	if testing.Short() {
		t.Skip("large archive fixture")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{\"action\":\"session\",\"session\":{\"id\":\"large\",\"requests\":["); err != nil {
		t.Fatal(err)
	}
	request := gcTestRequest("h_large12345", "https://target.test/app.js", "application/javascript", 50)
	request.Response.Body = strings.Repeat("large", 20000)
	request.ResponseBodyCapture = nil
	raw := gcTestJSON(t, request)
	for i := 0; i < 100; i++ {
		if i > 0 {
			if _, err := file.WriteString(","); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := file.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := file.WriteString("]}}\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	plan := gcTestPlan(t, dir, 1<<30, 30)
	if plan.Report.Classes[RetentionAmbient].Records != 100 || plan.Report.Classes[RetentionAmbient].PlannedBodyBytes != 10000000 {
		t.Fatal("large single-line log not fully streamed")
	}
	if _, err := RetentionApply(plan); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Size() >= 100000 {
		t.Fatalf("large archive not reclaimed: %d", info.Size())
	}
}

func TestGCRetentionStreamPayloads(t *testing.T) {
	dir := t.TempDir()
	request := gcTestRequest("h_stream1234", "https://target.test/socket", "", 50)
	request.Response = nil
	request.ResponseBodyCapture = nil
	request.RecordKind = "websocket"
	request.Stream = &store.StreamCapture{Version: 1, Protocol: "websocket", State: "closed", Events: []store.StreamEvent{{Sequence: 1, Kind: "message", Bytes: 8000, Payload: strings.Repeat("stream", 1000)}}, Capture: store.StreamCoverage{State: "complete", CapturedBytes: 6000, ObservedBytes: 6000, CapturedEvents: 1, ObservedEvents: 1}}
	gcTestSession(t, dir, []store.Request{request})
	plan := gcTestPlan(t, dir, 0, 0)
	if plan.Report.Classes[RetentionPrimary].PlannedBodyBytes != 6000 {
		t.Fatal("stream bytes not accounted")
	}
	if _, err := RetentionApply(plan); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "sessions.jsonl"))
	var entry struct {
		Session store.Session `json:"session"`
	}
	if err := sonic.Unmarshal(raw, &entry); err != nil {
		t.Fatal(err)
	}
	stream := entry.Session.Requests[0].Stream
	if stream.Events[0].Payload != "" || stream.Events[0].Bytes != 8000 || stream.Capture.State != "unavailable" || stream.Capture.ObservedBytes != 6000 {
		t.Fatal("stream payload or provenance not retained correctly")
	}
}
