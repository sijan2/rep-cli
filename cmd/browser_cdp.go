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

type browserCDPFlags struct {
	browserDebuggeeFlags
	Params       string
	KeepAttached bool
	Timeout      time.Duration
}

type browserEvalFlags struct {
	browserDebuggeeFlags
	AwaitPromise  bool
	ReturnByValue bool
	UserGesture   bool
	REPLMode      bool
	KeepAttached  bool
	Timeout       time.Duration
}

type browserActionFlags struct {
	browserDebuggeeFlags
	AwaitPromise   bool
	ReturnByValue  bool
	UserGesture    bool
	REPLMode       bool
	Timeout        time.Duration
	Idle           time.Duration
	Settle         time.Duration
	MaxBodyBytes   int
	MaxResultBytes int
}

var (
	attachDebuggee browserDebuggeeFlags
	detachDebuggee browserDebuggeeFlags
	probeDebuggee  browserDebuggeeFlags
	cdpFlags       browserCDPFlags
	evalFlags      browserEvalFlags
	actionFlags    browserActionFlags
)

var browserTargetsCmd = &cobra.Command{
	Use:   "targets",
	Short: "List Chromium debugger targets, including workers and extension contexts",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()
		client, err := selectBrowserBridge(ctx, browserSelector, "browser targets")
		if err != nil {
			return err
		}
		var result map[string]interface{}
		if err := client.Call(ctx, "browser.targets", nil, &result); err != nil {
			return emitBrowserCallError("browser targets", err)
		}
		return emitBrowserResult(result, func() { printBrowserJSON(result) })
	},
}

var browserAttachCmd = &cobra.Command{
	Use:   "attach",
	Short: "Keep a debugger target attached across low-latency CDP commands",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBrowserTargetRPC(cmd, "browser attach", "browser.attach", attachDebuggee, 10*time.Second)
	},
}

var browserDetachCmd = &cobra.Command{
	Use:   "detach",
	Short: "Release a persistent debugger attachment",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBrowserTargetRPC(cmd, "browser detach", "browser.detach", detachDebuggee, 10*time.Second)
	},
}

var browserCDPCmd = &cobra.Command{
	Use:   "cdp <Domain.command>",
	Short: "Send an arbitrary Chrome DevTools Protocol command to a real Arc tab or target",
	Long: `Send a raw CDP command through chrome.debugger in the running browser.

The command uses Arc's normal profile, renderer, network stack, TLS identity,
cookies, service workers, extensions, and device fingerprint. No headless or
WebDriver process is created.

Examples:
  rep browser cdp Runtime.evaluate --tab 123 --params '{"expression":"document.title","returnByValue":true}'
  rep browser cdp DOM.getDocument --tab 123
  rep browser cdp Input.dispatchMouseEvent --tab 123 --params @click.json --keep-attached`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params, err := debuggeeParams(cdpFlags.browserDebuggeeFlags)
		if err != nil {
			return emitBrowserArgumentError("browser cdp", err)
		}
		commandParams, err := readJSONObject(cdpFlags.Params)
		if err != nil {
			return emitBrowserArgumentError("browser cdp", err)
		}
		params["method"] = args[0]
		params["command_params"] = commandParams
		params["keep_attached"] = cdpFlags.KeepAttached
		return callBrowserRPC(cmd, "browser cdp", "browser.cdp", params, cdpFlags.Timeout)
	},
}

var browserEvalCmd = &cobra.Command{
	Use:   "eval <javascript-or-@file>",
	Short: "Evaluate JavaScript in a real signed-in page renderer",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params, err := debuggeeParams(evalFlags.browserDebuggeeFlags)
		if err != nil {
			return emitBrowserArgumentError("browser eval", err)
		}
		expression, err := readBrowserArgument(args[0])
		if err != nil {
			return emitBrowserArgumentError("browser eval", err)
		}
		params["expression"] = expression
		params["await_promise"] = evalFlags.AwaitPromise
		params["return_by_value"] = evalFlags.ReturnByValue
		params["user_gesture"] = evalFlags.UserGesture
		params["repl_mode"] = evalFlags.REPLMode
		params["keep_attached"] = evalFlags.KeepAttached
		return callBrowserRPC(cmd, "browser eval", "browser.eval", params, evalFlags.Timeout)
	},
}

