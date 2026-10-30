//go:build replab

package commands

import (
	"os"

	"path/filepath"

	"testing"
)

func TestScopedAndroidCannotReachGlobalClearHandler(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "global-android-data.json")
	contents := []byte(`{"requests":["must-survive"]}`)
	if err := os.WriteFile(sentinel, contents, 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := runScopeCLI(t, dir, map[string]string{"REP_SCOPE_TEST_ANDROID_SENTINEL": sentinel}, "android", "clear", "--task", "project/agent")
	requireScopeError(t, raw, err, "scope_unsupported")
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != string(contents) {
		t.Fatalf("scoped command reached shared-data handler: %q %v", data, err)
	}
	for _, args := range [][]string{{"android", "status"}, {"android", "summary"}, {"android", "config", "show"}} {
		raw, err := runScopeCLI(t, dir, nil, append(args, "--task", "project/agent")...)
		requireScopeError(t, raw, err, "scope_unsupported")
	}
}

func TestScopedSharedDeviceFamilyIsRejected(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"ig", "list"}, {"ig", "surface"}, {"ig", "body", "missing-request"}} {
		raw, err := runScopeCLI(t, dir, nil, append(args, "--task", "project/agent")...)
		requireScopeError(t, raw, err, "scope_unsupported")
	}
}
