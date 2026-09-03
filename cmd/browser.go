package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

type browserOpenFlags struct {
	Browser      string
	TabID        int
	Referrer     string
	Active       bool
	KeepTab      bool
	Timeout      time.Duration
	Idle         time.Duration
	MaxBodyBytes int
	Save         bool
	Note         string
	RedactOutput bool
}

type browserFetchFlags struct {
	Browser      string
	TabID        int
	Method       string
	Headers      []string
	Body         string
	Credentials  string
	Cache        string
	HeadersOnly  bool
	KeepTab      bool
	Timeout      time.Duration
	MaxBodyBytes int
	Save         bool
	Note         string
	RedactOutput bool
}

var (
	browserSelector string
	openFlags       browserOpenFlags
	browseFlags     browserOpenFlags
	fetchFlags      browserFetchFlags
	closeTabID      int
)

var browserCmd = &cobra.Command{
	Use:   "browser",
	Short: "Control a connected Arc/Chrome session through rep+",
	Long: `Control a real signed-in Chromium browser without focusing its UI.

The rep+ background worker owns a persistent native bridge. Navigation and
fetch commands use chrome.debugger inside the browser, capture request and
response data, and atomically publish the result to live.json.

Examples:
  rep browser status --browser arc
  rep browser reload-extension --browser arc
  rep browser tabs --browser arc
  rep browser create about:blank --browser arc
  rep browser open github.com --browser arc
  rep browser fetch https://github.com/settings/profile --browser arc
  rep browser fetch https://api.example.com/me -X POST -H 'content-type: application/json' --data '{}'
  rep browser download <request-id> /absolute/path/artifact.bin
  rep browse google.com --save --note google-home`,
}

var browserStatusCmd = &cobra.Command{
	Use:     "status",
	Aliases: []string{"ping"},
	Short:   "Check the extension/native bridge and browser capabilities",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()
		client, err := selectBrowserBridge(ctx, browserSelector, "browser status")
		if err != nil {
			return err
		}
		var result map[string]interface{}
		if err := client.Call(ctx, "browser.status", nil, &result); err != nil {
			return emitBrowserCallError("browser status", err)
		}
		result["browser"] = client.Registry.Browser
		result["browser_label"] = client.Registry.BrowserLabel
		result["native_host_pid"] = client.Registry.PID
		result["socket"] = client.Registry.Socket
		return emitBrowserResult(result, func() {
			fmt.Printf("browser: %s\n", nonEmpty(client.Registry.BrowserLabel, client.Registry.Browser))
			fmt.Printf("bridge: connected (pid %d)\n", client.Registry.PID)
			fmt.Printf("tabs: %v\n", result["tabs"])
			fmt.Printf("active captures: %v\n", result["active_captures"])
			fmt.Printf("capabilities: %v\n", result["capabilities"])
		})
	},
}

var browserTabsCmd = &cobra.Command{
	Use:   "tabs",
	Short: "List controllable tabs without activating them",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()
		client, err := selectBrowserBridge(ctx, browserSelector, "browser tabs")
		if err != nil {
			return err
		}
		var result struct {
			Tabs []struct {
				ID        int    `json:"id"`
				WindowID  int    `json:"window_id"`
				Active    bool   `json:"active"`
				Pinned    bool   `json:"pinned"`
				Incognito bool   `json:"incognito"`
				Discarded bool   `json:"discarded"`
				Status    string `json:"status"`
				Title     string `json:"title"`
				URL       string `json:"url"`
			} `json:"tabs"`
		}
		if err := client.Call(ctx, "browser.tabs", nil, &result); err != nil {
			return emitBrowserCallError("browser tabs", err)
		}
		return emitBrowserResult(result, func() {
			for _, tab := range result.Tabs {
				flags := ""
				if tab.Active {
					flags += " active"
				}
				if tab.Discarded {
					flags += " discarded"
				}
				if tab.Pinned {
					flags += " pinned"
				}
				if tab.Incognito {
					flags += " incognito"
				}
				fmt.Printf("%d\t%s%s\t%s\n", tab.ID, tab.Status, flags, tab.URL)
			}
		})
	},
}

