package commands

import (
	"github.com/spf13/cobra"
	"time"
)

// Evaluation options and their defaults are shared by eval and captured action.
// Each factory owns its own values; sibling commands cannot leak parsed flags.
type browserEvaluationOptions struct {
	browserDebuggeeFlags
	AwaitPromise, ReturnByValue, UserGesture, REPLMode, KeepAttached bool
	Timeout, Idle, Settle                                            time.Duration
	MaxResultBytes                                                   int
}

func newBrowserEvaluationCommand(capture bool, run browserCommandRunner) *cobra.Command {
	if !capture {
		return newBrowserEvaluationCommandWithCapture(false, nil, run)
	}
	shared := newBrowserCaptureOptions()
	command := newBrowserEvaluationCommandWithCapture(true, shared, run)
	bindBrowserCaptureFlags(command.Flags(), shared)
	return command
}

func newBrowserEvaluationCommandWithCapture(capture bool, shared *browserCaptureOptions, run browserCommandRunner) *cobra.Command {
	options := browserEvaluationOptions{}
	if capture && shared == nil {
		shared = newBrowserCaptureOptions()
	}
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
			timeout = shared.Timeout
			params["timeout_ms"], params["idle_ms"], params["settle_ms"] = timeout.Milliseconds(), options.Idle.Milliseconds(), options.Settle.Milliseconds()
			params["max_body_bytes"], params["max_result_bytes"] = shared.MaxBodyBytes, options.MaxResultBytes
			params["protocol_payloads"] = shared.ProtocolPayloads
			params["webrtc_media"] = shared.WebRTCMedia
			timeout += 10 * time.Second
		} else {
			params["keep_attached"] = options.KeepAttached
		}
		return run(cmd, "browser "+name, "browser."+name, params, timeout)
	}}
	bindDebuggeeFlags(command, &options.browserDebuggeeFlags)
	if !capture {
		command.Flags().DurationVar(&options.Timeout, "timeout", 30*time.Second, "Maximum operation duration")
	}
	command.Flags().BoolVar(&options.AwaitPromise, "await", true, "Await a returned Promise")
	command.Flags().BoolVar(&options.ReturnByValue, "by-value", true, "Serialize the result by value")
	command.Flags().BoolVar(&options.UserGesture, "user-gesture", false, "Evaluate with a browser user-gesture token")
	command.Flags().BoolVar(&options.REPLMode, "repl", false, "Use DevTools console REPL parsing rules")
	if capture {
		command.Flags().DurationVar(&options.Idle, "idle", 300*time.Millisecond, "Required network-idle interval after the action")
		command.Flags().DurationVar(&options.Settle, "settle", 1500*time.Millisecond, "Minimum observation window for delayed callbacks")
		command.Flags().IntVar(&options.MaxResultBytes, "max-result", 64*1024, "Maximum serialized evaluation-result bytes")

	} else {
		command.Flags().BoolVar(&options.KeepAttached, "keep-attached", false, "Keep the target attached for subsequent commands")

	}

	command.Long = short + ".\n\nUse 'browser interact' for typed, verified UI workflows. Advanced evaluation\nand capture settings remain available; see 'rep browser --help'."
	return command
}
