package commands

import (
	"strings"
	"testing"
)

func TestLabRetainsSpecializedHelp(t *testing.T) {
	out, stderr, code := runEntryPoint(t, "lab", "--help")
	if code != 0 || stderr != "" || !strings.Contains(out, "packets") || !strings.Contains(out, "jev") {
		t.Fatalf("specialized discovery missing: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	root, stderr, code := runEntryPoint(t, "--help")
	if code != 0 || stderr != "" || strings.Contains(root, "packets") || !strings.Contains(root, "lab COMMAND") {
		t.Fatalf("the root contract must stay compact: exit %d, stdout %q, stderr %q", code, root, stderr)
	}
	unknown, stderr, code := runEntryPoint(t, "lab", "nonsense")
	if code == 0 || stderr != "" || !strings.Contains(unknown, `"code":"invalid_argument"`) {
		t.Fatalf("unknown lab command: exit %d, stdout %q, stderr %q", code, unknown, stderr)
	}
}
