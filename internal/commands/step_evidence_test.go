package commands

import (
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/jevdom"
)

func TestStepEvidenceModelDoneDoesNotVerifyOutcome(t *testing.T) {
	store, run := operationEvidenceFixture(t)
	host := &stepHost{t: t, decision: jevdom.StepDecision{Status: "selected", Operation: jevdom.OpDone, Model: "fixture-model"}}
	out, err := runStepCommand(t, host, "Confirm the fixture", "--tab", "4", "--apply")
	if err != nil {
		t.Fatalf("step: %s %v", out, err)
	}
	page, err := store.ListOperations(run.ID, "", 20)
	if err != nil || len(page.Operations) != 2 {
		t.Fatalf("operations %+v %v", page, err)
	}
	finished := page.Operations[1]
	if finished.ModelClaim != "DONE" || finished.Verification != "unverified" || finished.Metadata["attempted"] != false || !strings.Contains(out, run.ID) {
		t.Fatalf("model DONE became observed success: %+v %s", finished, out)
	}
}
