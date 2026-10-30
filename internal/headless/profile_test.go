package headless

import (
	"path/filepath"
	"testing"
)

func TestNamedProfileSurvivesAgentSessionAndRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	t.Setenv("REP_PROFILE", "")
	first, err := NamedProfileDir("")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_THREAD_ID", "synthetic-new-session")
	t.Setenv("REP_TASK", "different-task")
	second, err := NamedProfileDir("")
	if err != nil || first != second || first != filepath.Join(root, "rep-cli", "profiles", "default") {
		t.Fatalf("unstable profile %s %s %v", first, second, err)
	}
	t.Setenv("REP_PROFILE", "fixture-account")
	third, err := NamedProfileDir("")
	if err != nil || third == first {
		t.Fatalf("name ignored: %s %v", third, err)
	}
	if _, err := NamedProfileDir("../escape"); err == nil {
		t.Fatal("profile escaped storage root")
	}
}
