package commands

import (
	"strings"
	"testing"
)

func TestAndroidLegacyBudgetIsTransparentNoOp(t *testing.T) {
	baseline, stderr, code := runEntryPoint(t, "lab", "android", "--task", "global")
	if code != 0 || stderr != "" {
		t.Fatalf("baseline code=%d stdout=%q stderr=%q", code, baseline, stderr)
	}
	legacy, stderr, code := runEntryPoint(t, "lab", "android", "--task", "global", "--budget", "full")
	if code != 0 || legacy != baseline || !strings.Contains(stderr, "--budget has no effect") {
		t.Fatalf("legacy code=%d stdout=%q stderr=%q", code, legacy, stderr)
	}
}
