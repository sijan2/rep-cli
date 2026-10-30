package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/output"
)

// Run the real entry point, including SilenceErrors and the final reporter, in
// a fresh process. Handlers that could reach a browser are replaced first.
func TestEntryPointHelperProcess(t *testing.T) {
	if os.Getenv("REP_ENTRY_POINT_TEST") != "1" {
		return
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

func runEntryPoint(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	directory := t.TempDir()
	command := exec.Command(os.Args[0], append([]string{"-test.run=^TestEntryPointHelperProcess$", "--"}, args...)...)
	command.Dir = directory
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		switch name {
		case "REP_WORKSPACE", "REP_TASK", "REPLIVE_PATH", "REPANDROID_PATH", "XDG_DATA_HOME", "REP_BRIDGE_DIR":
			continue
		}
		command.Env = append(command.Env, item)
	}
	command.Env = append(command.Env, "REP_ENTRY_POINT_TEST=1", "XDG_DATA_HOME="+filepath.Join(directory, "data"), "REP_BRIDGE_DIR="+filepath.Join(directory, "bridges"))
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String(), code
}

func TestUsageErrorsAreNeverSilent(t *testing.T) {
	for _, args := range [][]string{{"page", "--settle", "2s"}, {"eval"}, {"eval", "1", "--timeout", "bad"}, {"no-such-command"}} {
		stdout, stderr, code := runEntryPoint(t, args...)
		var payload struct{ Error output.AgentError }
		if code != 2 || stderr != "" || json.Unmarshal([]byte(stdout), &payload) != nil || payload.Error.Code != "invalid_argument" {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}
}

func TestCommandErrorsArePrintedExactlyOnce(t *testing.T) {
	stdout, stderr, code := runEntryPoint(t, "body", "h_00000000000000000000000000000000", "--task", "fixture")
	if code != 1 || stderr != "" || !json.Valid([]byte(stdout)) || strings.Count(stdout, `"error":`) != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestJevFailuresKeepTheirClassifiedCause(t *testing.T) {
	cases := []struct {
		err     error
		code    string
		message string
		suggest string
	}{
		{&jev.Error{Code: jev.CodeRateLimited, Message: "Jev API returned HTTP 429: rate limited after 3 attempts; retry after a short wait"}, jev.CodeRateLimited, "HTTP 429", "retry after a short wait"},
		{&bridge.RPCError{Code: jev.CodeInvalidResponse, Message: "Jev returned an invalid typed decision: question \"selection\" probabilities total 0.9000"}, jev.CodeInvalidResponse, "total 0.9000", "pin JEV_MODEL"},
		{fmt.Errorf("select: %w", &jev.Error{Code: jev.CodeNotConfigured, Message: "Jev key unavailable"}), jev.CodeNotConfigured, "Jev key unavailable", "rep-lab jev config"},
		{output.NewAgentError("host_outdated", "", "the connected rep-host predates this rep CLI", "rep browser reload-extension --browser arc"), "host_outdated", "predates", "reload-extension"},
		{errors.New("goal must contain between 1 and 2000 bytes"), output.ErrCodeCommandFailed, "goal must contain", ""},
	}
	for _, c := range cases {
		ae := jevAgentError("rep browser select", c.err)
		if ae.Code != c.code || !strings.Contains(ae.Message, c.message) || ae.Command != "rep browser select" || strings.HasPrefix(ae.Message, c.code+":") {
			t.Fatalf("err=%v ae=%+v", c.err, ae)
		}
		if c.suggest != "" && !strings.Contains(strings.Join(ae.Suggest, "\n"), c.suggest) {
			t.Fatalf("missing next step %q: %+v", c.suggest, ae.Suggest)
		}
	}
}
