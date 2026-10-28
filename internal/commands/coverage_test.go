package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
)

func TestCoverageEmptyTaskNeverReadsOtherSources(t *testing.T) {
	summaryTestScope(t, "coverage", "empty")
	global, _ := scope.GlobalDataDir()
	writeSummaryCapture(t, filepath.Join(global, "live.json"), store.Export{Requests: []store.Request{summaryFixture("global", "global.example")}})
	if err := store.AppendSessionLog(&store.Session{ID: "old", Requests: []store.Request{summaryFixture("saved", "saved.example")}}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	command := newCoverageCommand()
	command.SetOut(&out)
	if err := command.RunE(command, nil); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Provenance observedCaptureProvenance `json:"provenance"`
		Coverage   CaptureCoverageReport     `json:"coverage"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Provenance.SourceStatus != "no_capture" || report.Coverage.Capture.State != "no_capture" || strings.Contains(out.String(), ".example") {
		t.Fatalf("wrong empty-task result: %s", out.String())
	}
}

func TestObservedCapturePinsArchiveAndPreservesCollectorMetadata(t *testing.T) {
	selected := summaryTestScope(t, "coverage", "archive")
	writeSummaryCapture(t, selected.LivePath, store.Export{SessionID: "live-session"})
	expected := 3
	archive := &store.Session{ID: "archive", HashID: "s_0123456789abcdef", CaptureSessionID: "saved-session", CaptureDigest: "digest",
		Requests:       []store.Request{summaryFixture("saved", "saved.example")},
		BrowserSession: &store.BrowserSession{DroppedRequests: 2, ExpectedRequests: &expected, ReceivedRequests: 1, CaptureWarnings: []string{"fixture gap"}},
	}
	if err := store.AppendSessionLog(archive); err != nil {
		t.Fatal(err)
	}
	capture, err := loadObservedCapture("archive")
	if err != nil {
		t.Fatal(err)
	}
	if capture.Provenance.Source != "saved" || capture.Provenance.CaptureSessionID != "saved-session" || capture.Provenance.SavedHash != archive.HashID || capture.Export.BrowserSession.DroppedRequests != 2 || capture.Export.CaptureDigest != "digest" {
		t.Fatalf("lost archive identity or metadata: %+v", capture)
	}
	if capture.coverage().Capture.State != "gaps_reported" {
		t.Fatal("archive capture gaps disappeared")
	}
	if _, err := loadObservedCapture("missing"); err == nil {
		t.Fatal("missing archive silently selected another source")
	}
}

func TestObservedCaptureUnreadableAndCapturedEmptyRemainDistinct(t *testing.T) {
	selected := summaryTestScope(t, "coverage", "states")
	writeSummaryCapture(t, selected.LivePath, store.Export{SessionID: "empty-capture"})
	capture, err := loadObservedCapture("")
	if err != nil || capture.Provenance.SourceStatus != "captured" || capture.coverage().Capture.State == "no_capture" {
		t.Fatalf("captured empty lost evidence: %+v, %v", capture, err)
	}
	if err := os.WriteFile(selected.LivePath, []byte("invalid JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadObservedCapture(""); err == nil {
		t.Fatal("corrupt live capture reported as empty")
	}
}

func TestCoverageRequiresDataScope(t *testing.T) {
	if !commandRequiresScope(newCoverageCommand()) {
		t.Fatal("coverage can bypass task selection")
	}
}
