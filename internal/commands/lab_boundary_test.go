package commands

import (
	"strings"
	"testing"
)

func TestLabCommandBuildBoundary(t *testing.T) {
	for _, path := range []string{
		"packets", "native-capture", "media", "watch", "android", "ig", "engine", "probe", "audit",
		"locate", "element select", "element select-batch", "element validate", "element observe",
		"step", "state", "jev config", "jev status", "jev doctor", "jev classify", "arc",
	} {
		t.Run(path, func(t *testing.T) {
			args := strings.Fields(path)
			cmd, rest, err := rootCmd.Find(args)
			registered := err == nil && len(rest) == 0 && cmd.Name() == args[len(args)-1]
			if registered != labBuild {
				t.Fatalf("lab command %q registered=%v, replab build=%v", path, registered, labBuild)
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
