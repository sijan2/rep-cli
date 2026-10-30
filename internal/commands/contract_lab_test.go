//go:build replab

package commands

import (
	"strings"
	"testing"
)

func TestLabRetainsSpecializedHelp(t *testing.T) {
	out, stderr, code := runEntryPoint(t, "--help")
	if code != 0 || stderr != "" || !strings.Contains(out, "packets") || !strings.Contains(out, "jev") {
		t.Fatalf("specialized discovery missing: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
}
