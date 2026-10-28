package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/browserrpc"
	"github.com/repplus/rep-cli/internal/scope"
)

// Ownership survives a pending read, but never adopts an arbitrary --tab.
type referenceOwnedTab struct {
	Tab     int       `json:"tab_id"`
	Browser string    `json:"browser"`
	URL     string    `json:"url"`
	Created time.Time `json:"created_at"`
	Keep    bool      `json:"keep"`
}

func referenceTabPath(tab int) string {
	selected, _ := scope.Current()
	return filepath.Join(selected.DataDir, "reference-tabs", fmt.Sprintf("%d.json", tab))
}

func referenceLoadTab(tab int, browser string) *referenceOwnedTab {
	return referenceLoadTabFile(referenceTabPath(tab), tab, browser)
}

func referenceLoadClosedTab(tab int, browser string) *referenceOwnedTab {
	return referenceLoadTabFile(referenceTabPath(tab)+".closed", tab, browser)
}

func referenceLoadTabFile(path string, tab int, browser string) *referenceOwnedTab {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 8192 {
		return nil
	}
	var owned referenceOwnedTab
	if json.Unmarshal(data, &owned) != nil || owned.Tab != tab || owned.Browser != browser || owned.Created.After(time.Now()) || time.Since(owned.Created) > 2*time.Minute {
		return nil
	}
	return &owned
}

func referenceSaveTab(owned *referenceOwnedTab) error {
	data, err := json.Marshal(owned)
	if err != nil {
		return err
	}
	return writePrivateFile(referenceTabPath(owned.Tab), data)
}

func referenceAlreadyClosed(ctx context.Context, browser browserrpc.Caller, tab int, profile string) (bool, error) {
	if referenceLoadClosedTab(tab, profile) == nil {
		return false, nil
	}
	var current struct {
		Tabs []struct {
			ID int `json:"id"`
		} `json:"tabs"`
	}
	if err := browser.Call(ctx, "browser.tabs", nil, &current); err != nil {
		return false, err
	}
	for _, item := range current.Tabs {
		if item.ID == tab {
			return false, errors.New("this task previously closed the tab ID but it is present again; obtain a fresh page reference before closing")
		}
	}
	return true, nil
}

func referenceFinishTab(session *browserfast.Session, owned *referenceOwnedTab, result *fastOutput, nextPage string) {
	result.CleanupNext = ""
	result.CleanupError = ""
	if owned == nil {
		result.TabState = "retained_external"
		return
	}
	if owned.Keep {
		result.TabState = "kept"
		return
	}
	if result.Status != "ready" {
		result.TabState = "kept_pending"
		return
	}
	if owned.URL == "" || owned.URL != result.URL {
		result.TabState, result.CleanupNext = "kept_changed_document", nextPage
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	closeErr := session.CloseTab(ctx, owned.URL)
	for {
		var current struct {
			Tabs []struct {
				ID int `json:"id"`
			} `json:"tabs"`
		}
		if err := session.Call(ctx, "browser.tabs", nil, &current); err != nil {
			result.TabState, result.CleanupNext = "close_unconfirmed", nextPage
			result.CleanupError = referenceClip(err.Error(), 240)
			return
		}
		present := false
		for _, tab := range current.Tabs {
			present = present || tab.ID == session.Tab
		}
		if !present {
			// Page.close can acknowledge before Chrome removes the tab. Only
			// inventory reads are repeated; the close mutation is never retried.
			result.TabState = "closed"
			// Retain a short task-scoped receipt so a redundant close can be
			// verified as already complete without producing a discovery loop.
			data, _ := json.Marshal(owned)
			_ = writePrivateFile(referenceTabPath(session.Tab)+".closed", data)
			_ = os.Remove(referenceTabPath(session.Tab))
			return
		}
		timer := time.NewTimer(30 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			result.TabState, result.CleanupNext = "kept_beforeunload_or_close_failed", nextPage
			if closeErr != nil {
				result.CleanupError = referenceClip(closeErr.Error(), 240)
			}
			return
		case <-timer.C:
		}
	}
}
