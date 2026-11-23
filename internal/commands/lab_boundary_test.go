package commands

import (
	"strings"
	"testing"
)

// Specialized tools live under the one binary's lab group, never at the root.
func TestLabCommandBoundary(t *testing.T) {
	for _, path := range []string{
		"packets", "native-capture", "media", "watch", "android", "ig", "engine", "probe", "audit",
		"locate", "element select", "element select-batch", "element validate", "element observe",
		"step", "state", "jev config", "jev status", "jev doctor", "jev classify", "arc",
	} {
		t.Run(path, func(t *testing.T) {
			args := strings.Fields(path)
			cmd, rest, err := rootCmd.Find(append([]string{"lab"}, args...))
			if err != nil || len(rest) != 0 || cmd.Name() != args[len(args)-1] {
				t.Fatalf("lab command %q is not under rep lab: %v", path, err)
			}
			if direct, _, err := rootCmd.Find(args[:1]); err == nil && direct != rootCmd {
				t.Fatalf("lab command %q leaked into the root", path)
			}
		})
	}
	for _, path := range []string{"page", "do", "eval", "reference", "cdp"} {
		cmd, rest, err := rootCmd.Find([]string{path})
		if err != nil || len(rest) != 0 || cmd.Name() != path {
			t.Fatalf("core command %q was lost: %v", path, err)
		}
	}
}
