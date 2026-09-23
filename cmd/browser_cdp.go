package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/spf13/cobra"
)

type browserDebuggeeFlags struct {
	TabID    int
	TargetID string
}
type browserCommandRunner func(*cobra.Command, string, string, map[string]interface{}, time.Duration) error

// Evaluation options and their defaults are shared by eval and captured action.
// Each factory owns its own values; sibling commands cannot leak parsed flags.
type browserEvaluationOptions struct {
	browserDebuggeeFlags
	AwaitPromise, ReturnByValue, UserGesture, REPLMode, KeepAttached bool
	Timeout, Idle, Settle                                            time.Duration
	MaxBodyBytes, MaxResultBytes                                     int
}

func newBrowserTargetCommand(name, short string, timeout time.Duration, run browserCommandRunner) *cobra.Command {
	flags := browserDebuggeeFlags{}
	command := &cobra.Command{Use: name, Short: short, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		params, err := debuggeeParams(flags)
		if err != nil {
			return emitBrowserArgumentError("browser "+name, err)
		}
		return run(cmd, "browser "+name, "browser."+name, params, timeout)
	}}
	bindDebuggeeFlags(command, &flags)
	return command
}
func newBrowserCDPCommand(run browserCommandRunner) *cobra.Command {
	flags := browserDebuggeeFlags{}
	var raw string
	var keep bool
	var timeout time.Duration
	command := &cobra.Command{Use: "cdp <Domain.command>", Short: "Send a raw Chrome DevTools Protocol command", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		params, err := debuggeeParams(flags)
		if err != nil {
			return emitBrowserArgumentError("browser cdp", err)
		}
		values, err := readJSONObject(raw)
		if err != nil {
			return emitBrowserArgumentError("browser cdp", err)
		}
		params["method"], params["command_params"], params["keep_attached"] = args[0], values, keep
		return run(cmd, "browser cdp", "browser.cdp", params, timeout)
	}}
	bindDebuggeeFlags(command, &flags)
	command.Flags().StringVar(&raw, "params", "{}", "CDP parameter JSON or @path")
	command.Flags().BoolVar(&keep, "keep-attached", false, "Keep the target attached across low-latency commands")
	command.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Maximum RPC duration")
	return command
}
func newBrowserEvaluationCommand(capture bool, run browserCommandRunner) *cobra.Command {
	options := browserEvaluationOptions{}
	name, short := "eval", "Evaluate JavaScript in a real page renderer"
	if capture {
		name, short = "action", "Evaluate JavaScript and archive the resulting network capture"
	}
	command := &cobra.Command{Use: name + " <javascript-or-@file>", Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		params, err := debuggeeParams(options.browserDebuggeeFlags)
		if err != nil {
			return emitBrowserArgumentError("browser "+name, err)
		}
		expression, err := readBrowserArgument(args[0])
		if err != nil {
			return emitBrowserArgumentError("browser "+name, err)
		}
		params["expression"], params["await_promise"], params["return_by_value"] = expression, options.AwaitPromise, options.ReturnByValue
		params["user_gesture"], params["repl_mode"] = options.UserGesture, options.REPLMode
		timeout := options.Timeout
		if capture {
			params["timeout_ms"], params["idle_ms"], params["settle_ms"] = timeout.Milliseconds(), options.Idle.Milliseconds(), options.Settle.Milliseconds()
			params["max_body_bytes"], params["max_result_bytes"] = options.MaxBodyBytes, options.MaxResultBytes
			timeout += 10 * time.Second
		} else {
			params["keep_attached"] = options.KeepAttached
		}
		return run(cmd, "browser "+name, "browser."+name, params, timeout)
	}}
	bindDebuggeeFlags(command, &options.browserDebuggeeFlags)
	command.Flags().DurationVar(&options.Timeout, "timeout", 30*time.Second, "Maximum operation duration")
	command.Flags().BoolVar(&options.AwaitPromise, "await", true, "Await a returned Promise")
	command.Flags().BoolVar(&options.ReturnByValue, "by-value", true, "Serialize the result by value")
	command.Flags().BoolVar(&options.UserGesture, "user-gesture", false, "Evaluate with a browser user-gesture token")
	command.Flags().BoolVar(&options.REPLMode, "repl", false, "Use DevTools console REPL parsing rules")
	if capture {
		command.Flags().DurationVar(&options.Idle, "idle", 300*time.Millisecond, "Required network-idle interval after the action")
		command.Flags().DurationVar(&options.Settle, "settle", 1500*time.Millisecond, "Minimum observation window for delayed callbacks")
		command.Flags().IntVar(&options.MaxBodyBytes, "max-body", 8*1024*1024, "Maximum captured response bytes per request")
		command.Flags().IntVar(&options.MaxResultBytes, "max-result", 64*1024, "Maximum serialized evaluation-result bytes")
		advancedFlags(command, "idle", "settle", "max-result")
	} else {
		command.Flags().BoolVar(&options.KeepAttached, "keep-attached", false, "Keep the target attached for subsequent commands")
		advancedFlags(command, "keep-attached")
	}
	advancedFlags(command, "await", "by-value", "user-gesture", "repl", "target")
	command.Long = short + ".\n\nUse 'browser interact' for typed, verified UI workflows. Advanced evaluation\nand capture settings remain available; see 'rep describe browser'."
	return command
}

func bindDebuggeeFlags(command *cobra.Command, flags *browserDebuggeeFlags) {
	command.Flags().IntVar(&flags.TabID, "tab", -1, "Tab ID from 'rep browser tabs'")
	command.Flags().StringVar(&flags.TargetID, "target", "", "Debugger target ID from 'rep browser targets'")
}
func init() {
	browserCmd.AddCommand(
		&cobra.Command{Use: "targets", Short: "List Chromium debugger targets", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			return callBrowserRPC(cmd, "browser targets", "browser.targets", nil, 5*time.Second)
		}},
		newBrowserTargetCommand("attach", "Keep a debugger target attached", 10*time.Second, callBrowserRPC),
		newBrowserTargetCommand("detach", "Release a persistent debugger attachment", 10*time.Second, callBrowserRPC),
		newBrowserTargetCommand("probe", "Inspect browser identity and page lifecycle state", 15*time.Second, callBrowserRPC),
		newBrowserCDPCommand(callBrowserRPC), newBrowserEvaluationCommand(false, callBrowserRPC), newBrowserEvaluationCommand(true, callBrowserRPC),
	)
}

func callBrowserRPC(cmd *cobra.Command, command, method string, params map[string]interface{}, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
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
	}
	var result map[string]interface{}
	if err := client.Call(ctx, method, params, &result); err != nil {
		return emitBrowserCallError(command, err)
	}
	if method == "browser.action" {
		if err := finishBrowserCapture(ctx, client, result, handoff, "", false); err != nil {
			return emitBrowserCaptureReadError(command, err, false)
		}
	}
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
	data, err := sonic.MarshalIndent(value, "", "  ")
	if err == nil {
		fmt.Println(string(data))
	}
}
