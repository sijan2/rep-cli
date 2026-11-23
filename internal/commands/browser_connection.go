package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/headless"
	"github.com/repplus/rep-cli/internal/scope"
)

// Single transport-selection path for capture, control, and semantic workflows.
// It has no output side effects; each command keeps its own error presentation.
func connectBrowser(ctx context.Context, name string) (*bridge.Client, error) {
	if tab, ok := ctx.Value(labTabContextKey{}).(*agentstate.Tab); ok {
		index, err := coreStore()
		if err != nil {
			return nil, err
		}
		client, _, err := resolveCoreBrowser(ctx, index, tab)
		return client, err
	}
	if strings.EqualFold(strings.TrimSpace(name), "headless") {
		dir, err := headlessTaskDir()
		if err != nil {
			return nil, err
		}
		return headless.Connection(ctx, dir)
	}
	return bridge.Select(ctx, name)
}

func headlessTaskDir() (string, error) {
	selected, err := scope.Current()
	if err != nil {
		return "", err
	}
	if !selected.Scoped || selected.TaskSource == "default" {
		return "", fmt.Errorf("headless browser requires an isolated task")
	}
	return headless.NamedProfileDir("")
}
