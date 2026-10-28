package commands

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
)

func replayOriginalFixture(body []byte) *store.Request {
	return &store.Request{
		ID: "fixture", Method: http.MethodGet,
		Response:            &store.Response{Status: 200, Body: string(body)},
		ResponseBodyCapture: &store.BodyCapture{State: "complete", CapturedBytes: int64(len(body))},
	}
}

func replayResponseFixture(body []byte) *replayResult {
	return &replayResult{Status: 200, Body: body, BodyCapture: store.BodyCapture{State: "complete", CapturedBytes: int64(len(body))}}
}

func TestReplayComparisonUsesEveryByte(t *testing.T) {
	prefix := strings.Repeat("a", 8192)
	for _, tt := range []struct {
		name, original, replay string
		different              bool
	}{
		{"equal", prefix + "tail", prefix + "tail", false},
		{"different_tail", prefix + "a", prefix + "b", true},
		{"different_length", prefix, prefix + "a", true},
		{"json_string_whitespace", `{"value":"a  b"}`, `{"value":"a b"}`, true},
		{"leading_whitespace", " value", "value", true},
		{"line_endings", "value\r\n", "value\n", true},
		{"binary", "\x00\xff\x01", "\x00\xfe\x01", true},
		{"verified_empty", "", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			diff := computeReplayDiff(replayOriginalFixture([]byte(tt.original)), replayResponseFixture([]byte(tt.replay)))
			state := "equal"
			if tt.different {
				state = "different"
			}
			if diff.BodyDistinct == nil || *diff.BodyDistinct != tt.different || diff.BodyComparison.State != state || diff.BodyComparison.Method != "exact_bytes" {
				t.Fatalf("incorrect comparison: %+v", diff)
			}
			if diff.SizeDelta == nil || *diff.SizeDelta != len(tt.replay)-len(tt.original) {
				t.Fatalf("incorrect retained size delta: %+v", diff)
			}
		})
	}
}

func TestReplayComparisonRequiresCompleteEvidence(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*store.Request, *replayResult)
	}{
		{"legacy", func(req *store.Request, _ *replayResult) { req.ResponseBodyCapture = nil }},
		{"legacy_truncated", func(req *store.Request, _ *replayResult) {
			req.ResponseBodyCapture = nil
			req.ResponseBodyTruncated = true
		}},
		{"missing_response", func(req *store.Request, _ *replayResult) { req.Response = nil }},
		{"partial_original", func(req *store.Request, _ *replayResult) { req.ResponseBodyCapture.State = "partial" }},
		{"unknown_original", func(req *store.Request, _ *replayResult) { req.ResponseBodyCapture.State = "unknown" }},
		{"missing_original_state", func(req *store.Request, _ *replayResult) { req.ResponseBodyCapture.State = "" }},
		{"unavailable_original", func(req *store.Request, _ *replayResult) { req.ResponseBodyCapture.State = "unavailable" }},
		{"not_applicable_original", func(req *store.Request, _ *replayResult) { req.ResponseBodyCapture.State = "not_applicable" }},
		{"conflicting_truncation", func(req *store.Request, _ *replayResult) { req.ResponseBodyTruncated = true }},
		{"conflicting_read_error", func(req *store.Request, _ *replayResult) { req.ResponseBodyError = "interrupted" }},
		{"conflicting_complete_reason", func(req *store.Request, _ *replayResult) { req.ResponseBodyCapture.Reason = "capture_limit" }},
		{"conflicting_observed_bytes", func(req *store.Request, _ *replayResult) {
			observed := req.ResponseBodyCapture.CapturedBytes + 1
			req.ResponseBodyCapture.ObservedBytes = &observed
		}},
		{"negative_observed_bytes", func(req *store.Request, _ *replayResult) {
			observed := int64(-1)
			req.ResponseBodyCapture.ObservedBytes = &observed
		}},
		{"negative_expected_bytes", func(req *store.Request, _ *replayResult) {
			expected := int64(-1)
			req.ResponseBodyCapture.ExpectedBytes = &expected
		}},
		{"negative_captured_bytes", func(req *store.Request, _ *replayResult) { req.ResponseBodyCapture.CapturedBytes = -1 }},
		{"invalid_original_state", func(req *store.Request, _ *replayResult) { req.ResponseBodyCapture.State = "unrecognized" }},
		{"invalid_original_size", func(req *store.Request, _ *replayResult) { req.ResponseBodyCapture.CapturedBytes++ }},
		{"invalid_original_digest", func(req *store.Request, _ *replayResult) { req.ResponseBodyCapture.SHA256 = strings.Repeat("0", 64) }},
		{"invalid_original_base64", func(req *store.Request, _ *replayResult) { req.ResponseEncoding = "base64" }},
		{"unsupported_original_encoding", func(req *store.Request, _ *replayResult) { req.ResponseEncoding = "unknown" }},
		{"partial_replay", func(_ *store.Request, res *replayResult) { res.BodyCapture.State = "partial" }},
		{"unknown_replay", func(_ *store.Request, res *replayResult) { res.BodyCapture.State = "" }},
		{"replay_read_error", func(_ *store.Request, res *replayResult) { res.BodyError = "unexpected EOF" }},
		{"replay_truncated", func(_ *store.Request, res *replayResult) { res.BodyTruncated = true }},
		{"invalid_replay_size", func(_ *store.Request, res *replayResult) { res.BodyCapture.CapturedBytes++ }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Identical retained bytes still cannot establish equality.
			req, res := replayOriginalFixture([]byte("hello")), replayResponseFixture([]byte("hello"))
			tt.change(req, res)
			diff := computeReplayDiff(req, res)
			if diff.BodyDistinct != nil || diff.BodyComparison.State != "inconclusive" || diff.BodyComparison.Reason == "" {
				t.Fatalf("insufficient evidence became conclusive: %+v", diff)
			}
			encoded, err := json.Marshal(diff)
			if err != nil || !bytes.Contains(encoded, []byte(`"body_distinct":null`)) {
				t.Fatalf("unknown was serialized as false: %s, %v", encoded, err)
			}
		})
	}
}

