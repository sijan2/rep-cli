package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

func TestRootHelpContinuationNeverAddsAPreVerbTaskFlag(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	_, err := scope.ConfigureInvocation(scope.Options{Task: "fixture/continuation", TaskSet: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scope.Reset)
	var stdout bytes.Buffer
	command := &cobra.Command{Use: "eval"}
	command.SetContext(context.Background())
	command.SetOut(&stdout)
	if err := coreFailure(command, coreArgument("invalid synthetic input")); err == nil {
		t.Fatal("expected a reported usage error")
	}
	var result coreResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Error == nil || result.Error.Next != "rep --help" || coreNext("--help") != "rep --help" {
		t.Fatalf("root discovery must be directly executable: %s", stdout.String())
	}
	if got := coreNext("doctor"); got != "rep doctor --task fixture/continuation" {
		t.Fatalf("repair continuation lost the explicit task: %s", got)
	}
}
