package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/headless"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

func newCoreDoctorCommand() *cobra.Command {
	command := &cobra.Command{Use: "doctor", Short: "Repair the selected browser bridge without interrupting other tasks", Args: cobra.NoArgs}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := coreDoctor(cmd); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}

type coreDoctorOptions struct {
	host          string
	manifestPaths func(string) ([]string, error)
	reconnectWait time.Duration
}

func coreDoctor(cmd *cobra.Command) error {
	return coreDoctorWith(cmd, coreDoctorOptions{corePrivateOptions(false).Host, nativeManifestPaths, 5 * time.Second})
}
func coreDoctorWith(cmd *cobra.Command, options coreDoctorOptions) error {
	store, err := coreStore()
	if err != nil {
		return err
	}
	selected, _ := scope.Current()
	remembered, err := store.Remembered(selected.Identity())
	if err != nil {
		return err
	}
	browser := agentstate.Browser{Name: os.Getenv("REP_BROWSER")}
	if browser.Name == "" {
		browser.Name = "dia"
	}
	if remembered != nil {
		browser = *remembered
	}
	fixed := []string{}
	human := ""
	ctx, cancel := coreTimeout(cmd, 30*time.Second)
	defer cancel()
	// Resolve first so an unpinned sole bridge or a profile-ID selector repairs
	// that browser's manifest rather than the default browser's manifest.
	if browser.Name != "headless" {
		probe, cancel := context.WithTimeout(ctx, time.Second)
		_, identity, probeErr := resolveCoreBrowserOnce(probe, store, nil)
		cancel()
		if probeErr == nil {
			browser = identity
		} else {
			var problem *agentstate.Error
			if errors.As(probeErr, &problem) && problem.Code != "browser_unavailable" {
				return probeErr
			}
		}
	}
	var local headless.Status
	if browser.Name == "headless" {
		if browser.PrivateName == "" {
			browser.PrivateName = privateBrowserName()
		}
		dir, e := headless.NamedProfileDir(browser.PrivateName)
		if e != nil {
			return e
		}
		local, err = headless.Inspect(dir)
		if err != nil {
			return err
		}
		if local.Reason == "not_started" {
			result := coreResult{Browser: browser.Name, Status: "browser_unavailable", Error: &coreError{Code: "browser_unavailable", Message: "the private profile has not been started", Next: coreNext("browser", "start"), Retry: "needs-agent"}, Meta: map[string]any{"healthy": false, "fixed": fixed, "bridges": []any{}, "blocking_frames": []any{}}}
			if e := emitCore(cmd, result); e != nil {
				return e
			}
			return output.MarkReported(output.NewAgentError("browser_unavailable", cmd.CommandPath(), "the private profile has not been started"))
		}
		if changed, e := repairCoreManifest(filepath.Join(local.Profile, "NativeMessagingHosts", "com.repplus.host.json"), options.host, local.ExtensionID); e != nil {
			return e
		} else if changed {
			fixed = append(fixed, "native_manifest")
		}
	} else {
		extension := coreExtensionPath()
		if extension == "" {
			human = "Set REP_EXTENSION_PATH to the installed unpacked extension directory."
		} else {
			id, e := deriveExtensionID(extension)
			if e != nil {
				return e
			}
			host := options.host
			paths, e := options.manifestPaths(browser.Name)
			if e != nil {
				return e
			}
			for _, path := range paths {
				if changed, e := repairCoreManifest(path, host, id); e != nil {
					return e
				} else if changed {
					fixed = append(fixed, "native_manifest")
				}
			}
		}
	}
	client, identity, err := waitCoreBrowser(ctx, store, options.reconnectWait)
	if err == nil {
		browser = identity
	}
	if browser.Name == "headless" {
		stale := false
		if digest, e := headless.ExtensionDigest(local.Extension); e == nil {
			stale = digest != local.ExtensionDigest || (client != nil && client.Registry.BuildDigest != "" && client.Registry.BuildDigest != digest)
		}
		if err != nil || stale {
			reservation, e := store.BeginLifecycle(browser, selected.Identity(), selected.SessionKey, 30*time.Second)
			if e != nil {
				return e
			}
			closed := false
			defer func() {
				if reservation != "" {
					_ = store.EndLifecycle(browser, reservation, closed)
				}
			}()
			if client != nil {
				if e = coreReloadGuard(ctx, client); e != nil {
					return e
				}
			}
			dir, e := headless.NamedProfileDir(browser.PrivateName)
			if e != nil {
				return e
			}
			_, err = headless.Restart(ctx, dir, corePrivateOptions(local.Headed))
			closed = err == nil
			if err != nil {
				after, e := headless.Inspect(dir)
				closed = e == nil && (!after.Running || after.PID != local.PID)
			}
			if err != nil {
				return err
			}
			if err = store.EndLifecycle(browser, reservation, true); err != nil {
				return err
			}
			reservation = ""
			// A restart changes epoch. Retain profile selection but invalidate old tabs.
			client, err = headless.Connection(ctx, dir)
			if err != nil {
				return err
			}
			browser = browserIdentity(client, "headless", browser.PrivateName)
			if err = store.ObserveEpoch(browser.Profile, browser.Epoch); err != nil {
				return err
			}
			if err = store.Remember(selected.Identity(), selected.SessionKey, browser); err != nil {
				return err
			}
			fixed = append(fixed, "private_browser_restarted")
		}
	} else if err == nil {
		digest, e := headless.ExtensionDigest(coreExtensionPath())
		if e == nil && client.Registry.BuildDigest != "" && client.Registry.BuildDigest != digest {
			reservation, e := store.BeginLifecycle(browser, selected.Identity(), selected.SessionKey, 15*time.Second)
			if e != nil {
				return e
			}
			defer store.EndLifecycle(browser, reservation, false)
			if e = coreReloadGuard(ctx, client); e != nil {
				return e
			}
			var ignored any
			if e = client.Call(ctx, "browser.reload-extension", nil, &ignored); e != nil {
				return e
			}
			previous := client.Registry
			client, err = waitCoreReplacement(ctx, store, previous, digest, options.reconnectWait)
			if err != nil {
				human = "Open the selected browser's extension manager and reload rep+."
			} else {
				fixed = append(fixed, "extension_reloaded")
			}
		}
	}
	healthy := err == nil && client != nil
	_ = store.Health(selected.Identity(), healthy)
	status, next := "ready", coreNext("tabs")
	if !healthy {
		status = "needs-human"
		next = coreNext("doctor")
		if human == "" {
			human = "Open the selected browser, enable rep+ in its extension manager, and reload the extension."
		}
	}
	bridges := []any{}
	if client != nil {
		bridges = append(bridges, map[string]any{"browser": browser.Name, "profile_id": browser.Profile, "build_digest": client.Registry.BuildDigest, "connected": healthy})
	}
	meta := map[string]any{"healthy": healthy, "fixed": fixed, "bridges": bridges, "blocking_frames": []any{}}
	if human != "" {
		meta["human_step"] = human
	}
	_ = store.Journal(selected.Identity(), "", "doctor", status)
	result := coreResult{Browser: browser.Name, Profile: browser.Profile, Status: status, Next: next, Meta: meta}
	if !healthy {
		result.Next = ""
		result.Error = &coreError{Code: "browser_unavailable", Message: human, Next: next, Retry: "needs-human"}
	}
	if err := emitCore(cmd, result); err != nil {
		return err
	}
	if !healthy {
		return output.MarkReported(output.NewAgentError("browser_unavailable", cmd.CommandPath(), human))
	}
	return nil
}
func waitCoreBrowser(ctx context.Context, store *agentstate.Store, budget time.Duration) (*bridge.Client, agentstate.Browser, error) {
	wait, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var client *bridge.Client
	var browser agentstate.Browser
	var err error
	for {
		client, browser, err = resolveCoreBrowserOnce(wait, store, nil)
		if err == nil {
			return client, browser, nil
		}
		var problem *agentstate.Error
		if !errors.As(err, &problem) || problem.Code != "browser_unavailable" {
			return nil, browser, err
		}
		select {
		case <-wait.Done():
			return nil, browser, err
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func waitCoreReplacement(ctx context.Context, store *agentstate.Store, previous bridge.Registry, expectedDigest string, budget time.Duration) (*bridge.Client, error) {
	wait, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	for {
		client, _, err := resolveCoreBrowserOnce(wait, store, nil)
		if err == nil && client.Registry.ProfileID == previous.ProfileID && client.Registry.BuildDigest == expectedDigest {
			return client, nil
		}
		select {
		case <-wait.Done():
			return nil, wait.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func coreReloadGuard(ctx context.Context, client *bridge.Client) error {
	var status struct {
		Active int `json:"active_captures"`
		Queued int `json:"queued_captures"`
		Leases int `json:"active_leases"`
	}
	if err := client.Call(ctx, "browser.status", nil, &status); err != nil {
		return err
	}
	if status.Active > 0 || status.Queued > 0 || status.Leases > 0 {
		return agentstate.Failure("browser_in_use", "browser has an active capture or execution lease", coreNext("tabs"))
	}
	return nil
}
func coreExtensionPath() string { return headless.ConfiguredExtension() }

func repairCoreManifest(path, host, extensionID string) (bool, error) {
	info, err := os.Stat(host)
	if err != nil || info.IsDir() || info.Mode()&0111 == 0 {
		return false, agentstate.Failure("host_missing", "build or install rep-host beside rep before repair", coreNext("scope"))
	}
	if extensionID == "" {
		return false, coreArgument("extension identity is unavailable")
	}
	target := nativeHostManifest{Name: "com.repplus.host", Description: "rep+ Native Messaging Host for CLI browser control", Path: host, Type: "stdio", AllowedOrigins: mergeAllowedOrigins("chrome-extension://"+extensionID+"/", path)}
	previous, _ := os.ReadFile(path)
	var old nativeHostManifest
	if json.Unmarshal(previous, &old) == nil && old.Name == target.Name && old.Path == target.Path && old.Type == target.Type && len(old.AllowedOrigins) == len(target.AllowedOrigins) {
		same := true
		for i := range old.AllowedOrigins {
			same = same && old.AllowedOrigins[i] == target.AllowedOrigins[i]
		}
		if same {
			return false, nil
		}
	}
	data, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return false, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return false, err
	}
	return true, atomicWriteSetupFile(path, append(data, '\n'), 0600)
}
func init() { rootCmd.AddCommand(newCoreDoctorCommand()) }