func TestReplayComparisonDoesNotTreatLegacyEmptyAsVerifiedEmpty(t *testing.T) {
	req := &store.Request{Response: &store.Response{Status: 204}}
	diff := computeReplayDiff(req, replayResponseFixture(nil))
	if diff.BodyDistinct != nil || diff.BodyComparison.OriginalState != "unknown" {
		t.Fatalf("legacy empty body was treated as complete: %+v", diff)
	}
}

func TestReplayComparisonDecodesStoredResponse(t *testing.T) {
	body := []byte{0, 255, 1, 254, 2}
	for _, encodingLocation := range []string{"response", "metadata"} {
		t.Run(encodingLocation, func(t *testing.T) {
			req := replayOriginalFixture(body)
			req.Response.Body = base64.StdEncoding.EncodeToString(body)
			if encodingLocation == "response" {
				req.ResponseEncoding = "base64"
			} else {
				req.ResponseBodyCapture.Encoding = "base64"
			}
			summary := summarizeOriginal(req)
			if summary.Size != len(body) || summary.BodyCapture.State != "complete" || summary.BodyError != "" {
				t.Fatalf("stored encoding leaked into body evidence: %+v", summary)
			}
			diff := computeReplayDiff(req, replayResponseFixture(body))
			if diff.BodyDistinct == nil || *diff.BodyDistinct || diff.SizeDelta == nil || *diff.SizeDelta != 0 {
				t.Fatalf("decoded payload did not compare equal: %+v", diff)
			}
		})
	}
}

func TestReplayOriginalCorruptionIsExplicit(t *testing.T) {
	req := replayOriginalFixture([]byte("retained"))
	req.ResponseBodyCapture.CapturedBytes++
	summary := summarizeOriginal(req)
	if summary.BodyCapture.State != "unknown" || summary.BodyCapture.Reason != "invalid_body_evidence" || summary.BodyError == "" {
		t.Fatalf("corruption was hidden: %+v", summary)
	}
	diff := computeReplayDiff(req, replayResponseFixture([]byte("retained")))
	if diff.SizeDelta != nil || diff.BodyDistinct != nil {
		t.Fatalf("invalid evidence was compared: %+v", diff)
	}
}

func TestReplayRejectsCompleteBodyWithContradictoryObservedCount(t *testing.T) {
	req := replayOriginalFixture([]byte("retained"))
	observed := req.ResponseBodyCapture.CapturedBytes + 1
	req.ResponseBodyCapture.ObservedBytes = &observed
	summary := summarizeOriginal(req)
	if summary.BodyCapture.State != "unknown" || summary.BodyError == "" {
		t.Fatalf("contradictory metadata was presented as complete: %+v", summary)
	}
	diff := computeReplayDiff(req, replayResponseFixture([]byte("retained")))
	if diff.BodyDistinct != nil || diff.BodyComparison.State != "inconclusive" || diff.BodyComparison.OriginalState != "unknown" || diff.SizeDelta != nil {
		t.Fatalf("contradictory observed-byte count became equality: %+v", diff)
	}
}

