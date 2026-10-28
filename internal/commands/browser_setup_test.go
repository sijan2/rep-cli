package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeriveExtensionID(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"manifest_version":3}`), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := deriveExtensionID(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := deriveExtensionID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 32 {
		t.Fatalf("extension ID is not stable: %q %q", first, second)
	}
	for _, char := range first {
		if char < 'a' || char > 'p' {
			t.Fatalf("invalid extension ID character %q in %q", char, first)
		}
	}
}

func TestDiaNativeManifestPath(t *testing.T) {
	paths, err := nativeManifestPaths("dia")
	if err != nil {
		t.Fatal(err)
	}
	chrome, err := nativeManifestPaths("chrome")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != chrome[0] {
		t.Fatalf("Dia should use Chrome's native host manifest, got %v", paths)
	}
}

func TestMergeAllowedOriginsKeepsOtherBrowsers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "com.repplus.host.json")
	existing := `{"name":"com.repplus.host","type":"stdio","allowed_origins":["chrome-extension://old/","chrome-extension://new/"]}`
	if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}
	got := mergeAllowedOrigins("chrome-extension://new/", path)
	if len(got) != 2 || got[0] != "chrome-extension://new/" || got[1] != "chrome-extension://old/" {
		t.Fatalf("unexpected merged origins: %v", got)
	}
	if got := mergeAllowedOrigins("chrome-extension://new/", filepath.Join(t.TempDir(), "missing.json")); len(got) != 1 {
		t.Fatalf("missing manifest should yield only the new origin: %v", got)
	}
}

func TestDeriveExtensionIDMatchesRepPath(t *testing.T) {
	path := "/Users/sijan/code/projects/rep"
	if _, err := os.Stat(path); err != nil {
		t.Skip("local rep checkout unavailable")
	}
	id, err := deriveExtensionID(path)
	if err != nil {
		t.Fatal(err)
	}
	if id != "lcchcmkaicodllaolghmndhkkhhodjih" {
		t.Fatalf("got %s", id)
	}
}