var browserActionCmd = &cobra.Command{
	Use:   "action <javascript-or-@file>",
	Short: "Evaluate a page action and capture the network traffic it triggers",
	Long: `Evaluate JavaScript in a real signed-in page while one isolated CDP
capture records the requests, response metadata, and bounded response bodies
triggered by that action. This combines eval + capture into one operation and
publishes the sealed capture to live.json. Serialized evaluation output is
bounded independently so an accidental response body cannot flood the bridge.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params, err := debuggeeParams(actionFlags.browserDebuggeeFlags)
		if err != nil {
			return emitBrowserArgumentError("browser action", err)
		}
		expression, err := readBrowserArgument(args[0])
		if err != nil {
			return emitBrowserArgumentError("browser action", err)
		}
		params["expression"] = expression
		params["await_promise"] = actionFlags.AwaitPromise
		params["return_by_value"] = actionFlags.ReturnByValue
		params["user_gesture"] = actionFlags.UserGesture
		params["repl_mode"] = actionFlags.REPLMode
		params["timeout_ms"] = actionFlags.Timeout.Milliseconds()
		params["idle_ms"] = actionFlags.Idle.Milliseconds()
		params["settle_ms"] = actionFlags.Settle.Milliseconds()
		params["max_body_bytes"] = actionFlags.MaxBodyBytes
		params["max_result_bytes"] = actionFlags.MaxResultBytes
		return callBrowserRPC(cmd, "browser action", "browser.action", params, actionFlags.Timeout+10*time.Second)
	},
}

var browserProbeCmd = &cobra.Command{
	Use:   "probe",
	Short: "Measure bot-visible browser identity and page lifecycle state",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		params, err := debuggeeParams(probeDebuggee)
		if err != nil {
			return emitBrowserArgumentError("browser probe", err)
		}
		return callBrowserRPC(cmd, "browser probe", "browser.probe", params, 15*time.Second)
	},
}

func runBrowserTargetRPC(cmd *cobra.Command, command, method string, flags browserDebuggeeFlags, timeout time.Duration) error {
	params, err := debuggeeParams(flags)
	if err != nil {
		return emitBrowserArgumentError(command, err)
	}
	return callBrowserRPC(cmd, command, method, params, timeout)
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
	var result map[string]interface{}
	if err := client.Call(ctx, method, params, &result); err != nil {
		return emitBrowserCallError(command, err)
	}
	if method == "browser.action" {
		if err := enrichBrowserCaptureResult(result); err != nil {
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

func bindDebuggeeFlags(command *cobra.Command, flags *browserDebuggeeFlags) {
	command.Flags().IntVar(&flags.TabID, "tab", -1, "Tab ID from 'rep browser tabs'")
	command.Flags().StringVar(&flags.TargetID, "target", "", "Debugger target ID from 'rep browser targets'")
}

func init() {
	browserCmd.AddCommand(
		browserTargetsCmd,
		browserAttachCmd,
		browserDetachCmd,
		browserCDPCmd,
		browserEvalCmd,
		browserActionCmd,
		browserProbeCmd,
	)
	bindDebuggeeFlags(browserAttachCmd, &attachDebuggee)
	bindDebuggeeFlags(browserDetachCmd, &detachDebuggee)
	bindDebuggeeFlags(browserProbeCmd, &probeDebuggee)
	bindDebuggeeFlags(browserCDPCmd, &cdpFlags.browserDebuggeeFlags)
	bindDebuggeeFlags(browserEvalCmd, &evalFlags.browserDebuggeeFlags)
	bindDebuggeeFlags(browserActionCmd, &actionFlags.browserDebuggeeFlags)

	browserCDPCmd.Flags().StringVar(&cdpFlags.Params, "params", "{}", "CDP parameter JSON object or @path")
	browserCDPCmd.Flags().BoolVar(&cdpFlags.KeepAttached, "keep-attached", false, "Keep the target attached for subsequent low-latency commands")
	browserCDPCmd.Flags().DurationVar(&cdpFlags.Timeout, "timeout", 30*time.Second, "Maximum RPC duration")

	browserEvalCmd.Flags().BoolVar(&evalFlags.AwaitPromise, "await", true, "Await a returned Promise")
	browserEvalCmd.Flags().BoolVar(&evalFlags.ReturnByValue, "by-value", true, "Serialize the result by value")
	browserEvalCmd.Flags().BoolVar(&evalFlags.UserGesture, "user-gesture", false, "Evaluate with a browser user-gesture token")
	browserEvalCmd.Flags().BoolVar(&evalFlags.REPLMode, "repl", false, "Use DevTools console REPL parsing rules")
	browserEvalCmd.Flags().BoolVar(&evalFlags.KeepAttached, "keep-attached", false, "Keep the target attached for subsequent low-latency commands")
	browserEvalCmd.Flags().DurationVar(&evalFlags.Timeout, "timeout", 30*time.Second, "Maximum evaluation duration")

	browserActionCmd.Flags().BoolVar(&actionFlags.AwaitPromise, "await", true, "Await a returned Promise")
	browserActionCmd.Flags().BoolVar(&actionFlags.ReturnByValue, "by-value", true, "Serialize the result by value")
	browserActionCmd.Flags().BoolVar(&actionFlags.UserGesture, "user-gesture", false, "Evaluate with a browser user-gesture token")
	browserActionCmd.Flags().BoolVar(&actionFlags.REPLMode, "repl", false, "Use DevTools console REPL parsing rules")
	browserActionCmd.Flags().DurationVar(&actionFlags.Timeout, "timeout", 30*time.Second, "Maximum action/capture duration")
	browserActionCmd.Flags().DurationVar(&actionFlags.Idle, "idle", 300*time.Millisecond, "Required network-idle interval after the action")
	browserActionCmd.Flags().DurationVar(&actionFlags.Settle, "settle", 1500*time.Millisecond, "Minimum observation window for delayed browser callbacks")
	browserActionCmd.Flags().IntVar(&actionFlags.MaxBodyBytes, "max-body", 384*1024, "Maximum captured response-body bytes per request")
	browserActionCmd.Flags().IntVar(&actionFlags.MaxResultBytes, "max-result", 64*1024, "Maximum serialized action-result bytes returned through the bridge")
}
