package commands

import (
	"os"
	"path/filepath"

	"testing"

	"github.com/repplus/rep-cli/internal/scope"
)

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
