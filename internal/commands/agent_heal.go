package commands

import (
	"context"
	"errors"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

func coreVerificationNext(command *cobra.Command) string {
	if tab := agentstate.TabOf(invocationHandle(command, command.Flags().Args())); tab != "" {
		return coreNext("page", tab)
	}
	if store, err := coreStore(); err == nil {
		state, err := store.Read()
		selected, _ := scope.Current()
		handle := ""
		if err == nil {
			for _, tab := range state.Tabs {
				if tab.Owner == selected.Identity() && !tab.Closed {
					if handle != "" {
						return coreNext("tabs")
					}
					handle = tab.Handle
				}
			}
		}
		if handle != "" {
			return coreNext("page", handle)
		}
	}
	return coreNext("tabs")
}

func resolveCoreBrowser(ctx context.Context, store *agentstate.Store, tab *agentstate.Tab) (*bridge.Client, agentstate.Browser, error) {
	selected, _ := scope.Current()
	owner := selected.Identity()
	recent := store.RecentlyLive(owner)
	client, browser, err := resolveCoreBrowserOnce(ctx, store, tab)
	var problem *agentstate.Error
	if errors.As(err, &problem) && problem.Code == "browser_unavailable" {
		_ = store.Health(owner, false)
		if recent && ctx.Err() == nil {
			wait, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			for wait.Err() == nil {
				select {
				case <-wait.Done():
				case <-time.After(100 * time.Millisecond):
				}
				if wait.Err() != nil {
					break
				}
				client, browser, err = resolveCoreBrowserOnce(wait, store, tab)
				if err == nil {
					break
				}
				if !errors.As(err, &problem) || problem.Code != "browser_unavailable" {
					break
				}
			}
		}
	}
	if err != nil {
		return nil, browser, err
	}
	_ = store.Health(owner, true)
	client.Health = func(live bool) { _ = store.Health(owner, live) }
	client.Reconnect = func(wait context.Context, previous bridge.Registry) (bridge.Registry, error) {
		if !store.RecentlyLive(owner) {
			return bridge.Registry{}, errors.New("bridge has not been live recently")
		}
		for {
			recovered, identity, e := resolveCoreBrowserOnce(wait, store, tab)
			if e == nil {
				if identity.Profile != previous.ProfileID || identity.Epoch != previous.Epoch {
					return bridge.Registry{}, errors.New("browser session changed during recovery")
				}
				return recovered.Registry, nil
			}
			select {
			case <-wait.Done():
				return bridge.Registry{}, wait.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return client, browser, nil
}
