package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"
)

func TestCaptureEvidencePreservesUncertainActionVerification(t *testing.T) {
	for _, failed := range []bool{false, true} {
		recording := &coreRecording{result: map[string]any{"id": "s_fixture", "requests": []map[string]any{}}}
		if failed {
			recording.err = errors.New("synthetic disconnected bridge")
		}
		recording.once.Do(func() {})
		cmd := &cobra.Command{}
		cmd.SetContext(context.WithValue(context.Background(), coreCaptureContext{}, recording))
		result := coreResult{Error: &coreError{Code: "outcome_unknown", Next: "rep page t1", Retry: "do-not-retry"}}
		attachCoreCapture(cmd, &result)
		if result.Next != "" || result.Error.Next != "rep page t1" || result.Error.Retry != "do-not-retry" {
			t.Fatalf("capture replaced verification: %+v", result)
		}
	}
}
