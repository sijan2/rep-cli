package commands

import (
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

func newCoreTabsCommand() *cobra.Command {
	command := &cobra.Command{Use: "tabs", Short: "List tabs and ownership in the selected profile", Args: cobra.NoArgs}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		store, err := coreStore()
		if err != nil {
			return coreFailure(cmd, err)
		}
		ctx, cancel := coreTimeout(cmd, 5*time.Second)
		defer cancel()
		client, browser, err := resolveCoreBrowser(ctx, store, nil)
		if err != nil {
			return coreFailure(cmd, err)
		}
		var live struct {
			Tabs []struct {
				ID     int    `json:"id"`
				URL    string `json:"url"`
				Title  string `json:"title"`
				Active bool   `json:"active"`
			} `json:"tabs"`
		}
		if err := client.Call(ctx, "browser.tabs", nil, &live); err != nil {
			return coreFailure(cmd, err)
		}
		ids := []int{}
		for _, item := range live.Tabs {
			ids = append(ids, item.ID)
		}
		if err := store.Inventory(browser, ids); err != nil {
			return coreFailure(cmd, err)
		}
		state, err := store.Read()
		if err != nil {
			return coreFailure(cmd, err)
		}
		selected, _ := scope.Current()
		rows := []map[string]any{}
		for _, item := range live.Tabs {
			row := map[string]any{"owner": "user", "active": item.Active, "url": item.URL, "title": item.Title}
			for _, tab := range state.Tabs {
				if !tab.Closed && tab.Browser.Profile == browser.Profile && tab.Browser.Epoch == browser.Epoch && tab.ChromeID == item.ID {
					row["id"] = tab.Handle
					row["owner"] = "other"
					if tab.Owner == selected.Identity() {
						row["owner"] = "mine"
					}
					row["last_seen"] = tab.LastUsed
					row["adoptable"] = store.Adoptable(state, tab) && tab.Owner != selected.Identity()
					row["live"] = true
					row["leased"] = tab.Busy(time.Now())
					row["created_by_rep"] = tab.CreatedByRep
					if tab.Owner == "" {
						row["owner"] = "user"
					} else if tab.Owner != selected.Identity() {
						row["owner_state"] = "live"
						if store.Adoptable(state, tab) {
							row["owner_state"] = "idle"
						}
					}
					break
				}
			}
			rows = append(rows, row)
		}
		return emitCore(cmd, coreResult{Browser: browser.Name, Profile: browser.Profile, Complete: boolPointer(true), Untrusted: map[string]any{"tabs": rows}})
	}
	return command
}
func newCoreCloseCommand() *cobra.Command {
	command := &cobra.Command{Use: "close [TAB]", Short: "Close an owned tab without affecting other tasks", Args: cobra.MaximumNArgs(1)}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := coreClose(cmd, args); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}
func coreClose(command *cobra.Command, args []string) error {
	store, err := coreStore()
	if err != nil {
		return err
	}
	target := ""
	var addressed *agentstate.Tab
	if len(args) > 0 {
		if !agentstate.TabPattern.MatchString(args[0]) {
			return coreArgument("close requires a tab handle")
		}
		target = args[0]
		addressed, err = store.Tab(target)
		if err != nil {
			return err
		}
		if !addressed.CreatedByRep {
			return agentstate.Failure("user_tab", "close refuses a tab that rep did not create", coreNext("tabs"))
		}
	}
	ctx, cancel := coreTimeout(command, 5*time.Second)
	defer cancel()
	client, browser, err := resolveCoreBrowser(ctx, store, addressed)
	if err != nil {
		return err
	}
	selected, _ := scope.Current()
	if target == "" && selected.TaskSource != "flag" {
		return agentstate.Failure("ambiguous_target", "address a tab to close it", coreNext("tabs"))
	}
	tab, lease, err := store.Reserve(browser, selected.Identity(), selected.SessionKey, target, selected.TaskSource == "flag", false, 6*time.Second)
	if err != nil {
		return err
	}
	defer store.Release(tab.Handle, lease)
	if !tab.CreatedByRep {
		return agentstate.Failure("user_tab", "close refuses a tab that rep did not create", coreNext("tabs"))
	}
	session := &browserfast.Session{Browser: client, Tab: tab.ChromeID, Owner: tab.Owner}
	defer session.Close()
	if err := session.Acquire(ctx); err != nil {
		return err
	}
	if err := session.CloseTab(ctx, ""); err != nil {
		return err
	}
	if err := store.Change(tab.Handle, lease, func(tab *agentstate.Tab) error { tab.Closed = true; return nil }); err != nil {
		return err
	}
	result := coreTabResult(tab)
	result.Status = "closed"
	result.Meta = map[string]any{"closed": []string{tab.Handle}, "refused": []any{}}
	return emitCore(command, result)
}
func init() {
	rootCmd.AddCommand(newCorePageCommand(), newCoreDoCommand(), newCoreTabsCommand(), newCoreCloseCommand(), newCoreShotCommand(), newCoreReferenceCommand())
}
