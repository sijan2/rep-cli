package commands

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/scope"
)

func TestCoreEvalDerivedSiblingRequiresTargetBeforeBrowserDispatch(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("REP_TASK", "")
	t.Setenv("REP_WORKSPACE", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "synthetic-shared-siblings")
	if _, err := scope.ConfigureInvocation(scope.Options{}, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scope.Reset)
	command := newCoreEvalCommand()
	err := coreEvaluate(command, []string{"document.body.textContent='changed'"}, time.Second, false, false)
	var problem *agentstate.Error
	if !errors.As(err, &problem) || problem.Code != "ambiguous_target" {
		t.Fatalf("bare sibling script reached browser resolution: %v", err)
	}
}

func TestCoreEvalExportCollisionBeforeScriptDispatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.json")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	command := newCoreEvalCommand()
	if err := command.Flags().Set("out", path); err != nil {
		t.Fatal(err)
	}
	err := coreEvaluate(command, []string{"document.body.textContent='changed'", "t1"}, time.Second, false, false)
	var problem *agentstate.Error
	if !errors.As(err, &problem) || problem.Code != "artifact_exists_or_unwritable" {
		t.Fatalf("export collision reached browser resolution: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "original" {
		t.Fatal("existing export changed")
	}
}
