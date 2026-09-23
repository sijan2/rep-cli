package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/contextview"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
)

func summaryTestScope(t *testing.T, workspace, task string) scope.Scope {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("REPLIVE_PATH", "")
	t.Setenv("REP_WORKSPACE", "")
	t.Setenv("REP_TASK", "")
	selected, err := scope.Configure(scope.Options{Workspace: workspace, Task: task})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scope.Reset)
	return selected
}

func writeSummaryCapture(t *testing.T, path string, capture store.Export) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func summaryFixture(id, host string) store.Request {
	return store.Request{ID: id, Method: "GET", URL: "https://" + host + "/form/2027?token=secret-query", Response: &store.Response{Status: 200, Body: "private form body"}}
}

func TestEmptyTaskSummaryNeverReadsGlobalOrSavedHistory(t *testing.T) {
	selected := summaryTestScope(t, "applications", "form")
	global, _ := scope.GlobalDataDir()
	writeSummaryCapture(t, filepath.Join(global, "live.json"), store.Export{Requests: []store.Request{summaryFixture("h_1111111111111111", "ebay.example")}})
	if err := store.AppendSessionLog(&store.Session{ID: "old", Requests: []store.Request{summaryFixture("h_2222222222222222", "old.example")}}); err != nil {
		t.Fatal(err)
	}
	data, err := buildTrafficSummary(trafficSummaryOptions{MaxBytes: 2048}, false, "summary")
	if err != nil {
		t.Fatal(err)
	}
	var view contextview.View
	if err := json.Unmarshal(data, &view); err != nil {
		t.Fatal(err)
	}
	if view.Provenance.SourceStatus != "no_capture" || view.Totals.Requests != 0 || strings.Contains(string(data), "ebay") || strings.Contains(string(data), "old.example") {
		t.Fatalf("empty task inherited unrelated data: %s", data)
	}
	if view.Provenance.Workspace != selected.Workspace || view.Provenance.Task != selected.Task {
		t.Fatal("missing task provenance")
	}
}

func TestSummaryArchivesRequireExplicitSelectionAndKeepCaptureProvenance(t *testing.T) {
	selected := summaryTestScope(t, "applications", "form")
	writeSummaryCapture(t, selected.LivePath, store.Export{SessionID: "current-capture", Requests: []store.Request{summaryFixture("h_1111111111111111", "current.example")}})
	if err := store.AppendSessionLog(&store.Session{ID: "archive", CaptureSessionID: "previous-capture", Requests: []store.Request{summaryFixture("h_2222222222222222", "previous.example")}}); err != nil {
		t.Fatal(err)
	}
	for _, saved := range []string{"", "archive"} {
		input, err := loadTrafficSummary(selected, trafficSummaryOptions{Saved: saved})
		if err != nil {
			t.Fatal(err)
		}
		wantHost, wantCapture := "current.example", "current-capture"
		if saved != "" {
			wantHost, wantCapture = "previous.example", "previous-capture"
		}
		if len(input.Requests) != 1 || !strings.Contains(input.Requests[0].URL, wantHost) || input.Provenance.CaptureSessionID != wantCapture {
			t.Fatalf("wrong explicit source: %+v", input.Provenance)
		}
	}
}

func TestSummaryBudgetIncludesEnvelopeAndChangedFiltersRejectCursor(t *testing.T) {
	selected := summaryTestScope(t, "forms", "one")
	writeSummaryCapture(t, selected.LivePath, store.Export{SessionID: "capture", Requests: []store.Request{
		summaryFixture("h_1111111111111111", "form.example"), summaryFixture("h_2222222222222222", "other.example"),
	}})
	options := trafficSummaryOptions{MaxBytes: 2048}
	data, err := buildTrafficSummary(options, true, "summary")
	if err != nil {
		t.Fatal(err)
	}
	if len(data)+1 > options.MaxBytes {
		t.Fatal("envelope exceeds byte budget")
	}
	var envelope struct {
		Data contextview.View `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Cursor == "" {
		t.Fatal("missing cursor")
	}
	options.Since, options.Domain = envelope.Data.Cursor, "form.example"
	if _, err := buildTrafficSummary(options, false, "context"); err == nil {
		t.Fatal("cursor incorrectly reused across changed filters")
	}
}

func TestSummaryHostnameFiltersUseLabelBoundaries(t *testing.T) {
	selected := summaryTestScope(t, "forms", "two")
	writeSummaryCapture(t, selected.LivePath, store.Export{Requests: []store.Request{
		summaryFixture("h_1111111111111111", "form.example"), summaryFixture("h_2222222222222222", "api.form.example"), summaryFixture("h_3333333333333333", "notform.example"),
	}})
	input, err := loadTrafficSummary(selected, trafficSummaryOptions{Domain: "form.example"})
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Requests) != 2 || input.ExcludedRequests != 1 {
		t.Fatal("domain filter mixed unrelated hostname")
	}
	for _, domain := range []string{"https://form.example", "form.example/path", "*.example", "a@b", "example:443"} {
		if _, err := loadTrafficSummary(selected, trafficSummaryOptions{Domain: domain}); err == nil {
			t.Fatalf("invalid domain accepted: %q", domain)
		}
	}
}

func TestSummaryArchiveSelectionPrefersExactAndRejectsDuplicateIDs(t *testing.T) {
	sessions := []store.Session{
		{ID: "flow-one", HashID: "s_1111"}, {ID: "flow-two", HashID: "s_2222"}, {ID: "flow", HashID: "s_3333"},
	}
	selected, err := selectSummaryArchive(sessions, "flow")
	if err != nil || selected.HashID != "s_3333" {
		t.Fatal("prefixes obscured a full ID match")
	}
	sessions = append(sessions, store.Session{ID: "flow", HashID: "s_4444"})
	if _, err := selectSummaryArchive(sessions, "flow"); err == nil {
		t.Fatal("duplicate timestamp IDs selected an arbitrary archive")
	}
	selected, err = selectSummaryArchive(sessions, "s_4444")
	if err != nil || selected.HashID != "s_4444" {
		t.Fatal("full hash could not disambiguate archive")
	}
}

func TestSummaryLatestCursorRejectsDifferentArchiveWithSameDisplayID(t *testing.T) {
	selected := summaryTestScope(t, "forms", "archive")
	var cursor string
	for index := int64(1); index <= 2; index++ {
		archive := &store.Session{ID: "same-id", Timestamp: index, Requests: []store.Request{summaryFixture("h_1111111111111111", "form.example")}}
		if err := store.AppendSessionLog(archive); err != nil {
			t.Fatal(err)
		}
		if index == 1 {
			input, err := loadTrafficSummary(selected, trafficSummaryOptions{Saved: "latest"})
			if err != nil {
				t.Fatal(err)
			}
			data, err := contextview.Build(input, contextview.Options{Budget: 4096, CacheDir: filepath.Join(selected.DataDir, "context-checkpoints")})
			if err != nil {
				t.Fatal(err)
			}
			var view contextview.View
			if err := json.Unmarshal(data, &view); err != nil {
				t.Fatal(err)
			}
			cursor = view.Cursor
		}
	}
	_, err := buildTrafficSummary(trafficSummaryOptions{Saved: "latest", Since: cursor, MaxBytes: 4096}, false, "summary")
	if err == nil {
		t.Fatal("different saved archive accepted previous source cursor")
	}
}
