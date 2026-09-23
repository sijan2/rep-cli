package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/repplus/rep-cli/internal/scope"
)

func TestBodyArtifactsArePrivateUniqueAndTaskScoped(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Cleanup(scope.Reset)
	var firstPath string
	for _, task := range []string{"agent-a", "agent-b"} {
		selected, err := scope.Configure(scope.Options{Workspace: "project", Task: task})
		if err != nil {
			t.Fatal(err)
		}
		path, err := saveBodyArtifact("same-request-id", "body for "+task, ".json")
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(path) != filepath.Join(selected.DataDir, "body-exports") || path == firstPath {
			t.Fatalf("artifact outside task or colliding: %s", path)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("artifact permissions: %v %v", info, err)
		}
		dirInfo, err := os.Stat(filepath.Dir(path))
		if err != nil || dirInfo.Mode().Perm() != 0700 {
			t.Fatalf("export directory permissions: %v %v", dirInfo, err)
		}
		again, err := saveBodyArtifact("same-request-id", "later content", ".json")
		if err != nil || again == path {
			t.Fatalf("repeat save replaced artifact: %s %v", again, err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "body for "+task {
			t.Fatalf("original artifact changed: %q %v", data, err)
		}
		if firstPath == "" {
			firstPath = path
		}
	}
	data, err := os.ReadFile(firstPath)
	if err != nil || string(data) != "body for agent-a" {
		t.Fatalf("other task replaced agent a artifact: %q %v", data, err)
	}
}

func TestScopedBodyArtifactRejectsSharedDirectorySymlink(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Cleanup(scope.Reset)
	selected, err := scope.Configure(scope.Options{Workspace: "project", Task: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(selected.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(selected.DataDir, "body-exports")); err != nil {
		t.Fatal(err)
	}
	if _, err := saveBodyArtifact("request", "body", ".txt"); err == nil {
		t.Fatal("artifact was written through a shared-directory symlink")
	}
}
