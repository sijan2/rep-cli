package cmd

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
