package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var browserCreateActive bool

var browserCreateCmd = &cobra.Command{
	Use:   "create [url]",
	Short: "Create an inactive real-profile tab without starting a capture",
	Long: `Create a task-owned Chromium tab through rep+ without navigating an
existing user tab or requiring Arc's browser-process debugging port. The
default URL is about:blank; HTTP(S) URLs are also accepted. Close the returned
tab ID with 'rep browser close' when finished.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (returnErr error) {
		url := "about:blank"
		if len(args) == 1 {
			url = args[0]
		}
		record, err := beginBrowserEvidence("browser.create", map[string]any{"intent": "create an owned browser tab", "active": browserCreateActive}, browserSelector, -1)
		if err != nil {
			return emitBrowserCallError("browser create", err)
		}
		defer record.finishOnReturn(&returnErr)
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		defer cancel()
		client, err := selectBrowserBridge(ctx, browserSelector, "browser create")
		if err != nil {
			return err
		}
		var result map[string]interface{}
		record.dispatch()
		if err := client.Call(ctx, "browser.create", map[string]interface{}{
			"url": url, "active": browserCreateActive,
		}, &result); err != nil {
			return emitBrowserCallError("browser create", err)
		}
		if err := record.finish(map[string]any{"tab_id": result["tab_id"], "created": true}, "completed", "unverified", "", nil); err != nil {
			return emitBrowserCallError("browser create", err)
		}
		attachOperationEvidence(result, record)
		return emitBrowserResult(result, func() {
			fmt.Printf("created tab: %v\n", result["tab_id"])
		})
	},
}

func init() {
	browserCmd.AddCommand(browserCreateCmd)
	browserCreateCmd.Flags().BoolVar(&browserCreateActive, "active", false, "Activate the new tab")
}
