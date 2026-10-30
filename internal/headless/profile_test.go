package headless

import (
	"os"
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

func TestConfiguredExtensionDoesNotDependOnWorkingDirectory(t *testing.T) {
	t.Setenv("REP_EXTENSION_PATH", "")
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	dir := filepath.Join(config, "rep")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "extension")
	_ = os.WriteFile(filepath.Join(dir, "extension-path"), []byte(source+"\n"), 0600)
	if got := ConfiguredExtension(); got != source {
		t.Fatalf("config source: %q", got)
	}
	t.Setenv("REP_EXTENSION_PATH", "/synthetic/override")
	if ConfiguredExtension() != "/synthetic/override" {
		t.Fatal("environment did not override installation")
	}
	t.Setenv("REP_EXTENSION_PATH", "")
	_ = os.WriteFile(filepath.Join(dir, "extension-path"), []byte("relative"), 0600)
	if ConfiguredExtension() != "" {
		t.Fatal("accepted cwd-relative installation source")
	}
}
