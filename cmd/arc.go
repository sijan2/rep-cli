package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/cdp"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/spf13/cobra"
)

const (
	arcAppPath          = "/Applications/Arc.app"
	defaultArcDebugPort = 9222
)

var (
	arcDebugPort         int
	arcDebugOrigin       string
	arcTimeout           time.Duration
	arcCDPParams         string
	arcCDPTargetID       string
	arcLaunchRestart     bool
	arcReloadExtensionID string
)

var arcCmd = &cobra.Command{
	Use:   "arc",
	Short: "Inspect and control Arc's native Chromium DevTools endpoint",
	Long: `Operate Arc at the Chromium browser-process layer.

This backend talks to ArcCore's real DevTools server. It complements the rep+
extension bridge: browser-level CDP covers Target/Browser commands and can
reload the unpacked extension without UI automation, while rep browser uses
chrome.debugger for signed-in tab capture and renderer control.`,
}

var arcStatusCmd = &cobra.Command{
	Use:     "status",
	Aliases: []string{"inspect"},
	Short:   "Inspect Arc build, process flags, and browser-level CDP",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), arcTimeout)
		defer cancel()
		result := inspectArc(ctx, arcDebugPort)
		return emitBrowserResult(result, func() { printBrowserJSON(result) })
	},
}

var arcTargetsCmd = &cobra.Command{
	Use:   "targets",
	Short: "List targets from ArcCore's browser-process DevTools server",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), arcTimeout)
		defer cancel()
		targets, err := cdp.GetTargets(ctx, arcDebugPort)
		if err != nil {
			return emitArcError("arc targets", err)
		}
		result := map[string]interface{}{"port": arcDebugPort, "targets": targets}
		return emitBrowserResult(result, func() { printBrowserJSON(result) })
	},
}

var arcCDPCmd = &cobra.Command{
	Use:   "cdp <Domain.command>",
	Short: "Send a browser-level CDP command directly to ArcCore",
	Long: `Send a CDP command to Arc's browser target.

Examples:
  rep arc cdp Browser.getVersion
  rep arc cdp Target.getTargets
  rep arc cdp Target.createTarget --params '{"url":"https://github.com","background":true}'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params, err := readJSONObject(arcCDPParams)
		if err != nil {
			return emitBrowserArgumentError("arc cdp", err)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), arcTimeout)
		defer cancel()
		version, err := cdp.GetVersion(ctx, arcDebugPort)
		if err != nil {
			return emitArcError("arc cdp", err)
		}
		webSocketURL := version.WebSocketDebuggerURL
		scope := "browser"
		targetID := strings.TrimSpace(arcCDPTargetID)
		if targetID != "" {
			targets, targetsErr := cdp.GetTargets(ctx, arcDebugPort)
			if targetsErr != nil {
				return emitArcError("arc cdp", targetsErr)
			}
			webSocketURL = ""
			for _, target := range targets {
				if target.ID == targetID {
					webSocketURL = target.WebSocketDebuggerURL
					break
				}
			}
			if webSocketURL == "" {
				return emitArcError("arc cdp", fmt.Errorf("target %s has no live WebSocket endpoint", targetID))
			}
			scope = "target"
		}
		result, err := cdp.Call(ctx, webSocketURL, effectiveArcOrigin(), args[0], params)
		if err != nil {
			return emitArcError("arc cdp", err)
		}
		payload := map[string]interface{}{
			"browser": version.Browser,
			"method":  args[0],
			"scope":   scope,
			"result":  json.RawMessage(result),
		}
		if targetID != "" {
			payload["target_id"] = targetID
		}
		return emitBrowserResult(payload, func() { printBrowserJSON(payload) })
	},
}

var arcReloadExtensionCmd = &cobra.Command{
	Use:   "reload-extension",
	Short: "Reload the unpacked rep+ extension through its service-worker CDP target",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), arcTimeout)
		defer cancel()
		extensionID := strings.TrimSpace(arcReloadExtensionID)
		if extensionID == "" {
			var err error
			extensionID, err = installedArcExtensionID()
			if err != nil {
				return emitArcError("arc reload-extension", err)
			}
		}
		result, err := reloadArcExtension(ctx, arcDebugPort, extensionID)
		if err != nil {
			return emitArcError("arc reload-extension", err)
		}
		return emitBrowserResult(result, func() { printBrowserJSON(result) })
	},
}

var arcLaunchCmd = &cobra.Command{
	Use:   "launch",
	Short: "Launch Arc with a loopback browser-process CDP endpoint",
	Long: `Launch Arc without --headless or --enable-automation.

The fixed nonzero DevTools port preserves the ordinary Arc renderer identity.
If Arc is already running with different flags, pass --restart for a graceful
quit and relaunch; its normal session restoration remains in effect.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), arcTimeout+15*time.Second)
		defer cancel()
		result, err := launchArcDevTools(ctx, arcDebugPort, effectiveArcOrigin(), arcLaunchRestart)
		if err != nil {
			return emitArcError("arc launch", err)
		}
		return emitBrowserResult(result, func() { printBrowserJSON(result) })
	},
}

