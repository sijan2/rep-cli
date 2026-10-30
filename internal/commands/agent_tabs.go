package commands

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
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
func newCoreShotCommand() *cobra.Command {
	command := &cobra.Command{Use: "shot [TAB] [PATH]", Short: "Write a screenshot artifact", Args: cobra.MaximumNArgs(2)}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := coreShot(cmd, args); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}
func coreShot(command *cobra.Command, args []string) error {
	store, err := coreStore()
	if err != nil {
		return err
	}
	target, path := "", ""
	var addressed *agentstate.Tab
	if len(args) > 0 {
		target = args[0]
		if !agentstate.TabPattern.MatchString(target) {
			return coreArgument("shot requires a tab handle")
		}
		addressed, err = store.Tab(target)
		if err != nil {
			return err
		}
	}
	if len(args) > 1 {
		path = args[1]
	}
	ctx, cancel := coreTimeout(command, 10*time.Second)
	defer cancel()
	client, browser, err := resolveCoreBrowser(ctx, store, addressed)
	if err != nil {
		return err
	}
	selected, _ := scope.Current()
	tab, lease, err := store.Reserve(browser, selected.Identity(), selected.SessionKey, target, selected.TaskSource == "flag", false, 11*time.Second)
	if err != nil {
		return err
	}
	defer store.Release(tab.Handle, lease)
	session := &browserfast.Session{Browser: client, Tab: tab.ChromeID, Owner: tab.Owner}
	defer session.Close()
	if err := session.Acquire(ctx); err != nil {
		return err
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err := session.Inspect(ctx, "", "Page.captureScreenshot", map[string]any{"format": "png", "captureBeyondViewport": false}, &shot); err != nil {
		return err
	}
	data, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil {
		return err
	}
	if path == "" {
		path = filepath.Join(selected.DataDir, "shots", fmt.Sprintf("%s-%d.png", tab.Handle, time.Now().UnixNano()))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	result := coreTabResult(tab)
	result.Status = "saved"
	result.Meta = map[string]any{"path": path, "bytes": len(data)}
	return emitCore(command, result)
}
func init() {
	rootCmd.AddCommand(newCorePageCommand(), newCoreDoCommand(), newCoreTabsCommand(), newCoreCloseCommand(), newCoreShotCommand(), newCoreReferenceCommand())
}
