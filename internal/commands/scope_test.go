package commands

import (
	"encoding/json"

	"github.com/spf13/cobra"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the actual entry point without browser or device side effects.
func TestScopeCLIHelperProcess(t *testing.T) {
	if os.Getenv("REP_SCOPE_CLI_TEST") != "1" {
		return
	}
	if sentinel := os.Getenv("REP_SCOPE_TEST_ANDROID_SENTINEL"); sentinel != "" {
		command, rest, err := rootCmd.Find([]string{"android", "clear"})
		if err != nil || len(rest) != 0 || command.Name() != "clear" {
			t.Fatal("Android command missing")
		}
		command.RunE = func(*cobra.Command, []string) error { return os.Remove(sentinel) }
	}
	for index, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"rep"}, os.Args[index+1:]...)
			rootCmd.SetArgs(os.Args[1:])
			Execute()
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func TestScopeDerivesIdentityOfflineAndSeparatesTasks(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		args         []string
		env          map[string]string
		task, source string
	}{
		{[]string{"scope", "--task", "project/a"}, nil, "project/a", "flag"},
		{[]string{"scope"}, map[string]string{"REP_TASK": "project/b"}, "project/b", "environment"},
		{[]string{"scope"}, map[string]string{"REP_TASK": "global"}, "global", "environment"},
	} {
		raw, err := runScopeCLI(t, dir, tc.env, tc.args...)
		var result struct {
			Task     string   `json:"task"`
			Source   string   `json:"task_source"`
			Tabs     []string `json:"tabs"`
			Captures []any    `json:"captures"`
			Journal  []any    `json:"journal"`
		}
		if err != nil || json.Unmarshal(raw, &result) != nil || result.Task != tc.task || result.Source != tc.source || len(result.Tabs) != 0 || len(result.Captures) != 0 || len(result.Journal) != 0 {
			t.Fatalf("offline scope: %s %v", raw, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "data")); !os.IsNotExist(err) {
		t.Fatalf("scope created data or a browser: %v", err)
	}
}

func TestScopedStoresRejectExternalCaptureOverrides(t *testing.T) {
	dir := t.TempDir()
	raw, err := runScopeCLI(t, dir, map[string]string{"REPLIVE_PATH": filepath.Join(dir, "shared.json")}, "scope", "--task", "project/a")
	requireScopeError(t, raw, err, "invalid_argument")
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
	for _, name := range []string{"REP_WORKSPACE", "REP_TASK", "REPLIVE_PATH", "REPANDROID_PATH", "REP_PROFILE", "XDG_RUNTIME_DIR", "CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "CODEX_SESSION_ID", "REP_TASK_SESSION"} {
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
