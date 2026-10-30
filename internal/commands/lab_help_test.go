//go:build replab

package commands

import (
	"strings"
	"testing"
)

func TestLabHelpShowsExperimentalAndCoreCommands(t *testing.T) {
	help := labHelp(rootCmd)
	if !strings.HasPrefix(help, "Usage:\n  rep-lab") {
		t.Fatal(help)
	}
	for _, name := range []string{"packets", "native-capture", "media", "watch", "android", "ig", "engine", "probe", "audit", "locate", "element", "step", "jev", "arc", "reference", "cdp"} {
		if !strings.Contains(help, "\n  "+name+"  ") {
			t.Fatalf("missing lab command %s", name)
		}
	}
}
