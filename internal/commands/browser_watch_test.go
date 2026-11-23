package commands

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestScopedAmbientCaptureRejectedBeforeBridgeUse(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	configureCaptureTestScope(t, "ambient")
	if err := runBrowserWatch(&cobra.Command{}, "start"); err == nil {
		t.Fatal("scoped ambient all-tab capture was accepted")
	}
}
