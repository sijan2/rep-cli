package contextview

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckpointPermissionsImmutabilityExpirationAndCapacity(t *testing.T) {
	dir := t.TempDir()
	cache, err := openCache(filepath.Join(dir, "cursors"))
	if err != nil {
		t.Fatal(err)
	}
	value := checkpoint{Version: 1, Scope: digestString("scope"), Source: digestString("source"), Groups: map[string]string{}}
	cursor, err := cache.save(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cache.dir, cursor+".json")
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("checkpoint permission %o", info.Mode().Perm())
	}
	dirInfo, _ := os.Stat(cache.dir)
	if dirInfo.Mode().Perm() != 0700 {
		t.Fatalf("cache directory permission %o", dirInfo.Mode().Perm())
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	again, err := cache.save(value)
	if err != nil || again != cursor {
		t.Fatalf("checkpoint address changed: %s %v", again, err)
	}
	info, _ = os.Stat(path)
	if !info.ModTime().Equal(oldTime) {
		t.Fatal("existing immutable checkpoint was rewritten")
	}
	expired := time.Now().Add(-checkpointLifetime - time.Minute)
	_ = os.Chtimes(path, expired, expired)
	if _, err := cache.load(cursor, value.Scope, value.Source); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected explicit expiry: %v", err)
	}
	for i := 0; i < maxCheckpoints+10; i++ {
		value.Groups = map[string]string{"g1_" + digestString(fmt.Sprintf("group-%d", i))[:32]: digestString(fmt.Sprintf("body-%d", i))}
		if _, err := cache.save(value); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(cache.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != maxCheckpoints {
		t.Fatalf("cache has %d entries, want %d", len(entries), maxCheckpoints)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expired checkpoint was not pruned")
	}
}

func TestRejectsTamperedCheckpoint(t *testing.T) {
	cacheDir := t.TempDir()
	input := testInput(testRequest("r1", "/api/help", 1))
	first, _ := buildView(t, input, Options{CacheDir: cacheDir})
	path := filepath.Join(cacheDir, first.Cursor+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"groups":{`, `"groups":{"g1_00000000000000000000000000000000":"`+strings.Repeat("0", 64)+`",`, 1))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(input, Options{CacheDir: cacheDir, Since: first.Cursor}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected content-address validation error: %v", err)
	}
}

func TestRejectsSymlinkCacheAndCheckpoint(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := openCache(link); err == nil {
		t.Fatal("cache symlink should be rejected")
	}
	cache, err := openCache(target)
	if err != nil {
		t.Fatal(err)
	}
	cursor := "c1_" + strings.Repeat("0", 64)
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(target, cursor+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.load(cursor, "scope", "source"); err == nil {
		t.Fatal("checkpoint symlink should be rejected")
	}
}
