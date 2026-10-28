package commands

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Capture limits belong to the capture family: eval and ordinary tab operations
// do not capture bodies or install collectors. Legacy spellings bind fresh values
// locally so their flags cannot change another command's defaults.
type browserCaptureOptions struct {
	Timeout          time.Duration
	MaxBodyBytes     int
	ProtocolPayloads bool
	WebRTCMedia      bool
}

type browserOpenRunner func(*cobra.Command, string, browserOpenFlags) error
type browserFetchRunner func(*cobra.Command, string, browserFetchFlags) error

func newBrowserCaptureOptions() *browserCaptureOptions {
	return &browserCaptureOptions{Timeout: 30 * time.Second, MaxBodyBytes: 8 * 1024 * 1024}
}

func bindBrowserCaptureFlags(flags *pflag.FlagSet, options *browserCaptureOptions) {
	flags.DurationVar(&options.Timeout, "timeout", 30*time.Second, "Maximum capture operation duration")
	flags.IntVar(&options.MaxBodyBytes, "max-body", 8*1024*1024, "Maximum captured response-body bytes per request")
	flags.BoolVar(&options.ProtocolPayloads, "protocol-payloads", false, "Instrument page WebTransport and WebRTC data-channel APIs during capture")
	flags.BoolVar(&options.WebRTCMedia, "webrtc-media", false, "Record clones of observed WebRTC audio/video tracks with native browser codecs")
}

func newBrowserCaptureCommand(open browserOpenRunner, fetch browserFetchRunner, action browserCommandRunner) *cobra.Command {
	options := newBrowserCaptureOptions()
	command := &cobra.Command{
		Use:   "capture",
		Short: "Capture network evidence from navigation, fetch, or a page action",
		Long: `Capture one bounded browser operation in the selected workspace and task.

Use open for navigation, fetch for a credentialed request, and action for page
JavaScript. Captures in a task archive automatically. Shared capture limits and
collectors may appear before or after the operation; see 'rep describe browser'.`,
	}
	bindBrowserCaptureFlags(command.PersistentFlags(), options)
	command.AddCommand(newBrowserOpenCommand(options, false, open), newBrowserFetchCommand(options, fetch), newBrowserEvaluationCommandWithCapture(true, options, action), newBrowserFanoutCommand(options))
	return command
}

func newBrowserOpenCommand(shared *browserCaptureOptions, includeBrowser bool, run browserOpenRunner) *cobra.Command {
	flags := browserOpenFlags{}
	command := &cobra.Command{
		Use:   "open <url-or-@file>",
		Short: "Navigate in a background tab and capture the full network session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags.RedactOutput = browserURLArgumentIsSensitive(args[0])
			rawURL, err := readBrowserURLArgument(args[0])
			if err != nil {
				name := "browser open"
				if includeBrowser {
					name = "browse"
				}
				return emitBrowserArgumentError(name, err)
			}
			if !includeBrowser {
				flags.Browser = browserSelector
			}
			flags.Timeout, flags.MaxBodyBytes = shared.Timeout, shared.MaxBodyBytes
			flags.ProtocolPayloads, flags.WebRTCMedia = shared.ProtocolPayloads, shared.WebRTCMedia
			return run(cmd, rawURL, flags)
		},
	}
	if includeBrowser {
		command.Flags().StringVar(&flags.Browser, "browser", defaultBrowser, "Browser bridge to use: dia, arc, chrome, headless, or any")
	}
	command.Flags().IntVar(&flags.TabID, "tab", -1, "Reuse a specific tab ID instead of creating a temporary tab")
	command.Flags().StringVar(&flags.Referrer, "referrer", "", "Optional HTTP(S) referrer for Page.navigate")
	command.Flags().BoolVar(&flags.Active, "active", false, "Activate the navigation tab")
	command.Flags().BoolVar(&flags.KeepTab, "keep-tab", false, "Keep a newly created tab after capture")
	command.Flags().StringVar(&flags.WaitWebSocket, "wait-websocket", "", "Wait until a matching WebSocket URL is seen and every match closes")
	command.Flags().DurationVar(&flags.Idle, "idle", 800*time.Millisecond, "Required network-idle interval")
	command.Flags().BoolVar(&flags.Save, "save", false, "Legacy global mode: archive capture (task captures already archive automatically)")
	command.Flags().StringVar(&flags.Note, "note", "", "Archive note")
	advancedFlags(command, "referrer", "idle", "save")
	return command
}

