package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/repplus/rep-cli/internal/evidence"
	"github.com/repplus/rep-cli/internal/scope"
)

// Recording is opt-in. The nil recorder returns before filesystem access,
// serialization, artifact hashing, or additional browser observations.
type operationEvidenceRef struct {
	RunID       string `json:"run_id"`
	OperationID string `json:"operation_id"`
}

type browserOperationEvidence struct {
	store               *evidence.Store
	runID               string
	operation           evidence.Operation
	attempted           bool
	finished            bool
	completionAttempted bool
	artifacts           []string
	references          []evidence.Reference
	referenceBytes      int
	referencesOmitted   int
}

func beginBrowserEvidence(kind string, input any, browser string, tab int) (*browserOperationEvidence, error) {
	if evidenceRunID == "" {
		return nil, nil
	}
	selected, err := scope.Current()
	if err != nil {
		return nil, err
	}
	if !selected.Scoped {
		return nil, errors.New("--run requires an explicit workspace and task")
	}
	store, err := evidence.Open(selected.DataDir)
	if err != nil {
		return nil, err
	}
	op, err := store.BeginOperation(evidenceRunID, evidence.Operation{
		Kind: kind, Input: compactEvidenceJSON(input),
		Metadata: map[string]any{"collector": "rep-cli", "cli_version": Version, "workspace": selected.Workspace, "task": selected.Task, "browser": browser, "tab_id": tab},
	})
	if err != nil {
		return nil, fmt.Errorf("record pending operation before browser work: %w", err)
	}
	return &browserOperationEvidence{store: store, runID: evidenceRunID, operation: op}, nil
}

func compactEvidenceJSON(value any) json.RawMessage {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{"summary_unavailable":true}`)
	}
	const maximum = 16 * 1024
	if len(data) <= maximum {
		return data
	}
	sum := sha256.Sum256(data)
	summary, _ := json.Marshal(map[string]any{"summary_omitted": true, "serialized_bytes": len(data), "sha256": hex.EncodeToString(sum[:])})
	return summary
}

func (record *browserOperationEvidence) ref() *operationEvidenceRef {
	if record == nil {
		return nil
	}
	return &operationEvidenceRef{RunID: record.runID, OperationID: record.operation.ID}
}

func (record *browserOperationEvidence) dispatch() {
	if record != nil {
		record.attempted = true
	}
}

func (record *browserOperationEvidence) observation(phase, kind, fingerprint string, binding any) {
	if record == nil || fingerprint == "" {
		return
	}
	metadata := map[string]any{"phase": phase, "kind": kind, "representation": "fingerprint_only", "snapshot_retained": false}
	if binding != nil {
		metadata["binding"] = binding
	}
	record.reference(evidence.Reference{Kind: "observation", ID: fingerprint, Metadata: metadata})
}

func (record *browserOperationEvidence) reference(reference evidence.Reference) {
	if record == nil {
		return
	}
	encoded, err := json.Marshal(reference)
	if err != nil || len(record.references) >= 64 || record.referenceBytes+len(encoded) > 20*1024 {
		record.referencesOmitted++
		return
	}
	record.references = append(record.references, reference)
	record.referenceBytes += len(encoded)
}

func (record *browserOperationEvidence) relatedOperation(other *operationEvidenceRef, relationship string) {
	if record == nil || other == nil {
		return
	}
	record.reference(evidence.Reference{Kind: "operation", ID: other.OperationID,
		Metadata: map[string]any{"run_id": other.RunID, "relationship": relationship, "basis": "explicit_cli_call"}})
}

func (record *browserOperationEvidence) capture(result map[string]any) {
	if record == nil {
		return
	}
	archive, _ := result["saved_hash_id"].(string)
	if archive == "" {
		return
	}
	digest, _ := result["capture_sha256"].(string)
	record.reference(evidence.Reference{Kind: "capture_archive", ID: archive, SHA256: digest,
		Metadata: map[string]any{"session_id": result["session_id"], "snapshot_verified": result["capture_snapshot_verified"], "body_capture_states": result["body_capture_states"], "incomplete_bodies": result["incomplete_bodies"]}})
}

func (record *browserOperationEvidence) artifact(path, kind, coverage, reason string, metadata map[string]any) error {
	if record == nil {
		return nil
	}
	artifact, err := record.store.ImportArtifact(record.runID, evidence.ImportSpec{Path: path, Kind: kind, Collector: "rep-cli", Build: Version,
		Coverage: evidence.Coverage{State: coverage, Reason: reason}, Metadata: metadata})
	if err != nil {
		return fmt.Errorf("browser work may already have completed; artifact recording failed (inspect the saved output before retrying): %w", err)
	}
	record.artifacts = append(record.artifacts, artifact.ID)
	return nil
}

func (record *browserOperationEvidence) finish(result any, status, verification, modelClaim string, operationErr error) error {
	if record == nil || record.finished {
		return nil
	}
	if status == "" {
		status = "completed"
	}
	if verification == "" {
		verification = "unverified"
	}
	finished := evidence.Operation{Status: status, Verification: verification, ModelClaim: modelClaim, Result: compactEvidenceJSON(result),
		FinishedAt: time.Now().UTC(), ArtifactIDs: record.artifacts, References: record.references,
		Metadata: map[string]any{"attempted": record.attempted, "duration_ms": time.Since(record.operation.StartedAt).Milliseconds(),
			"references_total": len(record.references) + record.referencesOmitted, "references_omitted": record.referencesOmitted}}
	if operationErr != nil {
		message := operationErr.Error()
		if len(message) > 4096 {
			finished.Metadata["error_bytes"] = len(message)
			finished.Metadata["error_omitted"] = true
			finished.Error = "operation failed; the full error exceeded the evidence record budget"
		} else {
			finished.Error = message
		}
	}
	record.completionAttempted = true
	_, err := record.store.FinishOperation(record.runID, record.operation.ID, finished)
	if err != nil {
		return fmt.Errorf("operation %s may already have run; evidence completion failed (inspect the run and owned tab before retrying): %w", record.operation.ID, err)
	}
	record.finished = true
	return nil
}

// Early returns retain an explicit failure or an unknown outcome. Successful
// paths call finish before emitting success to stdout.
func (record *browserOperationEvidence) finishOnReturn(returnErr *error) {
	if record == nil || record.finished || record.completionAttempted {
		return
	}
	status := "failed"
	if record.attempted {
		status = "unknown"
	}
	cause := *returnErr
	if cause == nil {
		cause = errors.New("operation ended without a recorded outcome")
	}
	if err := record.finish(nil, status, "unknown", "", cause); err != nil {
		*returnErr = errors.Join(*returnErr, err)
	}
}

func captureEvidenceSummary(result map[string]any) map[string]any {
	summary := map[string]any{}
	for _, key := range []string{"session_id", "saved_hash_id", "capture_sha256", "tab_id", "timed_out", "requests", "failed_requests", "body_capture_states", "incomplete_bodies", "capture_snapshot_verified", "duration_ms", "terminal_outcome"} {
		if value, exists := result[key]; exists {
			summary[key] = value
		}
	}
	return summary
}

func attachOperationEvidence(result map[string]any, record *browserOperationEvidence) {
	if record != nil {
		result["evidence"] = record.ref()
	}
}
