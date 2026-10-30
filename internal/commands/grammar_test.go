package commands

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/output"
)

func TestFlagsBeforeVerbFailWithQuotedCorrection(t *testing.T) {
	for _, args := range [][]string{{"--task", "fixture", "scope"}, {"--json", "scope"}, {"--task", "scope", "scope"}, {"--task", "bad'; echo unsafe", "scope"}} {
		err := verbFirst(rootCmd, args)
		var ae output.AgentError
		if !errors.As(err, &ae) || ae.Code != "flag_before_verb" || !strings.HasPrefix(ae.Next, "rep scope ") {
			t.Fatalf("%v: %+v", args, ae)
		}
		stdout, _, code := runEntryPoint(t, args...)
		var decoded struct{ Error output.AgentError }
		if code != 2 || json.Unmarshal([]byte(stdout), &decoded) != nil || decoded.Error.Code != ae.Code || decoded.Error.Next != ae.Next {
			t.Fatalf("code=%d stdout=%s", code, stdout)
		}
	}
	for _, args := range [][]string{{"--help"}, {"--version"}, {"scope", "--task", "fixture"}, {"--help", "scope"}} {
		if err := verbFirst(rootCmd, args); err != nil {
			t.Fatalf("accepted grammar rejected: %v %v", args, err)
		}
	}
}