func newBrowserFetchCommand(shared *browserCaptureOptions, run browserFetchRunner) *cobra.Command {
	flags := browserFetchFlags{}
	command := &cobra.Command{
		Use:   "fetch <url-or-@file>",
		Short: "Run fetch inside the target origin using the browser session",
		Long: `Execute fetch() inside a matching browser tab with credentials included.

If no matching-origin tab exists, rep creates an inactive temporary tab,
loads the origin, performs the request, captures it, then closes the tab.
HttpOnly cookies remain browser-managed and are sent normally.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags.RedactOutput = browserURLArgumentIsSensitive(args[0])
			rawURL, err := readBrowserURLArgument(args[0])
			if err != nil {
				return emitBrowserArgumentError("browser fetch", err)
			}
			flags.Browser = browserSelector
			flags.Timeout, flags.MaxBodyBytes = shared.Timeout, shared.MaxBodyBytes
			flags.ProtocolPayloads, flags.WebRTCMedia = shared.ProtocolPayloads, shared.WebRTCMedia
			return run(cmd, rawURL, flags)
		},
	}
	command.Flags().IntVar(&flags.TabID, "tab", -1, "Use a specific matching-origin tab")
	command.Flags().StringVarP(&flags.Method, "method", "X", "GET", "HTTP method")
	command.Flags().StringArrayVarP(&flags.Headers, "header", "H", nil, "Request header 'Name: value' (repeatable)")
	command.Flags().StringVarP(&flags.Body, "data", "d", "", "Request body or @path")
	command.Flags().StringVar(&flags.Credentials, "credentials", "include", "Fetch credentials mode: include, same-origin, omit")
	command.Flags().StringVar(&flags.Cache, "cache", "default", "Fetch cache mode (default preserves normal browser behavior)")
	command.Flags().BoolVar(&flags.HeadersOnly, "headers-only", false, "Cancel the response body after headers while retaining the captured request")
	command.Flags().BoolVar(&flags.KeepTab, "keep-tab", false, "Keep a temporary origin tab")
	command.Flags().BoolVar(&flags.Save, "save", false, "Legacy global mode: archive capture (task captures already archive automatically)")
	command.Flags().StringVar(&flags.Note, "note", "", "Archive note")
	advancedFlags(command, "credentials", "cache", "save")
	return command
}

func newLegacyBrowserOpenCommand(browse bool, run browserOpenRunner) *cobra.Command {
	options := newBrowserCaptureOptions()
	command := newBrowserOpenCommand(options, browse, run)
	if browse {
		command.Use = "browse <url-or-@file>"
	}
	bindBrowserCaptureFlags(command.Flags(), options)
	markCaptureCompatibility(command, "rep browser capture open")
	return command
}

func newLegacyBrowserFetchCommand(run browserFetchRunner) *cobra.Command {
	options := newBrowserCaptureOptions()
	command := newBrowserFetchCommand(options, run)
	bindBrowserCaptureFlags(command.Flags(), options)
	markCaptureCompatibility(command, "rep browser capture fetch")
	return command
}

func newLegacyBrowserActionCommand(run browserCommandRunner) *cobra.Command {
	command := newBrowserEvaluationCommand(true, run)
	markCaptureCompatibility(command, "rep browser capture action")
	return command
}

func markCaptureCompatibility(command *cobra.Command, replacement string) {
	command.Hidden = true
	run := command.RunE
	command.RunE = func(cmd *cobra.Command, args []string) error {
		// Cobra's Deprecated field writes to stdout and corrupts JSON pipelines.
		fmt.Fprintf(cmd.ErrOrStderr(), "deprecated: %s; use %s\n", cmd.CommandPath(), replacement)
		return run(cmd, args)
	}
}
