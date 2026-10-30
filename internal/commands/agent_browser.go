package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/headless"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

func privateBrowserName() string {
	name := strings.TrimSpace(os.Getenv("REP_PROFILE"))
	if name == "" {
		name = "default"
	}
	return name
}
func corePrivateOptions(headed bool) headless.Options {
	executable, _ := os.Executable()
	return headless.Options{Headed: headed, Extension: headless.ConfiguredExtension(), Binary: strings.TrimSpace(os.Getenv("REP_HEADLESS_BINARY")), Host: filepath.Join(filepath.Dir(executable), "rep-host")}
}
func newCoreBrowserStartCommand() *cobra.Command {
	headed := false
	command := &cobra.Command{Use: "start [arc|chrome|dia]", Short: "Start a private browser or remember a real browser choice", Args: cobra.MaximumNArgs(1)}
	command.Flags().BoolVar(&headed, "headed", false, "Open the private profile for human sign-in")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := coreBrowserStart(cmd, args, headed); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}
func coreBrowserStart(cmd *cobra.Command, args []string, headed bool) error {
	store, err := coreStore()
	if err != nil {
		return err
	}
	selected, _ := scope.Current()
	if len(args) > 0 {
		name := args[0]
		if name != "arc" && name != "chrome" && name != "dia" {
			return coreArgument("browser name must be arc, chrome, or dia")
		}
		if headed {
			return coreArgument("--headed applies only to the private profile")
		}
		browser := agentstate.Browser{Name: name}
		previous, err := store.Remembered(selected.Identity())
		if err != nil {
			return err
		}
		if previous != nil && previous.Name == name {
			browser = *previous
		}
		if err := store.Remember(selected.Identity(), selected.SessionKey, browser); err != nil {
			return err
		}
		_ = store.Journal(selected.Identity(), "", "browser.start", "selected")
		return emitCore(cmd, coreResult{Browser: name, Profile: browser.Profile, Status: "selected", Next: coreNext("tabs"), Meta: map[string]any{"running": "not_probed", "launched": false}})
	}
	name := privateBrowserName()
	browser := agentstate.Browser{Name: "headless", PrivateName: name}
	state, err := store.Read()
	if err != nil {
		return err
	}
	for _, tab := range state.Tabs {
		if tab.Owner == selected.Identity() && !tab.Closed && (tab.Browser.Name != "headless" || tab.Browser.PrivateName != name) {
			return agentstate.Failure("browser_in_use", "task still owns tabs in another browser", coreNext("tabs"))
		}
	}
	dir, err := headless.NamedProfileDir(name)
	if err != nil {
		return err
	}
	ctx, cancel := coreTimeout(cmd, 25*time.Second)
	defer cancel()
	current, err := headless.Inspect(dir)
	if err != nil {
		return err
	}
	options := corePrivateOptions(headed)
	restart := current.Running && current.Headed != headed
	if current.Running {
		extension := options.Extension
		if extension == "" {
			extension = current.Extension
		}
		if digest, e := headless.ExtensionDigest(extension); e == nil && digest != current.ExtensionDigest {
			restart = true
		}
	}
	var status headless.Status
	if !current.Running || restart {
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
		if restart {
			status, err = headless.Restart(ctx, dir, options)
			closed = err == nil
			if err != nil {
				after, e := headless.Inspect(dir)
				closed = e == nil && (!after.Running || after.PID != current.PID)
			}
		} else {
			status, err = headless.Start(ctx, dir, options)
		}
		if err != nil {
			return err
		}
		// Release before remembering the new epoch or allowing normal tab leases.
		if err = store.EndLifecycle(browser, reservation, closed); err != nil {
			return err
		}
		reservation = ""
	} else {
		status, err = headless.Start(ctx, dir, options)
		if err != nil {
			return err
		}
	}
	client, err := headless.Connection(ctx, dir)
	if err != nil {
		return err
	}
	browser = browserIdentity(client, "headless", name)
	if err = store.ObserveEpoch(browser.Profile, browser.Epoch); err != nil {
		return err
	}
	if err = store.Remember(selected.Identity(), selected.SessionKey, browser); err != nil {
		return err
	}
	_ = store.Health(selected.Identity(), true)
	_ = store.Journal(selected.Identity(), "", "browser.start", "ready")
	return emitCore(cmd, coreResult{Browser: "headless", Profile: browser.Profile, Status: "ready", Next: coreNext("tabs"), Meta: map[string]any{"running": status.Running, "profile": name, "headed": status.Headed, "restarted": restart}})
}
func newCoreBrowserStopCommand() *cobra.Command {
	command := &cobra.Command{Use: "stop", Short: "Stop the private browser when no other task holds it", Args: cobra.NoArgs}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := coreBrowserStop(cmd); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}
func coreBrowserStop(cmd *cobra.Command) error {
	store, err := coreStore()
	if err != nil {
		return err
	}
	selected, _ := scope.Current()
	browser, err := store.Remembered(selected.Identity())
	if err != nil {
		return err
	}
	if browser == nil {
		browser = &agentstate.Browser{Name: "headless", PrivateName: privateBrowserName()}
	}
	if browser.Name != "headless" {
		return agentstate.Failure("real_browser", "stop only controls a private browser", coreNext("tabs"))
	}
	if browser.PrivateName == "" {
		browser.PrivateName = privateBrowserName()
	}
	if selected.TaskSource != "flag" && strings.HasPrefix(selected.SessionKey, "agent-") {
		state, e := store.Read()
		if e != nil {
			return e
		}
		for _, tab := range state.Tabs {
			if tab.Owner == selected.Identity() && !tab.Closed {
				return agentstate.Failure("ambiguous_target", "pin the task before stopping a profile with owned tabs", coreNext("tabs"))
			}
		}
	}
	reservation, err := store.BeginLifecycle(*browser, selected.Identity(), selected.SessionKey, 15*time.Second)
	if err != nil {
		return err
	}
	closed := false
	defer func() { _ = store.EndLifecycle(*browser, reservation, closed) }()
	dir, err := headless.NamedProfileDir(browser.PrivateName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()
	if _, err = headless.Stop(ctx, dir); err != nil {
		return err
	}
	closed = true
	_ = store.Health(selected.Identity(), false)
	_ = store.Journal(selected.Identity(), "", "browser.stop", "stopped")
	return emitCore(cmd, coreResult{Browser: browser.Name, Profile: browser.Profile, Status: "stopped", Next: coreNext("browser", "start"), Meta: map[string]any{"running": false, "profile": browser.PrivateName}})
}
func init() { browserCmd.AddCommand(newCoreBrowserStartCommand(), newCoreBrowserStopCommand()) }
