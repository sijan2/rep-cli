package commands

import (
	"strings"
	"testing"
)

func TestLabHelpListsSpecializedCommands(t *testing.T) {
	help := labHelp(labCmd)
	if !strings.Contains(help, "Usage:\n  rep lab") {
		t.Fatal(help)
	}
	for _, name := range []string{"packets", "native-capture", "media", "watch", "android", "ig", "engine", "probe", "audit", "locate", "element", "step", "jev", "arc"} {
		if !strings.Contains(help, "\n  "+name+"  ") {
			t.Fatalf("missing lab command %s", name)
		}
	}
}
