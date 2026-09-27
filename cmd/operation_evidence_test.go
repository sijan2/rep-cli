package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/evidence"
	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/repplus/rep-cli/internal/scope"
)

func operationEvidenceFixture(t *testing.T) (*evidence.Store, evidence.Run) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	selected := configureCaptureTestScope(t, "operation-evidence")
	store, err := evidence.Open(selected.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.BeginRun("verify the local fixture", "fixture confirmation observed", nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := evidenceRunID
	evidenceRunID = run.ID
	t.Cleanup(func() { evidenceRunID = previous })
	return store, run
}

func TestBrowserEvidenceDisabledDoesNotTouchDisk(t *testing.T) {
	base := filepath.Join(t.TempDir(), "uncreated")
	t.Setenv("XDG_DATA_HOME", base)
	previous := evidenceRunID
	evidenceRunID = ""
	t.Cleanup(func() { evidenceRunID = previous })
	record, err := beginBrowserEvidence("browser.step", func() {}, "arc", 4)
	if err != nil || record != nil {
		t.Fatalf("disabled recorder = %v, %v", record, err)
	}
	record.dispatch()
	record.observation("before", "controls", "fingerprint", nil)
	if err := record.artifact("/missing", "screenshot", "unknown", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := record.finish(func() {}, "completed", "unverified", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(base); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled recording accessed its data directory: %v", err)
	}
}

func TestBrowserEvidenceRetainsPendingAndImmutableArtifacts(t *testing.T) {
	store, run := operationEvidenceFixture(t)
	record, err := beginBrowserEvidence("browser.screenshot", map[string]any{"intent": "inspect the fixture"}, "headless", 4)
	if err != nil {
		t.Fatal(err)
	}
	start, err := store.LoadOperation(run.ID, record.ref().OperationID)
	if err != nil || start.Status != "pending" || start.Phase != "started" {
		t.Fatalf("pending operation missing before dispatch: %+v %v", start, err)
	}
	record.dispatch()
	record.observation("before", "controls", "original-fingerprint", map[string]any{"generation": "fixture-generation"})
	path := filepath.Join(t.TempDir(), "fixture.bin")
	if err := os.WriteFile(path, []byte("original bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := record.artifact(path, "screenshot", "unknown", "viewport coverage unknown", nil); err != nil {
		t.Fatal(err)
	}
	if err := record.finish(map[string]any{"saved": true}, "completed", "unverified", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListOperations(run.ID, "", 20)
	if err != nil || len(page.Operations) != 2 {
		t.Fatalf("operation history %+v %v", page, err)
	}
	finished := page.Operations[1]
	if page.Operations[0].Status != "pending" || finished.ParentID != start.ID || finished.Status != "completed" || finished.Verification != "unverified" || len(finished.ArtifactIDs) != 1 {
		t.Fatalf("recording changed the start or invented verification: %+v", page.Operations)
	}
	artifact, err := store.LoadArtifact(run.ID, finished.ArtifactIDs[0])
	if err != nil || artifact.Coverage.State != "unknown" {
		t.Fatalf("hashing invented complete coverage: %+v %v", artifact, err)
	}
	data, err := store.ReadArtifact(run.ID, artifact.ID, 0, 100)
	if err != nil || string(data.Data) != "original bytes" {
		t.Fatalf("artifact changed with its source: %q %v", data.Data, err)
	}
	if len(finished.References) != 1 || finished.References[0].Metadata["snapshot_retained"] != false {
		t.Fatalf("fingerprint claimed to retain an observation: %+v", finished.References)
	}
}

func TestBrowserEvidenceDispatchedFailureRemainsUnknown(t *testing.T) {
	store, run := operationEvidenceFixture(t)
	record, err := beginBrowserEvidence("browser.step", nil, "headless", 4)
	if err != nil {
		t.Fatal(err)
	}
	record.dispatch()
	returnErr := error(context.DeadlineExceeded)
	record.finishOnReturn(&returnErr)
	if !errors.Is(returnErr, context.DeadlineExceeded) {
		t.Fatalf("original failure lost: %v", returnErr)
	}
	page, err := store.ListOperations(run.ID, "", 20)
	if err != nil || len(page.Operations) != 2 || page.Operations[1].Status != "unknown" || page.Operations[1].Verification != "unknown" {
		t.Fatalf("dispatch failure fabricated a result: %+v %v", page, err)
	}
}

func TestStepEvidenceModelDoneDoesNotVerifyOutcome(t *testing.T) {
	store, run := operationEvidenceFixture(t)
	host := &stepHost{t: t, decision: jevdom.StepDecision{Status: "selected", Operation: jevdom.OpDone, Model: "fixture-model"}}
	out, err := runStepCommand(t, host, "Confirm the fixture", "--tab", "4", "--apply")
	if err != nil {
		t.Fatalf("step: %s %v", out, err)
	}
	page, err := store.ListOperations(run.ID, "", 20)
	if err != nil || len(page.Operations) != 2 {
		t.Fatalf("operations %+v %v", page, err)
	}
	finished := page.Operations[1]
	if finished.ModelClaim != "DONE" || finished.Verification != "unverified" || finished.Metadata["attempted"] != false || !strings.Contains(out, run.ID) {
		t.Fatalf("model DONE became observed success: %+v %s", finished, out)
	}
}

func TestBrowserEvidenceReferencesAreBoundedAndDiscloseOmissions(t *testing.T) {
	store, run := operationEvidenceFixture(t)
	record, err := beginBrowserEvidence("browser.shots", nil, "headless", -1)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 1000; index++ {
		record.observation("before", "controls", "fingerprint", nil)
	}
	if err := record.finish(nil, "completed", "unverified", "", nil); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListOperations(run.ID, "", 20)
	if err != nil || len(page.Operations) != 2 {
		t.Fatal(err)
	}
	finished := page.Operations[1]
	if len(finished.References) != 64 || finished.Metadata["references_omitted"] != float64(936) || finished.Metadata["references_total"] != float64(1000) {
		t.Fatalf("reference omissions were hidden: %+v", finished.Metadata)
	}
}

func TestBrowserEvidenceRejectsGlobalScope(t *testing.T) {
	_, _ = operationEvidenceFixture(t)
	if _, err := scope.Configure(scope.Options{Global: true}); err != nil {
		t.Fatal(err)
	}
	if record, err := beginBrowserEvidence("browser.create", nil, "arc", -1); err == nil || record != nil || !strings.Contains(err.Error(), "explicit workspace and task") {
		t.Fatalf("global run was accepted: %v %v", record, err)
	}
}

func TestBrowserEvidencePinsCaptureArchiveAndDigest(t *testing.T) {
	store, run := operationEvidenceFixture(t)
	record, err := beginBrowserEvidence("browser.open", nil, "arc", 12)
	if err != nil {
		t.Fatal(err)
	}
	fixture := createBrowserSnapshotFixture(t, "capture-operation", "fixture-request")
	handoff, err := prepareBrowserCapture(context.Background(), fixture)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]any{"session_id": "capture-operation", "requests": 1}
	if err := finishBrowserCapture(context.Background(), fixture, result, handoff, "fixture", false); err != nil {
		t.Fatal(err)
	}
	record.capture(result)
	if err := record.finish(captureEvidenceSummary(result), "completed", "unverified", "", nil); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListOperations(run.ID, "", 20)
	if err != nil || len(page.Operations) != 2 || len(page.Operations[1].References) != 1 {
		t.Fatalf("missing capture reference: %+v %v", page, err)
	}
	ref := page.Operations[1].References[0]
	if ref.Kind != "capture_archive" || ref.ID != result["saved_hash_id"] || ref.SHA256 != fixture.reference.SHA256 || ref.Metadata["snapshot_verified"] != true {
		t.Fatalf("capture reference not bound to verified archive: %+v", ref)
	}
}

func TestScreenshotEvidenceIsPendingBeforeRPCAndImportsImage(t *testing.T) {
	store, run := operationEvidenceFixture(t)
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	server := newFakeBrowserBridgeServer(t, func(request bridge.RPCRequest) fakeBrowserBridgeReply {
		page, err := store.ListOperations(run.ID, "", 20)
		if err != nil || len(page.Operations) != 1 || page.Operations[0].Status != "pending" {
			t.Errorf("browser RPC preceded pending evidence: %+v %v", page, err)
			return fakeBrowserBridgeReply{rpcErr: &bridge.RPCError{Code: "fixture_failed", Message: "no pending evidence"}}
		}
		if request.Method != "browser.screenshot" {
			t.Errorf("extra browser observation: %s", request.Method)
		}
		return fakeBrowserBridgeReply{result: map[string]any{"tab_id": 9, "format": "png", "data": base64.StdEncoding.EncodeToString(imageBytes.Bytes())}}
	})
	client := &bridge.Client{Registry: bridge.Registry{Browser: "arc", Socket: server.listener.Addr().String()}}
	result, err := captureScreenshot(context.Background(), client, 9, pageScreenshotFlags{Format: "png"}, "")
	if err != nil || result.Evidence == nil || result.Width != 3 || result.Height != 2 {
		t.Fatalf("screenshot result %+v %v", result, err)
	}
	page, err := store.ListOperations(run.ID, "", 20)
	if err != nil || len(page.Operations) != 2 || len(page.Operations[1].ArtifactIDs) != 1 {
		t.Fatalf("screenshot artifact not recorded: %+v %v", page, err)
	}
	if requests := server.Requests(); len(requests) != 1 {
		t.Fatalf("recording added browser round trips: %+v", requests)
	}
}

func TestBrowserEvidenceFinishFailureDoesNotRetryOrClaimCompletion(t *testing.T) {
	store, run := operationEvidenceFixture(t)
	record, err := beginBrowserEvidence("browser.step", nil, "headless", 4)
	if err != nil {
		t.Fatal(err)
	}
	record.dispatch()
	selected, err := scope.Current()
	if err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(selected.DataDir, "evidence", "v1", "runs", run.ID, "operations.idx")
	backup := index + ".fixture"
	if err := os.Rename(index, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(index, 0700); err != nil {
		t.Fatal(err)
	}
	finishErr := record.finish(map[string]any{"performed": true}, "completed", "unverified", "", nil)
	if finishErr == nil || !strings.Contains(finishErr.Error(), "may already have run") {
		t.Fatalf("lost completion uncertainty: %v", finishErr)
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, index); err != nil {
		t.Fatal(err)
	}
	record.finishOnReturn(&finishErr)
	page, err := store.ListOperations(run.ID, "", 20)
	if err != nil || len(page.Operations) != 1 || page.Operations[0].Status != "pending" {
		t.Fatalf("failed completion was silently retried: %+v %v", page, err)
	}
}
