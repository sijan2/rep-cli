package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/evidence"
	"github.com/repplus/rep-cli/internal/scope"
)

func runEvidenceCommand(t *testing.T, s *evidence.Store, args ...string) (string, error) {
	t.Helper()
	command := newEvidenceCommand(func() (*evidence.Store, map[string]string, error) {
		return s, map[string]string{"workspace": "fixture", "task": "evidence-test"}, nil
	})
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(args)
	err := command.Execute()
	return stdout.String(), err
}

func TestEvidenceCommandRoundTrip(t *testing.T) {
	s, err := evidence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out, err := runEvidenceCommand(t, s, "begin", "--intent", "Inspect local fixture", "--stop", "Attach only")
	if err != nil {
		t.Fatal(err)
	}
	var run evidence.Run
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		t.Fatal(err)
	}
	if run.Stop != "Attach only" || run.Identity["task"] != "evidence-test" {
		t.Fatalf("bad run: %+v", run)
	}
	source := filepath.Join(t.TempDir(), "trace.bin")
	if err := os.WriteFile(source, []byte{0, 255, 65}, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(t.TempDir(), "collection.json")
	if err := os.WriteFile(manifest, []byte(`{"collector":"fixture","trial":"failed-1","coverage":{"state":"partial","reason":"interrupted"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = runEvidenceCommand(t, s, "import", run.ID, source, "--manifest", manifest)
	if err != nil {
		t.Fatal(err)
	}
	var a evidence.Artifact
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatal(err)
	}
	if a.Trial != "failed-1" || a.Coverage.State != "partial" {
		t.Fatalf("metadata lost: %+v", a)
	}
	out, err = runEvidenceCommand(t, s, "read", run.ID, a.ID, "--offset", "1", "--length", "1")
	if err != nil {
		t.Fatal(err)
	}
	var data evidence.ArtifactBytes
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data.Data, []byte{255}) || data.NextOffset != 2 {
		t.Fatalf("byte read incorrect: %+v", data)
	}
	out, err = runEvidenceCommand(t, s, "verify", run.ID, a.ID)
	if err != nil || !strings.Contains(out, `"byte_integrity":"verified"`) || !strings.Contains(out, "not_established_by_hash") {
		t.Fatalf("integrity conflated with coverage: %s %v", out, err)
	}
	if err := os.WriteFile(manifest, []byte(`{"kind":"fixture","status":"failed","error":"collector stopped"}`), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = runEvidenceCommand(t, s, "record", run.ID, "--manifest", manifest)
	if err != nil || !strings.Contains(out, `"status":"failed"`) {
		t.Fatalf("failed trial missing: %s %v", out, err)
	}
}

func TestEvidenceCommandsRejectInvalidManifestsAndScopeOverride(t *testing.T) {
	s, err := evidence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "manifest.json")
	for _, data := range []string{`{"intent":"x","unknown_field":true}`, `null`, `[]`, `{"intent":"x"} {"intent":"y"}`, `{"intent":"x","identity":{"task":"other"}}`} {
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := runEvidenceCommand(t, s, "begin", "--manifest", file); err == nil {
			t.Fatalf("accepted manifest %s", data)
		}
	}
	if _, err := runEvidenceCommand(t, s, "begin"); err == nil {
		t.Fatal("accepted missing intent")
	}
	if _, err := runEvidenceCommand(t, s, "record", "missing-run"); err == nil {
		t.Fatal("accepted missing operation manifest")
	}
}

func TestEvidenceExplicitTaskRequired(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("REP_WORKSPACE", "")
	t.Setenv("REP_TASK", "")
	defer scope.Reset()
	for _, opts := range []scope.Options{{Global: true}, {Workspace: "fixture"}} {
		selected, err := scope.Configure(opts)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := openScopedEvidence(); err == nil {
			t.Fatalf("accepted scope %+v", selected)
		}
		if _, err := os.Stat(filepath.Join(selected.DataDir, "evidence")); !os.IsNotExist(err) {
			t.Fatal("invalid scope created evidence files")
		}
	}
}

func TestEvidenceCompareDoesNotClaimWholeRunEqualityForPartialPages(t *testing.T) {
	s, err := evidence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	left, err := s.BeginRun("same", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	right, err := s.BeginRun("same", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "trace")
	if err := os.WriteFile(source, []byte("same bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		for _, run := range []evidence.Run{left, right} {
			if _, err := s.ImportArtifact(run.ID, evidence.ImportSpec{Path: source}); err != nil {
				t.Fatal(err)
			}
		}
	}
	out, err := runEvidenceCommand(t, s, "compare", left.ID, right.ID, "--limit", "1")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		CoversAll      bool                   `json:"covers_all_artifact_manifests"`
		Shared         []artifactContentMatch `json:"shared_content"`
		Interpretation string                 `json:"interpretation"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.CoversAll || len(result.Shared) != 1 || !strings.Contains(result.Interpretation, "does not establish") {
		t.Fatalf("partial comparison overclaimed: %s", out)
	}
}
