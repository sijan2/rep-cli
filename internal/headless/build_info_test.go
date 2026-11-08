package headless

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareExtensionPublishesCurrentMetadata(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"manifest.json": `{}`, "background.js": "// first"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "build-info.json")
	for _, stale := range []string{"", `{`, `{"digest":"old"}`} {
		if stale == "" {
			_ = os.Remove(path)
		} else if err := os.WriteFile(path, []byte(stale), 0600); err != nil {
			t.Fatal(err)
		}
		digest, changed, err := PrepareExtension(dir)
		if err != nil || !changed {
			t.Fatalf("repair %q: %v %v", stale, changed, err)
		}
		var info struct{ Digest string }
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, &info) != nil || info.Digest != digest {
			t.Fatalf("worker cannot advertise current source: %s %v", data, err)
		}
		before, _ := os.Stat(path)
		if before.Mode().Perm() != 0600 {
			t.Fatal("metadata is not private")
		}
		if got, changed, err := PrepareExtension(dir); err != nil || changed || got != digest {
			t.Fatalf("idempotent repair: %q %v %v", got, changed, err)
		}
		after, _ := os.Stat(path)
		if !os.SameFile(before, after) {
			t.Fatal("current metadata was rewritten")
		}
	}
	old, _, _ := PrepareExtension(dir)
	if err := os.WriteFile(filepath.Join(dir, "background.js"), []byte("// changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if next, changed, err := PrepareExtension(dir); err != nil || !changed || next == old {
		t.Fatalf("source edit not published: %q %v %v", next, changed, err)
	}
	if err := os.Remove(filepath.Join(dir, "background.js")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, _, err := PrepareExtension(dir); err == nil {
		t.Fatal("missing source should fail")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed repair changed metadata")
	}
}