func TestExecuteReplayRetainsBoundedBodyEvidence(t *testing.T) {
	for _, tt := range []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"small", 123},
		{"at_limit", maxReplayBodyBytes},
		{"over_limit", maxReplayBodyBytes + 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			payload := bytes.Repeat([]byte{'x'}, tt.size)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			res, err := executeReplay(context.Background(), &store.Request{Method: http.MethodGet, URL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			retained := min(tt.size, maxReplayBodyBytes)
			if len(res.Body) != retained || res.BodyCapture.CapturedBytes != int64(retained) || res.BodyCapture.ObservedBytes == nil || *res.BodyCapture.ObservedBytes != int64(tt.size) {
				t.Fatalf("incorrect bounded read: %d bytes, %+v", len(res.Body), res.BodyCapture)
			}
			digest := sha256.Sum256(payload[:retained])
			if res.BodyCapture.SHA256 != hex.EncodeToString(digest[:]) || !bytes.Equal(res.Body, payload[:retained]) {
				t.Fatal("retained bytes or their digest changed")
			}
			wantState := "complete"
			if tt.size > maxReplayBodyBytes {
				wantState = "partial"
			}
			if res.BodyCapture.State != wantState || res.BodyTruncated != (tt.size > maxReplayBodyBytes) || res.BodyError != "" {
				t.Fatalf("incorrect completeness: %+v, truncated=%t error=%s", res.BodyCapture, res.BodyTruncated, res.BodyError)
			}
			if res.BodyTruncated && res.BodyCapture.Reason != "replay_body_limit" {
				t.Fatalf("missing truncation reason: %+v", res.BodyCapture)
			}
			if res.HTTPProtocol != "HTTP/1.1" || res.FinalURL != server.URL || res.BodyCapture.Source != "go-http-client" {
				t.Fatalf("missing client provenance: %+v", res.BodyCapture)
			}
		})
	}
}

func TestExecuteReplayPreservesBytesAfterReadFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("partial"))
	}))
	defer server.Close()
	res, err := executeReplay(context.Background(), &store.Request{Method: http.MethodGet, URL: server.URL})
	if err != nil {
		t.Fatalf("response evidence was discarded: %v", err)
	}
	if res.Status != http.StatusAccepted || string(res.Body) != "partial" || res.BodyCapture.State != "partial" || res.BodyCapture.Reason != "response_body_read_failed" || res.BodyError == "" {
		t.Fatalf("body failure was hidden: status=%d body=%q capture=%+v error=%s", res.Status, res.Body, res.BodyCapture, res.BodyError)
	}
	diff := computeReplayDiff(replayOriginalFixture(res.Body), res)
	if diff.BodyDistinct != nil || diff.BodyComparison.State != "inconclusive" {
		t.Fatalf("failed read became equality: %+v", diff)
	}
}

func TestExecuteReplayReportsDecompressionAndRedirect(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte("decoded payload"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	defer server.Close()
	for _, explicitEncoding := range []bool{false, true} {
		t.Run(fmt.Sprint(explicitEncoding), func(t *testing.T) {
			req := &store.Request{Method: http.MethodGet, URL: server.URL + "/start"}
			if explicitEncoding {
				req.Headers = store.HeaderMap{"Accept-Encoding": {"gzip"}}
			}
			res, err := executeReplay(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if res.FinalURL != server.URL+"/final" || res.AutomaticallyDecompressed == explicitEncoding {
				t.Fatalf("incorrect response representation: url=%s decompressed=%t", res.FinalURL, res.AutomaticallyDecompressed)
			}
			want := []byte("decoded payload")
			if explicitEncoding {
				want = compressed.Bytes()
			}
			if !bytes.Equal(res.Body, want) {
				t.Fatal("response representation was not preserved")
			}
		})
	}
}

func TestReplaySummaryKeepsBinaryBytesLossless(t *testing.T) {
	res := replayResponseFixture([]byte{0, 255, 1})
	summary, _ := buildReplaySummary(res)
	decoded, err := base64.StdEncoding.DecodeString(summary.InlineBodyPreview)
	if err != nil || summary.InlineBodyEncoding != "base64" || !bytes.Equal(decoded, res.Body) {
		t.Fatalf("binary preview lost bytes: %+v, %v", summary, err)
	}
}

func TestReplayMutantIncludesExplicitEquality(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("fixture"))
	}))
	defer server.Close()
	req := replayOriginalFixture([]byte("fixture"))
	req.URL = server.URL
	result := runOneMutant(req, "header.X-Fixture", "test")
	if result.Error != nil || result.BodyComparison == nil || result.BodyComparison.State != "equal" || result.BodyCapture == nil || result.BodyCapture.State != "complete" || result.HTTPProtocol != "HTTP/1.1" || result.FinalURL != server.URL {
		t.Fatalf("comparison evidence missing from result: %+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil || !bytes.Contains(encoded, []byte(`"body_distinct":false`)) {
		t.Fatalf("verified equality was omitted: %s, %v", encoded, err)
	}
}

