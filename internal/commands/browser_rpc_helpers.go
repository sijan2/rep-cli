package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/spf13/cobra"
	"os"
	"strings"
	"time"
)

type browserDebuggeeFlags struct {
	TabID    int
	TargetID string
}

type browserCommandRunner func(*cobra.Command, string, string, map[string]interface{}, time.Duration) error

func bindDebuggeeFlags(command *cobra.Command, flags *browserDebuggeeFlags) {
	command.Flags().IntVar(&flags.TabID, "tab", -1, "Tab ID from 'rep browser tabs'")
	command.Flags().StringVar(&flags.TargetID, "target", "", "Debugger target ID from 'rep browser targets'")
}

func callBrowserRPC(cmd *cobra.Command, command, method string, params map[string]interface{}, timeout time.Duration) (returnErr error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	tab := -1
	if value, ok := params["tab_id"].(int); ok {
		tab = value
	}
	input := map[string]any{"intent": command, "timeout_ms": timeout.Milliseconds()}
	if value, ok := params["method"].(string); ok {
		input["cdp_method"] = value
	}
	record, err := beginBrowserEvidence(method, input, browserSelector, tab)
	if err != nil {
		return emitBrowserCallError(command, err)
	}
	defer record.finishOnReturn(&returnErr)
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	client, err := selectBrowserBridge(ctx, browserSelector, command)
	if err != nil {
		return err
	}
	var handoff browserCaptureHandoff
	if method == "browser.action" {
		handoff, err = prepareBrowserCapture(ctx, client)
		if err != nil {
			return emitBrowserCallError(command, err)
		}
		handoff.applyLimits(params)
		protocolPayloads, _ := params["protocol_payloads"].(bool)
		webrtcMedia, _ := params["webrtc_media"].(bool)
		if err := requireBrowserCollectors(ctx, client, protocolPayloads, webrtcMedia); err != nil {
			return emitBrowserCallError(command, err)
		}
	}
	var result map[string]interface{}
	record.dispatch()
	if err := client.Call(ctx, method, params, &result); err != nil {
		return emitBrowserCallError(command, err)
	}
	if method == "browser.action" {
		if err := finishBrowserCapture(ctx, client, result, handoff, "", false); err != nil {
			return emitBrowserCaptureReadError(command, err, false)
		}
		record.capture(result)
	}
	// Raw evaluator results may contain credentials or large response bodies.
	// Their command completion is recorded without copying renderer payloads.
	summary := captureEvidenceSummary(result)
	summary["rpc_completed"] = true
	if err := record.finish(summary, "completed", "unverified", "", nil); err != nil {
		return emitBrowserCallError(command, err)
	}
	attachOperationEvidence(result, record)
	return emitBrowserResult(result, func() { printBrowserJSON(result) })
}

func debuggeeParams(flags browserDebuggeeFlags) (map[string]interface{}, error) {
	targetID := strings.TrimSpace(flags.TargetID)
	if flags.TabID >= 0 && targetID != "" {
		return nil, fmt.Errorf("provide exactly one of --tab or --target")
	}
	if flags.TabID >= 0 {
		return map[string]interface{}{"tab_id": flags.TabID}, nil
	}
	if targetID != "" {
		return map[string]interface{}{"target_id": targetID}, nil
	}
	return nil, fmt.Errorf("--tab or --target is required")
}

func readBrowserArgument(value string) (string, error) {
	if !strings.HasPrefix(value, "@") {
		return value, nil
	}
	path := strings.TrimPrefix(value, "@")
	if path == "" {
		return "", fmt.Errorf("missing path after @")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), nil
}

func readJSONObject(value string) (map[string]interface{}, error) {
	if strings.TrimSpace(value) == "" {
		return map[string]interface{}{}, nil
	}
	raw, err := readBrowserArgument(value)
	if err != nil {
		return nil, err
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("invalid JSON object: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("CDP params must be a JSON object")
	}
	return result, nil
}

func emitBrowserArgumentError(command string, err error) error {
	return output.EmitAgentError(os.Stdout, output.NewAgentError(
		output.ErrCodeInvalidArgument,
		command,
		err.Error(),
		"rep browser tabs",
		"rep browser targets",
	), getOutputMode() == "json")
}

func printBrowserJSON(value interface{}) {
	data, err := output.Marshal(output.Result(value))
	if err == nil {
		fmt.Println(string(data))
	}
}