var browserOpenCmd = &cobra.Command{
	Use:   "open <url-or-@file>",
	Short: "Navigate in a background tab and capture the full network session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		openFlags.RedactOutput = browserURLArgumentIsSensitive(args[0])
		rawURL, err := readBrowserURLArgument(args[0])
		if err != nil {
			return emitBrowserArgumentError("browser open", err)
		}
		openFlags.Browser = browserSelector
		return runBrowserOpen(cmd, rawURL, openFlags)
	},
}

var browseCmd = &cobra.Command{
	Use:   "browse <url-or-@file>",
	Short: "Background browser navigation with sessioned network capture",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		browseFlags.RedactOutput = browserURLArgumentIsSensitive(args[0])
		rawURL, err := readBrowserURLArgument(args[0])
		if err != nil {
			return emitBrowserArgumentError("browse", err)
		}
		return runBrowserOpen(cmd, rawURL, browseFlags)
	},
}

var browserFetchCmd = &cobra.Command{
	Use:   "fetch <url-or-@file>",
	Short: "Run fetch inside the target origin using the browser session",
	Long: `Execute fetch() inside a matching browser tab with credentials included.

If no matching-origin tab exists, rep creates an inactive temporary tab,
loads the origin, performs the request, captures it, then closes the tab.
HttpOnly cookies remain browser-managed and are sent normally.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fetchFlags.RedactOutput = browserURLArgumentIsSensitive(args[0])
		rawURL, err := readBrowserURLArgument(args[0])
		if err != nil {
			return emitBrowserArgumentError("browser fetch", err)
		}
		fetchFlags.Browser = browserSelector
		return runBrowserFetch(cmd, rawURL, fetchFlags)
	},
}

var browserCloseCmd = &cobra.Command{
	Use:   "close <tab-id>",
	Short: "Close a browser tab by ID",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := fmt.Sscanf(args[0], "%d", &closeTabID); err != nil || closeTabID < 0 {
			return output.EmitAgentError(os.Stdout, output.NewAgentError(output.ErrCodeInvalidArgument, "browser close", "tab-id must be a non-negative integer", "rep browser tabs"), getOutputMode() == "json")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()
		client, err := selectBrowserBridge(ctx, browserSelector, "browser close")
		if err != nil {
			return err
		}
		var result map[string]interface{}
		if err := client.Call(ctx, "browser.close", map[string]interface{}{"tab_id": closeTabID}, &result); err != nil {
			return emitBrowserCallError("browser close", err)
		}
		return emitBrowserResult(result, func() { fmt.Printf("closed tab %d\n", closeTabID) })
	},
}

var browserWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Stream normal browsing traffic into a dedicated live session",
}

var browserWatchStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Clear live.json and start ambient all-tab capture",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBrowserWatch(cmd, "start")
	},
}

var browserWatchStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Seal the current ambient capture",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBrowserWatch(cmd, "stop")
	},
}

var browserWatchStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether ambient capture is enabled",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBrowserWatch(cmd, "status")
	},
}

func runBrowserOpen(cmd *cobra.Command, rawURL string, flags browserOpenFlags) error {
	if flags.Timeout <= 0 {
		flags.Timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), flags.Timeout+10*time.Second)
	defer cancel()
	client, err := selectBrowserBridge(ctx, flags.Browser, "browser open")
	if err != nil {
		return err
	}
	params := map[string]interface{}{
		"url": rawURL, "active": flags.Active, "keep_tab": flags.KeepTab,
		"timeout_ms": flags.Timeout.Milliseconds(), "idle_ms": flags.Idle.Milliseconds(),
		"max_body_bytes": flags.MaxBodyBytes, "redact_output": flags.RedactOutput,
	}
	if strings.TrimSpace(flags.Referrer) != "" {
		params["referrer"] = flags.Referrer
	}
	if flags.TabID >= 0 {
		params["tab_id"] = flags.TabID
	}
	var result map[string]interface{}
	if err := client.Call(ctx, "browser.open", params, &result); err != nil {
		return emitBrowserCallErrorWithPrivacy("browser open", err, flags.RedactOutput)
	}
	if err := enrichBrowserCaptureResult(result); err != nil {
		return emitBrowserCaptureReadError("browser open", err, flags.RedactOutput)
	}
	if flags.Save {
		session, saveErr := archiveBrowserCapture(flags.Note)
		if saveErr != nil {
			return emitBrowserCaptureSaveError("browser open", saveErr, flags.RedactOutput)
		}
		result["saved_session_id"] = session.ID
		result["saved_hash_id"] = session.HashID
	}
	if flags.RedactOutput {
		redactSensitiveBrowserResult(result, "open")
	}
	return emitBrowserResult(result, func() {
		fmt.Printf("session: %v\n", result["session_id"])
		if flags.RedactOutput {
			fmt.Println("url: <redacted>")
		} else {
			fmt.Printf("url: %v\n", result["final_url"])
		}
		fmt.Printf("capture: %v requests across %v domains (%v response bodies)\n", result["requests"], result["domains"], result["response_bodies"])
		printBrowserCapturedRequests(result)
		fmt.Printf("timed_out: %v\n", result["timed_out"])
		if saved := result["saved_session_id"]; saved != nil {
			fmt.Printf("saved: %v\n", saved)
		}
		fmt.Println("next: rep body <captured-request-id>")
	})
}

func runBrowserFetch(cmd *cobra.Command, rawURL string, flags browserFetchFlags) error {
	if flags.Timeout <= 0 {
		flags.Timeout = 30 * time.Second
	}
	headers, err := parseBrowserHeaders(flags.Headers)
	if err != nil {
		return output.EmitAgentError(os.Stdout, output.NewAgentError(output.ErrCodeInvalidArgument, "browser fetch", err.Error()), getOutputMode() == "json")
	}
	body := flags.Body
	if strings.HasPrefix(body, "@") {
		data, readErr := os.ReadFile(strings.TrimPrefix(body, "@"))
		if readErr != nil {
			return output.EmitAgentError(os.Stdout, output.WrapError(readErr, output.ErrCodeInvalidArgument, "browser fetch"), getOutputMode() == "json")
		}
		body = string(data)
	}
	credentials := strings.ToLower(flags.Credentials)
	if credentials != "include" && credentials != "same-origin" && credentials != "omit" {
		return output.EmitAgentError(os.Stdout, output.NewAgentError(output.ErrCodeInvalidArgument, "browser fetch", "--credentials must be include, same-origin, or omit"), getOutputMode() == "json")
	}
	cacheMode := strings.ToLower(strings.TrimSpace(flags.Cache))
	validCacheModes := map[string]bool{"default": true, "no-store": true, "reload": true, "no-cache": true, "force-cache": true, "only-if-cached": true}
	if !validCacheModes[cacheMode] {
		return output.EmitAgentError(os.Stdout, output.NewAgentError(output.ErrCodeInvalidArgument, "browser fetch", "--cache must be default, no-store, reload, no-cache, force-cache, or only-if-cached"), getOutputMode() == "json")
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), flags.Timeout+10*time.Second)
	defer cancel()
	client, err := selectBrowserBridge(ctx, flags.Browser, "browser fetch")
	if err != nil {
		return err
	}
	params := map[string]interface{}{
		"url": rawURL, "method": strings.ToUpper(flags.Method), "headers": headers,
		"body": body, "credentials": credentials, "cache": cacheMode, "keep_tab": flags.KeepTab,
		"headers_only": flags.HeadersOnly, "timeout_ms": flags.Timeout.Milliseconds(), "max_body_bytes": flags.MaxBodyBytes,
		"redact_output": flags.RedactOutput,
	}
	if flags.TabID >= 0 {
		params["tab_id"] = flags.TabID
	}
	var result map[string]interface{}
	if err := client.Call(ctx, "browser.fetch", params, &result); err != nil {
		return emitBrowserCallErrorWithPrivacy("browser fetch", err, flags.RedactOutput)
	}
	if err := enrichBrowserCaptureResult(result); err != nil {
		return emitBrowserCaptureReadError("browser fetch", err, flags.RedactOutput)
	}
	if flags.Save {
		session, saveErr := archiveBrowserCapture(flags.Note)
		if saveErr != nil {
			return emitBrowserCaptureSaveError("browser fetch", saveErr, flags.RedactOutput)
		}
		result["saved_session_id"] = session.ID
	}
	if flags.RedactOutput {
		redactSensitiveBrowserResult(result, "fetch")
	}
	return emitBrowserResult(result, func() {
		response, _ := result["response"].(map[string]interface{})
		fmt.Printf("session: %v\n", result["session_id"])
		if flags.RedactOutput {
			fmt.Printf("response: %v <redacted>\n", response["status"])
		} else {
			fmt.Printf("response: %v %v\n", response["status"], response["url"])
		}
		fmt.Printf("capture: %v requests (%v response bodies)\n", result["requests"], result["response_bodies"])
		printBrowserCapturedRequests(result)
		if body, ok := response["body"].(string); ok && body != "" {
			fmt.Println(body)
		}
	})
}

func runBrowserWatch(cmd *cobra.Command, action string) error {
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

func selectBrowserBridge(ctx context.Context, selector, command string) (*bridge.Client, error) {
	client, err := bridge.Select(ctx, selector)
	if err == nil {
		return client, nil
	}
	return nil, output.EmitAgentError(os.Stdout, output.NewAgentError(
		output.ErrCodeBrowserUnavailable,
		command,
		err.Error(),
		"reload rep+ in arc://extensions or chrome://extensions",
		"verify com.repplus.host.json points to the installed rep-host",
		"rep browser status --browser arc",
	), getOutputMode() == "json")
}

func emitBrowserCallError(command string, err error) error {
	return emitBrowserCallErrorWithPrivacy(command, err, false)
}

func emitBrowserCallErrorWithPrivacy(command string, err error, redact bool) error {
	code := output.ErrCodeBrowserRPC
	var rpcError *bridge.RPCError
	if errorsAs(err, &rpcError) && rpcError.Code != "" {
		code = rpcError.Code
	}
	if redact {
		return output.EmitAgentError(os.Stdout, output.NewAgentError(
			code,
			command,
			"request for a URL loaded from a private file failed; sensitive details were omitted",
			"rep browser status",
			"rep browser tabs",
		), getOutputMode() == "json")
	}
	return output.EmitAgentError(os.Stdout, output.WrapError(err, code, command, "rep browser status", "rep browser tabs"), getOutputMode() == "json")
}

func emitBrowserCaptureSaveError(command string, err error, redact bool) error {
	if redact {
		return output.EmitAgentError(os.Stdout, output.NewAgentError(
			output.ErrCodeStoreWrite,
			command,
			"sealed browser capture could not be archived; sensitive details were omitted",
			"rep browser status",
			"rep summary",
		), getOutputMode() == "json")
	}
	return output.EmitAgentError(os.Stdout, output.WrapError(err, output.ErrCodeStoreWrite, command), getOutputMode() == "json")
}

func redactSensitiveBrowserResult(result map[string]interface{}, operation string) {
	if result == nil {
		return
	}
	safe := map[string]interface{}{
		"sensitive_output_redacted": true,
	}
	for _, key := range []string{
		"tab_id", "requests", "domains", "response_bodies", "captured_body_bytes",
		"failed_requests", "ignored_cancellations", "pending_requests", "duration_ms", "settle_ms",
	} {
		if value, ok := browserSafeNumber(result[key]); ok {
			safe[key] = value
		}
	}
	for _, key := range []string{"tab_closed", "timed_out"} {
		if value, ok := result[key].(bool); ok {
			safe[key] = value
		}
	}
	for _, key := range []string{"session_id", "saved_session_id", "saved_hash_id"} {
		if value, ok := browserSafeIdentifier(result[key]); ok {
			safe[key] = value
		}
	}
	if value, ok := result["load_state"].(string); ok {
		switch value {
		case "network-idle", "load-grace", "timeout", "failed":
			safe["load_state"] = value
		}
	}
	if requests, ok := result["captured_requests"].([]browserCapturedRequest); ok {
		safe["captured_requests"] = browserSafeCapturedRequests(requests)
	}
	if outcome, ok := result["terminal_outcome"].(*browserTerminalOutcome); ok {
		if sanitized, valid := browserSafeTerminalOutcome(outcome); valid {
			safe["terminal_outcome"] = sanitized
		}
	}
	if operation == "open" {
		safe["requested_url_redacted"] = true
		safe["final_url_redacted"] = true
		safe["navigation_omitted"] = true
	}
	if request, ok := result["request"].(map[string]interface{}); ok {
		safeRequest := map[string]interface{}{"url_redacted": true}
		if method, ok := browserSafeHTTPMethod(request["method"]); ok {
			safeRequest["method"] = method
		}
		safe["request"] = safeRequest
	}
	if response, ok := result["response"].(map[string]interface{}); ok {
		safeResponse := map[string]interface{}{
			"url_redacted":    true,
			"headers_omitted": true,
			"body_omitted":    true,
		}
		for _, key := range []string{"status", "body_bytes", "body_declared_bytes"} {
			if value, ok := browserSafeNumber(response[key]); ok {
				safeResponse[key] = value
			}
		}
		for _, key := range []string{"ok", "redirected", "body_truncated"} {
			if value, ok := response[key].(bool); ok {
				safeResponse[key] = value
			}
		}
		if value, ok := response["type"].(string); ok {
			switch value {
			case "basic", "cors", "default", "error", "opaque", "opaqueredirect":
				safeResponse["type"] = value
			}
		}
		safe["response"] = safeResponse
	}
	for key := range result {
		delete(result, key)
	}
	for key, value := range safe {
		result[key] = value
	}
}

func browserSafeCapturedRequests(requests []browserCapturedRequest) []browserCapturedRequest {
	safe := make([]browserCapturedRequest, 0, len(requests))
	for _, request := range requests {
		id, idOK := browserSafeRequestID(request.ID)
		method, methodOK := browserSafeHTTPMethod(request.Method)
		if !idOK || !methodOK || request.Sequence <= 0 || request.Status < 0 || request.BodyBytes < 0 {
			continue
		}
		safe = append(safe, browserCapturedRequest{
			Sequence:                request.Sequence,
			ID:                      id,
			Method:                  method,
			Status:                  request.Status,
			BodyBytes:               request.BodyBytes,
			BodyTruncated:           request.BodyTruncated,
			IntentionalCancellation: browserIntentionalCancellationKind(request.IntentionalCancellation),
		})
	}
	return safe
}

func browserSafeTerminalOutcome(outcome *browserTerminalOutcome) (*browserTerminalOutcome, bool) {
	if outcome == nil || (outcome.Kind != "redirect_chain" && outcome.Kind != "download_handoff") ||
		outcome.TerminalStatus < 0 || outcome.RedirectHops < 0 || outcome.LaterFormFailures < 0 {
		return nil, false
	}
	sourceID, sourceOK := browserSafeRequestID(outcome.SourceRequestID)
	terminalID, terminalOK := browserSafeRequestID(outcome.TerminalRequestID)
	if !sourceOK || !terminalOK {
		return nil, false
	}
	requestIDs, ok := browserSafeRequestIDs(outcome.RequestIDs)
	if !ok {
		return nil, false
	}
	failureIDs, ok := browserSafeRequestIDs(outcome.LaterFormFailureIDs)
	if !ok {
		return nil, false
	}
	return &browserTerminalOutcome{
		Kind:                     outcome.Kind,
		Completed:                outcome.Completed,
		TerminalResponseReceived: outcome.TerminalResponseReceived,
		DownloadHandoffStarted:   outcome.DownloadHandoffStarted,
		SourceRequestID:          sourceID,
		TerminalRequestID:        terminalID,
		TerminalStatus:           outcome.TerminalStatus,
		RedirectHops:             outcome.RedirectHops,
		RequestIDs:               requestIDs,
		LaterFormFailures:        outcome.LaterFormFailures,
		LaterFormFailureIDs:      failureIDs,
	}, true
}

func browserSafeRequestIDs(values []string) ([]string, bool) {
	safe := make([]string, 0, len(values))
	for _, value := range values {
		id, ok := browserSafeRequestID(value)
		if !ok {
			return nil, false
		}
		safe = append(safe, id)
	}
	return safe, true
}

func browserSafeRequestID(value string) (string, bool) {
	if len(value) != 18 || !strings.HasPrefix(value, "h_") {
		return "", false
	}
	for _, character := range value[2:] {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return "", false
		}
	}
	return value, true
}

func browserSafeNumber(value interface{}) (interface{}, bool) {
	switch number := value.(type) {
	case int:
		return number, number >= 0
	case int32:
		return number, number >= 0
	case int64:
		return number, number >= 0
	case float64:
		return number, number >= 0
	case json.Number:
		parsed, err := number.Float64()
		return number, err == nil && parsed >= 0
	default:
		return nil, false
	}
}

func browserSafeIdentifier(value interface{}) (string, bool) {
	text, ok := value.(string)
	if !ok || text == "" || len(text) > 160 {
		return "", false
	}
	for _, character := range text {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			continue
		}
		return "", false
	}
	return text, true
}

func browserSafeHTTPMethod(value interface{}) (string, bool) {
	method, ok := value.(string)
	method = strings.ToUpper(strings.TrimSpace(method))
	if !ok || method == "" || len(method) > 16 {
		return "", false
	}
	for _, character := range method {
		if character < 'A' || character > 'Z' {
			return "", false
		}
	}
	return method, true
}

func emitBrowserResult(value interface{}, printText func()) error {
	if getOutputMode() == "json" {
		data, err := sonic.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	printText()
	return nil
}

func parseBrowserHeaders(values []string) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for _, value := range values {
		index := strings.Index(value, ":")
		if index <= 0 {
			return nil, fmt.Errorf("invalid header %q (want 'Name: value')", value)
		}
		name := strings.TrimSpace(value[:index])
		if name == "" {
			return nil, fmt.Errorf("invalid empty header name")
		}
		result[name] = strings.TrimSpace(value[index+1:])
	}
	return result, nil
}

func readBrowserURLArgument(value string) (string, error) {
	raw, err := readBrowserArgument(value)
	if err != nil {
		return "", err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("URL must not be empty")
	}
	return raw, nil
}

func browserURLArgumentIsSensitive(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), "@")
}

func archiveBrowserCapture(note string) (*store.Session, error) {
	livePath, err := store.GetLiveFilePath()
	if err != nil {
		return nil, err
	}
	export, err := loadLiveExport(livePath)
	if err != nil {
		return nil, err
	}
	if len(export.Requests) == 0 {
		return nil, fmt.Errorf("browser capture produced no requests")
	}
	persistent, err := store.Get()
	if err != nil {
		return nil, err
	}
	session, err := persistent.AddSession(store.GenerateSessionID(note), note, export.Requests)
	if err != nil {
		return nil, err
	}
	if err := persistent.Save(); err != nil {
		return nil, err
	}
	return session, nil
}

func nonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "unknown"
}

// Avoid importing errors only for one type assertion helper; this also works
// for wrapped RPC errors.
func errorsAs(err error, target interface{}) bool {
	switch typed := target.(type) {
	case **bridge.RPCError:
		for err != nil {
			if value, ok := err.(*bridge.RPCError); ok {
				*typed = value
				return true
			}
			type unwrapper interface{ Unwrap() error }
			wrapped, ok := err.(unwrapper)
			if !ok {
				break
			}
			err = wrapped.Unwrap()
		}
	}
	return false
}

func bindOpenFlags(command *cobra.Command, flags *browserOpenFlags, includeBrowser bool) {
	if includeBrowser {
		command.Flags().StringVar(&flags.Browser, "browser", "arc", "Browser bridge to use: arc, chrome, or any")
	}
	command.Flags().IntVar(&flags.TabID, "tab", -1, "Reuse a specific tab ID instead of creating a temporary tab")
	command.Flags().StringVar(&flags.Referrer, "referrer", "", "Optional HTTP(S) referrer for Page.navigate")
	command.Flags().BoolVar(&flags.Active, "active", false, "Activate the navigation tab")
	command.Flags().BoolVar(&flags.KeepTab, "keep-tab", false, "Keep a newly created tab after capture")
	command.Flags().DurationVar(&flags.Timeout, "timeout", 30*time.Second, "Maximum navigation/capture duration")
	command.Flags().DurationVar(&flags.Idle, "idle", 800*time.Millisecond, "Required network-idle interval")
	command.Flags().IntVar(&flags.MaxBodyBytes, "max-body", 384*1024, "Maximum captured response-body bytes per request")
	command.Flags().BoolVar(&flags.Save, "save", false, "Archive the capture after completion")
	command.Flags().StringVar(&flags.Note, "note", "", "Archive note (with --save)")
}

func init() {
	rootCmd.AddCommand(browserCmd)
	rootCmd.AddCommand(browseCmd)
	browserCmd.AddCommand(browserStatusCmd, browserTabsCmd, browserOpenCmd, browserFetchCmd, browserCloseCmd, browserWatchCmd)
	browserWatchCmd.AddCommand(browserWatchStartCmd, browserWatchStopCmd, browserWatchStatusCmd)
	browserCmd.PersistentFlags().StringVar(&browserSelector, "browser", "arc", "Browser bridge to use: arc, chrome, or any")
	bindOpenFlags(browserOpenCmd, &openFlags, false)
	bindOpenFlags(browseCmd, &browseFlags, true)

	browserFetchCmd.Flags().IntVar(&fetchFlags.TabID, "tab", -1, "Use a specific matching-origin tab")
	browserFetchCmd.Flags().StringVarP(&fetchFlags.Method, "method", "X", "GET", "HTTP method")
	browserFetchCmd.Flags().StringArrayVarP(&fetchFlags.Headers, "header", "H", nil, "Request header 'Name: value' (repeatable)")
	browserFetchCmd.Flags().StringVarP(&fetchFlags.Body, "data", "d", "", "Request body or @path")
	browserFetchCmd.Flags().StringVar(&fetchFlags.Credentials, "credentials", "include", "Fetch credentials mode: include, same-origin, omit")
	browserFetchCmd.Flags().StringVar(&fetchFlags.Cache, "cache", "default", "Fetch cache mode (default preserves normal browser behavior)")
	browserFetchCmd.Flags().BoolVar(&fetchFlags.HeadersOnly, "headers-only", false, "Cancel the response body after headers while retaining the captured request")
	browserFetchCmd.Flags().BoolVar(&fetchFlags.KeepTab, "keep-tab", false, "Keep a temporary origin tab")
	browserFetchCmd.Flags().DurationVar(&fetchFlags.Timeout, "timeout", 30*time.Second, "Maximum fetch/capture duration")
	browserFetchCmd.Flags().IntVar(&fetchFlags.MaxBodyBytes, "max-body", 384*1024, "Maximum captured/returned response-body bytes")
	browserFetchCmd.Flags().BoolVar(&fetchFlags.Save, "save", false, "Archive the capture after completion")
	browserFetchCmd.Flags().StringVar(&fetchFlags.Note, "note", "", "Archive note (with --save)")
}

var _ = json.RawMessage{}