func TestReplayArtifactsArePrivateUniqueAndTaskScoped(t *testing.T) {
	summaryTestScope(t, "project", "agent-a")
	paths := map[string]string{}
	for _, task := range []string{"agent-a", "agent-b"} {
		selected, err := scope.Configure(scope.Options{Workspace: "project", Task: task})
		if err != nil {
			t.Fatal(err)
		}
		for _, body := range []string{"first " + task, "later " + task} {
			path, err := writeSpill("text/plain", []byte(body))
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := paths[path]; exists || filepath.Dir(path) != filepath.Join(selected.DataDir, "replay-exports") {
				t.Fatalf("artifact collides or escaped its task: %s", path)
			}
			paths[path] = body
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("artifact is not private: %v, %v", info, err)
			}
			dirInfo, err := os.Stat(filepath.Dir(path))
			if err != nil || dirInfo.Mode().Perm() != 0700 {
				t.Fatalf("artifact directory is not private: %v, %v", dirInfo, err)
			}
		}
	}
	for path, want := range paths {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != want {
			t.Fatalf("a later save changed %s: %q, %v", path, body, err)
		}
	}
}

func TestReplayGlobalArtifactsRemainUnique(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("REP_SPILL_DIR", t.TempDir())
	t.Cleanup(scope.Reset)
	if _, err := scope.Configure(scope.Options{Global: true}); err != nil {
		t.Fatal(err)
	}
	first, err := writeSpill("application/xml", []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := writeSpill("application/xml", []byte("second"))
	if err != nil || first == second {
		t.Fatalf("global saves collided: %q, %q, %v", first, second, err)
	}
	wantDir := filepath.Join(os.TempDir(), "rep-artifacts", "replay-exports")
	if filepath.Dir(first) != wantDir || filepath.Ext(first) != ".xml" {
		t.Fatalf("incorrect global artifact destination: %s", first)
	}
	body, err := os.ReadFile(first)
	if err != nil || string(body) != "first" {
		t.Fatalf("original global artifact replaced: %q, %v", body, err)
	}
}

func TestReplayArtifactRejectsSharedDirectorySymlink(t *testing.T) {
	selected := summaryTestScope(t, "project", "agent")
	if err := os.MkdirAll(selected.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	shared := t.TempDir()
	if err := os.Symlink(shared, filepath.Join(selected.DataDir, "replay-exports")); err != nil {
		t.Fatal(err)
	}
	if path, err := writeSpill("text/plain", []byte("private bytes")); err == nil || path != "" {
		t.Fatalf("replay saved through a shared directory link: %q, %v", path, err)
	}
	entries, err := os.ReadDir(shared)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed save leaked bytes to shared directory: %v, %v", entries, err)
	}
}

func TestReplayArtifactFailureIsVisibleWithoutNetworkRetry(t *testing.T) {
	selected := summaryTestScope(t, "project", "agent")
	if err := os.MkdirAll(selected.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	// A file at the artifact directory path causes a deterministic write failure
	// even if the test process has permission to bypass Unix mode restrictions.
	if err := os.WriteFile(filepath.Join(selected.DataDir, "replay-exports"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	payload := []byte(strings.Repeat("x", output.OverflowPreviewSize+1))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	req := replayOriginalFixture(payload)
	req.URL = server.URL
	previousIncludeBody := replayIncludeBody
	replayIncludeBody = false
	t.Cleanup(func() { replayIncludeBody = previousIncludeBody })

	encoded := replayCaptureOutput(t, func() error {
		return runSingle(req, summarizeOriginal(req), nil, true)
	})
	var envelope struct {
		Data struct {
			Replay replaySummary `json:"replay"`
			Diff   diffReport    `json:"diff"`
		} `json:"data"`
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || envelope.Data.Replay.ArtifactError == "" || envelope.Data.Replay.ResponseBodyPath != "" || envelope.Data.Diff.BodyComparison.State != "equal" || envelope.Data.Replay.BodyCapture.State != "complete" {
		t.Fatalf("single replay hid its artifact failure or retried: %s, requests=%d", encoded, requests.Load())
	}
	result := runOneMutant(req, "header.X-Fixture", "test")
	if requests.Load() != 2 || result.ArtifactError == "" || result.SpillPath != "" || result.Error != nil || result.BodyComparison == nil || result.BodyComparison.State != "equal" {
		t.Fatalf("matrix result hid its artifact failure or retried: %+v, requests=%d", result, requests.Load())
	}
	text := replayCaptureOutput(t, func() error {
		printReplayText(output.WrapData("replay", "fixture", map[string]interface{}{"replay": envelope.Data.Replay, "mutants": []mutantResult{result}}))
		return nil
	})
	if strings.Count(string(text), "artifact error:") != 2 {
		t.Fatalf("plain output hid artifact failures: %s", text)
	}
}

func replayCaptureOutput(t *testing.T, run func() error) []byte {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	previous := os.Stdout
	os.Stdout = file
	defer func() { os.Stdout = previous }()
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
