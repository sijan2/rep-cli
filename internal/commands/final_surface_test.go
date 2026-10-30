package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestFinalTreeHasNoHiddenCommandsFlagsOrAliases(t *testing.T) {
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		if command.Hidden || len(command.Aliases) > 0 {
			t.Errorf("hidden command or alias: %s", command.CommandPath())
		}
		check := func(flag *pflag.Flag) {
			if flag.Hidden {
				t.Errorf("hidden flag: %s --%s", command.CommandPath(), flag.Name)
			}
		}
		command.LocalFlags().VisitAll(check)
		command.InheritedFlags().VisitAll(check)
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(rootCmd)
}

func TestRemovedPathsAndShimsFailBeforeWork(t *testing.T) {
	paths := []string{"read", "list", "summary", "context", "detail", "get", "stream", "auth", "setup", "curl", "browse", "identity", "workspace", "gc", "evidence", "describe", "agent-prompt", "completion", "__complete", "__completeNoDesc", "version"}
	for _, child := range []string{"page", "do", "eval", "chat", "reference", "open", "fetch", "action", "capture", "headless", "status", "ping", "doctor", "install", "reload-extension", "targets", "attach", "detach", "cdp", "tabs", "close", "create", "navigate", "screenshot", "shots", "download", "locate", "element", "step", "native-capture", "watch", "state", "runtime", "interact", "select", "select-batch", "observe", "validate"} {
		paths = append(paths, "browser "+child)
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			stdout, stderr, code := runEntryPoint(t, strings.Fields(path)...)
			var result struct {
				Error struct {
					Code  string `json:"code"`
					Next  string `json:"next"`
					Retry string `json:"retry"`
				} `json:"error"`
			}
			if code != 2 || stderr != "" || json.Unmarshal([]byte(stdout), &result) != nil || result.Error.Code != "invalid_argument" || result.Error.Next == "" || result.Error.Retry != "needs-agent" || strings.Contains(stdout, `"suggest"`) {
				t.Fatalf("removed path or incomplete error contract: %s => %d %s %s", path, code, stdout, stderr)
			}
		})
	}
	for _, flag := range []string{"--raw-json", "-j", "--json", "-o", "--output", "--envelope", "--no-color", "--workspace", "--global", "--run"} {
		stdout, stderr, code := runEntryPoint(t, "scope", flag)
		if code != 2 || stderr != "" || !strings.Contains(stdout, `"code":"invalid_argument"`) {
			t.Fatalf("accepted removed root flag %s: %s", flag, stdout)
		}
	}
	for _, flag := range []string{"--saved", "--info", "--require-complete", "--save", "--head", "--pointer", "--format", "--records", "--record-offset", "--offset", "--max-bytes"} {
		stdout, _, code := runEntryPoint(t, "body", "h_fixture", flag)
		if code != 2 || !strings.Contains(stdout, "unknown flag") {
			t.Fatalf("accepted removed body flag %s: %s", flag, stdout)
		}
	}
}
