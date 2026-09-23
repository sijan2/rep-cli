package cmd

import (
	"context"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/headless"
	"strings"
)

// Single transport-selection path for capture, control, and semantic workflows.
// It has no output side effects; each command keeps its own error presentation.
func connectBrowser(ctx context.Context, name string) (*bridge.Client, error) {
	if strings.EqualFold(strings.TrimSpace(name), "headless") {
		dir, err := headlessTaskDir()
		if err != nil {
			return nil, err
		}
		return headless.Connection(ctx, dir)
	}
	return bridge.Select(ctx, name)
}
