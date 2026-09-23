package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Run the real root command in a fresh process so Cobra globals, scope selection
// and the store cache cannot accidentally make isolation tests pass.
func TestScopeCLIHelperProcess(t *testing.T) {
	if os.Getenv("REP_SCOPE_CLI_TEST") != "1" {
		return
	}
	actionCommand, _, err := rootCmd.Find([]string{"browser", "action"})
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []*cobra.Command{browserOpenCmd, actionCommand, browseCmd, authCmd, setupCmd} {
		// Guard regressions must fail locally instead of invoking a browser or
		// reading/writing existing shared credential exports.
		command.RunE = func(cmd *cobra.Command, args []string) error { return os.ErrPermission }
	}
	if sentinel := os.Getenv("REP_SCOPE_TEST_ANDROID_SENTINEL"); sentinel != "" {
		// Never invoke the real destructive handler, even if this regression
		// test catches a future missing guard. Only a temporary fixture is used.
		androidClearCmd.RunE = func(cmd *cobra.Command, args []string) error { return os.Remove(sentinel) }
	}
	for index, arg := range os.Args {
		if arg == "--" {
			rootCmd.SetArgs(os.Args[index+1:])
			if err := rootCmd.Execute(); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func TestScopedAndroidCannotReachGlobalClearHandler(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "global-android-data.json")
	contents := []byte(`{"requests":["must-survive"]}`)
	if err := os.WriteFile(sentinel, contents, 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := runScopeCLI(t, dir, map[string]string{"REP_SCOPE_TEST_ANDROID_SENTINEL": sentinel}, "--workspace", "project", "--task", "agent", "android", "clear", "-j")
	requireScopeError(t, raw, err, "scope_unsupported")
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != string(contents) {
		t.Fatalf("scoped command reached shared-data handler: %q %v", data, err)
	}
	for _, args := range [][]string{{"android", "status"}, {"android", "summary"}, {"android", "config", "show"}} {
		raw, err := runScopeCLI(t, dir, nil, append([]string{"--workspace", "project", "--task", "agent", "-j"}, args...)...)
		requireScopeError(t, raw, err, "scope_unsupported")
	}
}

func TestScopedSharedDeviceFamilyIsRejected(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"ig", "list"}, {"ig", "surface"}, {"ig", "body", "missing-request"}} {
		raw, err := runScopeCLI(t, dir, nil, append([]string{"--workspace", "project", "--task", "agent", "-j"}, args...)...)
		requireScopeError(t, raw, err, "scope_unsupported")
	}
}

func TestScopedSharedCredentialExportsAreRejected(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"auth"}, {"auth", "--save"}, {"setup", "example.test"}} {
		raw, err := runScopeCLI(t, dir, nil, append([]string{"--workspace", "project", "--task", "agent", "-j"}, args...)...)
		requireScopeError(t, raw, err, "scope_unsupported")
	}
}

func runScopeCLI(t *testing.T, directory string, extra map[string]string, args ...string) ([]byte, error) {
	t.Helper()
	command := exec.Command(os.Args[0], append([]string{"-test.run=^TestScopeCLIHelperProcess$", "--"}, args...)...)
	command.Dir = directory
	env := map[string]string{}
	for _, item := range os.Environ() {
		name, value, ok := strings.Cut(item, "=")
		if ok {
			env[name] = value
		}
	}
	for _, name := range []string{"REP_WORKSPACE", "REP_TASK", "REPLIVE_PATH", "REPANDROID_PATH"} {
		delete(env, name)
	}
	env["REP_SCOPE_CLI_TEST"] = "1"
	env["XDG_DATA_HOME"] = filepath.Join(directory, "data")
	for name, value := range extra {
		env[name] = value
	}
	for name, value := range env {
		command.Env = append(command.Env, name+"="+value)
	}
	return command.CombinedOutput()
}

func requireScopeError(t *testing.T, raw []byte, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, command succeeded: %s", code, raw)
	}
	var result struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Error.Code != code {
		t.Fatalf("wanted structured %s: %s (%v)", code, raw, err)
	}
}

func TestRootScopeGuardBeforeReadingOrCapturing(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"summary"}, {"context"}, {"list"}, {"body", "some-request"}, {"primary"}, {"sessions"}, {"browser", "open", "https://example.test"}, {"browser", "action", "1+1", "--tab", "123"}, {"browse", "https://example.test"}, {"jev", "classify", "some-request"}} {
		raw, err := runScopeCLI(t, dir, nil, append(args, "-j")...)
		requireScopeError(t, raw, err, "scope_required")
	}
	for _, args := range [][]string{{"scope", "-j"}, {"primary", "--help"}, {"describe", "summary"}} {
		if raw, err := runScopeCLI(t, dir, nil, args...); err != nil {
			t.Fatalf("discovery/help unexpectedly guarded %v: %s %v", args, raw, err)
		}
	}
}

func TestRootScopeRequiresExplicitTaskAndAllowsGlobalOptOut(t *testing.T) {
	dir := t.TempDir()
	legacyDir := filepath.Join(dir, "data", "rep-cli")
	if err := os.MkdirAll(legacyDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "store.json"), []byte(`{"primary_domains":{"legacy.example":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := runScopeCLI(t, dir, nil, "--workspace", "project", "primary", "-j")
	requireScopeError(t, raw, err, "task_required")
	raw, err = runScopeCLI(t, dir, nil, "--workspace", "project", "--task", "agent-a", "primary", "-j")
	if err != nil || strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("scoped empty read saw legacy data: %s %v", raw, err)
	}
	raw, err = runScopeCLI(t, dir, map[string]string{"REP_WORKSPACE": "project", "REP_TASK": "agent-a"}, "--global", "primary", "-j")
	if err != nil || !strings.Contains(string(raw), "legacy.example") {
		t.Fatalf("explicit global could not read legacy data: %s %v", raw, err)
	}
	raw, err = runScopeCLI(t, dir, nil, "--global", "--workspace", "project", "primary", "-j")
	requireScopeError(t, raw, err, "invalid_argument")
}

func TestRootProjectBindingAndTaskIsolation(t *testing.T) {
	dir := t.TempDir()
	if raw, err := runScopeCLI(t, dir, nil, "workspace", "init", "project", "-j"); err != nil {
		t.Fatalf("project init: %s %v", raw, err)
	}
	raw, err := runScopeCLI(t, dir, nil, "primary", "-j")
	requireScopeError(t, raw, err, "task_required")
	if raw, err := runScopeCLI(t, dir, map[string]string{"REP_TASK": "agent-a"}, "primary", "a.example", "-j"); err != nil {
		t.Fatalf("agent a set primary: %s %v", raw, err)
	}
	raw, err = runScopeCLI(t, dir, map[string]string{"REP_TASK": "agent-b"}, "primary", "-j")
	if err != nil || strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("agent b saw agent a primary: %s %v", raw, err)
	}
	raw, err = runScopeCLI(t, dir, map[string]string{"REP_TASK": "agent-a"}, "primary", "-j")
	if err != nil || !strings.Contains(string(raw), "a.example") {
		t.Fatalf("agent a lost settings: %s %v", raw, err)
	}
	raw, err = runScopeCLI(t, dir, map[string]string{"REP_TASK": "agent-a", "REPLIVE_PATH": "/tmp/shared-capture.json"}, "primary", "-j")
	requireScopeError(t, raw, err, "invalid_argument")
}