func inspectArc(ctx context.Context, port int) map[string]interface{} {
	result := map[string]interface{}{
		"app_path": arcAppPath,
		"port":     port,
		"origin":   effectiveArcOrigin(),
	}
	for key, plistKey := range map[string]string{
		"arc_version": "CFBundleShortVersionString",
		"arc_build":   "CFBundleVersion",
		"arc_commit":  "BCNYCommitInfo",
		"arc_core":    "ArcCoreVersion",
	} {
		if value, err := plistValue(ctx, plistKey); err == nil && value != "" {
			result[key] = value
		}
	}
	if pid, args, err := arcProcess(ctx); err == nil {
		result["running"] = true
		result["pid"] = pid
		result["process_args"] = args
	} else {
		result["running"] = false
	}
	version, versionErr := cdp.GetVersion(ctx, port)
	if versionErr != nil {
		result["remote_debugging"] = false
		result["remote_debugging_error"] = versionErr.Error()
		return result
	}
	result["remote_debugging"] = true
	result["protocol"] = version.ProtocolVersion
	result["chromium"] = version.Browser
	result["v8"] = version.V8Version
	result["user_agent"] = version.UserAgent
	result["browser_websocket"] = version.WebSocketDebuggerURL
	if targets, err := cdp.GetTargets(ctx, port); err == nil {
		counts := map[string]int{}
		extensions := make([]map[string]string, 0)
		for _, target := range targets {
			counts[target.Type]++
			if strings.HasPrefix(target.URL, "chrome-extension://") {
				extensions = append(extensions, map[string]string{"id": extensionIDFromURL(target.URL), "type": target.Type})
			}
		}
		result["target_counts"] = counts
		result["extension_targets"] = extensions
	}
	return result
}

