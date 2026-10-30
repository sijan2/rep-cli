package commands

import (
	"errors"
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
