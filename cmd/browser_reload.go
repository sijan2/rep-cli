package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/spf13/cobra"
)

var browserReloadWait time.Duration

var browserReloadCmd = &cobra.Command{
	Use:   "reload-extension",
	Short: "Reload rep+ through its existing Native Messaging bridge",
	Long: `Ask the connected rep+ background worker to reload itself after it
posts the RPC response, then wait for a responsive replacement bridge. This
loads changed unpacked-extension source without opening the extension manager
or requiring Arc's browser-process remote-debugging port.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if browserReloadWait < 0 || browserReloadWait > time.Minute {
			return emitBrowserArgumentError("browser reload-extension", errors.New("--wait must be between 0 and 1m"))
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		client, err := selectBrowserBridge(ctx, browserSelector, "browser reload-extension")
		if err != nil {
			cancel()
			return err
		}
		previousRegistry := client.Registry
		var result map[string]interface{}
		err = client.Call(ctx, "browser.reload-extension", nil, &result)
		cancel()
		if err != nil {
			return emitBrowserCallError("browser reload-extension", err)
		}
		result["previous_host_pid"] = previousRegistry.PID
		if browserReloadWait > 0 {
			registry, waitErr := waitForReplacementBridge(cmd.Context(), previousRegistry, browserReloadWait)
			if waitErr != nil {
				return emitBrowserCallError("browser reload-extension", waitErr)
			}
			result["reconnected"] = true
			result["native_host_pid"] = registry.PID
		}
		return emitBrowserResult(result, func() {
			fmt.Printf("extension reload: scheduled\n")
			if result["reconnected"] == true {
				fmt.Printf("bridge: reconnected (pid %v)\n", result["native_host_pid"])
			}
		})
	},
}

func waitForReplacementBridge(ctx context.Context, previous bridge.Registry, timeout time.Duration) (bridge.Registry, error) {
	if strings.TrimSpace(previous.ExtensionID) == "" {
		return bridge.Registry{}, errors.New("cannot safely identify a replacement browser bridge: the original bridge did not report an extension ID")
	}
	deadline := time.Now().Add(timeout)
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return bridge.Registry{}, ctx.Err()
	case <-timer.C:
	}
	for time.Now().Before(deadline) {
		registries, discoverErr := bridge.Discover()
		if discoverErr == nil {
			for _, registry := range replacementBridgeCandidates(previous, registries) {
				remaining := time.Until(deadline)
				if remaining <= 0 {
					break
				}
				probeTimeout := 750 * time.Millisecond
				if remaining < probeTimeout {
					probeTimeout = remaining
				}
				probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
				candidate := &bridge.Client{Registry: registry}
				var status interface{}
				err := candidate.Call(probeCtx, "bridge.ping", nil, &status)
				cancel()
				if err == nil {
					return candidate.Registry, nil
				}
			}
		}
		wait := 200 * time.Millisecond
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		if wait <= 0 {
			break
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return bridge.Registry{}, ctx.Err()
		case <-timer.C:
		}
	}
	return bridge.Registry{}, fmt.Errorf("replacement browser bridge did not reconnect within %s", timeout)
}

func replacementBridgeCandidates(previous bridge.Registry, registries []bridge.Registry) []bridge.Registry {
	candidates := make([]bridge.Registry, 0, len(registries))
	for _, candidate := range registries {
		if matchesReplacementBridge(previous, candidate) {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func matchesReplacementBridge(previous, candidate bridge.Registry) bool {
	if candidate.PID == previous.PID && candidate.StartedAt == previous.StartedAt {
		return false
	}
	if !sameBridgeIdentityValue(previous.Browser, candidate.Browser) ||
		!sameBridgeIdentityValue(previous.ExtensionID, candidate.ExtensionID) {
		return false
	}
	if previous.ParentPID > 0 && candidate.ParentPID != previous.ParentPID {
		return false
	}
	if previous.BrowserLabel != "" && !sameBridgeIdentityValue(previous.BrowserLabel, candidate.BrowserLabel) {
		return false
	}
	if previous.UserAgent != "" && strings.TrimSpace(candidate.UserAgent) != strings.TrimSpace(previous.UserAgent) {
		return false
	}
	return true
}

func sameBridgeIdentityValue(previous, candidate string) bool {
	previous = strings.TrimSpace(previous)
	return previous == "" || strings.EqualFold(previous, strings.TrimSpace(candidate))
}

func init() {
	browserCmd.AddCommand(browserReloadCmd)
	browserReloadCmd.Flags().DurationVar(&browserReloadWait, "wait", 15*time.Second, "Wait for a responsive replacement bridge (0 disables)")
}