func launchArcDevTools(ctx context.Context, port int, origin string, restart bool) (map[string]interface{}, error) {
	if _, args, err := arcProcess(ctx); err == nil {
		wantedPort := "--remote-debugging-port=" + strconv.Itoa(port)
		wantedOrigin := "--remote-allow-origins=" + origin
		if strings.Contains(args, wantedPort) && strings.Contains(args, wantedOrigin) {
			return inspectArc(ctx, port), nil
		}
		if !restart {
			return nil, fmt.Errorf("Arc is already running without the requested DevTools flags; rerun with --restart")
		}
		quit := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", `tell application "Arc" to quit`)
		if output, err := quit.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("quit Arc: %w: %s", err, strings.TrimSpace(string(output)))
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, _, err := arcProcess(ctx); err != nil {
				break
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		if _, _, err := arcProcess(ctx); err == nil {
			return nil, fmt.Errorf("Arc did not exit after a graceful quit")
		}
	}
	arguments := []string{
		"-na", arcAppPath, "--args",
		"--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port=" + strconv.Itoa(port),
		"--remote-allow-origins=" + origin,
	}
	launch := exec.CommandContext(ctx, "/usr/bin/open", arguments...)
	if output, err := launch.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("launch Arc: %w: %s", err, strings.TrimSpace(string(output)))
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		_, err := cdp.GetVersion(probeCtx, port)
		cancel()
		if err == nil {
			return inspectArc(ctx, port), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("Arc did not expose CDP on 127.0.0.1:%d", port)
}

func reloadArcExtension(ctx context.Context, port int, extensionID string) (map[string]interface{}, error) {
	targets, err := cdp.GetTargets(ctx, port)
	if err != nil {
		return nil, err
	}
	var target cdp.Target
	for _, candidate := range targets {
		if extensionIDFromURL(candidate.URL) == extensionID && candidate.WebSocketDebuggerURL != "" {
			target = candidate
			break
		}
	}
	if target.ID == "" {
		return nil, fmt.Errorf("no live service-worker target for extension %s", extensionID)
	}
	_, callErr := cdp.Call(ctx, target.WebSocketDebuggerURL, effectiveArcOrigin(), "Runtime.evaluate", map[string]interface{}{
		"expression":    "chrome.runtime.reload(); true",
		"returnByValue": true,
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		current, listErr := cdp.GetTargets(ctx, port)
		if listErr == nil {
			for _, candidate := range current {
				if extensionIDFromURL(candidate.URL) == extensionID && candidate.ID != target.ID {
					return map[string]interface{}{
						"reloaded": true, "extension_id": extensionID,
						"old_target_id": target.ID, "new_target_id": candidate.ID,
					}, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if callErr != nil {
		return nil, callErr
	}
	return nil, fmt.Errorf("extension target did not restart after reload")
}

func installedArcExtensionID() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, "Library", "Application Support", "Arc", "NativeMessagingHosts", "com.repplus.host.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var manifest struct {
		AllowedOrigins []string `json:"allowed_origins"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", err
	}
	for _, origin := range manifest.AllowedOrigins {
		if id := extensionIDFromURL(origin); id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("native host manifest has no Chrome extension origin")
}

func extensionIDFromURL(value string) string {
	const prefix = "chrome-extension://"
	if !strings.HasPrefix(value, prefix) {
		return ""
	}
	remainder := strings.TrimPrefix(value, prefix)
	if index := strings.IndexByte(remainder, '/'); index >= 0 {
		remainder = remainder[:index]
	}
	return remainder
}

func arcProcess(ctx context.Context) (int, string, error) {
	command := exec.CommandContext(ctx, "/usr/bin/pgrep", "-x", "Arc")
	data, err := command.Output()
	if err != nil {
		return 0, "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, "", fmt.Errorf("Arc is not running")
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, "", err
	}
	argsCommand := exec.CommandContext(ctx, "/bin/ps", "-ww", "-p", strconv.Itoa(pid), "-o", "args=")
	args, err := argsCommand.Output()
	if err != nil {
		return 0, "", err
	}
	return pid, strings.TrimSpace(string(args)), nil
}

func plistValue(ctx context.Context, key string) (string, error) {
	path := filepath.Join(arcAppPath, "Contents", "Info.plist")
	command := exec.CommandContext(ctx, "/usr/bin/plutil", "-extract", key, "raw", "-o", "-", path)
	data, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func effectiveArcOrigin() string {
	if strings.TrimSpace(arcDebugOrigin) != "" {
		return strings.TrimSpace(arcDebugOrigin)
	}
	return cdp.Origin(arcDebugPort)
}

func emitArcError(command string, err error) error {
	return output.EmitAgentError(os.Stdout, output.NewAgentError(
		output.ErrCodeBrowserRPC,
		command,
		err.Error(),
		fmt.Sprintf("rep arc launch --port %d --restart", arcDebugPort),
		fmt.Sprintf("rep arc status --port %d", arcDebugPort),
	), getOutputMode() == "json")
}

func init() {
	rootCmd.AddCommand(arcCmd)
	arcCmd.AddCommand(arcStatusCmd, arcTargetsCmd, arcCDPCmd, arcReloadExtensionCmd, arcLaunchCmd)
	arcCmd.PersistentFlags().IntVar(&arcDebugPort, "port", defaultArcDebugPort, "ArcCore loopback DevTools port")
	arcCmd.PersistentFlags().StringVar(&arcDebugOrigin, "origin", "", "WebSocket Origin allowed by Arc (default: loopback port URL)")
	arcCmd.PersistentFlags().DurationVar(&arcTimeout, "timeout", 10*time.Second, "Maximum command duration")
	arcCDPCmd.Flags().StringVar(&arcCDPParams, "params", "{}", "CDP parameter JSON object or @path")
	arcCDPCmd.Flags().StringVar(&arcCDPTargetID, "target", "", "Page/worker target ID (default: browser target)")
	arcLaunchCmd.Flags().BoolVar(&arcLaunchRestart, "restart", false, "Gracefully restart Arc if its running flags differ")
	arcReloadExtensionCmd.Flags().StringVar(&arcReloadExtensionID, "extension-id", "", "Extension ID (default: from Arc native-host manifest)")
}
