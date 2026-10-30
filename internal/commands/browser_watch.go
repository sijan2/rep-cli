//go:build replab

package commands

import (
	"context"

	"fmt"

	"time"

	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

func runBrowserWatch(cmd *cobra.Command, action string) error {
	selected, err := scope.Current()
	if err != nil {
		return err
	}
	if selected.Scoped {
		return emitBrowserArgumentError("browser watch "+action, fmt.Errorf("ambient capture covers all tabs and is unavailable in a task scope; use explicit tab captures"))
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
	defer cancel()
	client, err := selectBrowserBridge(ctx, browserSelector, "browser watch "+action)
	if err != nil {
		return err
	}
	var result map[string]interface{}
	if err := client.Call(ctx, "browser.watch."+action, nil, &result); err != nil {
		return emitBrowserCallError("browser watch "+action, err)
	}
	return emitBrowserResult(result, func() {
		fmt.Printf("ambient capture: %v\n", result["watching"])
		if session := result["session_id"]; session != nil {
			fmt.Printf("session: %v\n", session)
		}
	})
}
