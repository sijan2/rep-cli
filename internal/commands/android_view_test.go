//go:build replab

package commands

import (
	"strings"
	"testing"
)

func TestAndroidLegacyBudgetIsTransparentNoOp(t *testing.T) {
	baseline, stderr, code := runEntryPoint(t, "android", "--global", "-j")
	if code != 0 || stderr != "" {
		t.Fatalf("baseline code=%d stdout=%q stderr=%q", code, baseline, stderr)
	}
	legacy, stderr, code := runEntryPoint(t, "android", "--global", "--budget", "full", "-j")
	if code != 0 || legacy != baseline || !strings.Contains(stderr, "--budget has no effect") {
		t.Fatalf("legacy code=%d stdout=%q stderr=%q", code, legacy, stderr)
	}
}
