package commands

import (
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/headless"
	"github.com/repplus/rep-cli/internal/scope"
)

type coreScopeInventory struct {
	Browser         string           `json:"browser"`
	Profile         string           `json:"profile_id"`
	Tabs            []string         `json:"tabs"`
	Captures        []map[string]any `json:"captures"`
	Storage         map[string]int64 `json:"storage"`
	StorageComplete bool             `json:"storage_complete"`
	Journal         []map[string]any `json:"journal"`
	JournalComplete bool             `json:"journal_complete"`
	BridgeDownSince *time.Time       `json:"bridge_down_since,omitempty"`
}

func readCoreScope(selected scope.Scope) (coreScopeInventory, error) {
	result := coreScopeInventory{Tabs: []string{}, Captures: []map[string]any{}, Storage: map[string]int64{}, Journal: []map[string]any{}, StorageComplete: true, JournalComplete: true}
	registry, err := coreStore()
	if err != nil {
		return result, err
	}
	state, err := registry.Read()
	if err != nil {
		return result, err
	}
	result.Browser = strings.TrimSpace(os.Getenv("REP_BROWSER"))
	if result.Browser == "" {
		result.Browser = "dia"
	}
	privateName := ""
	if task := state.Tasks[selected.Identity()]; task != nil {
		result.BridgeDownSince = task.BridgeDownSince
		if task.Browser != nil {
			result.Browser, result.Profile, privateName = task.Browser.Name, task.Browser.Profile, task.Browser.PrivateName
		}
	}
	for _, tab := range state.Tabs {
		if tab.Owner == selected.Identity() && !tab.Closed {
			result.Tabs = append(result.Tabs, tab.Handle)
		}
	}
	sort.Strings(result.Tabs)
	for _, capture := range state.Captures {
		if capture.Owner == selected.Identity() && capture.Request == "" {
			result.Captures = append(result.Captures, map[string]any{"id": capture.ID, "archive": capture.Archive, "tab": capture.Tab, "created": capture.Created})
		}
	}
	sort.Slice(result.Captures, func(i, j int) bool {
		return result.Captures[i]["created"].(time.Time).After(result.Captures[j]["created"].(time.Time))
	})
	if len(result.Captures) > 20 {
		result.Captures = result.Captures[:20]
	}
	result.Storage, result.StorageComplete = coreStorage(selected.DataDir)
	if result.Browser == "headless" {
		if dir, e := headless.NamedProfileDir(privateName); e == nil {
			usage, complete := coreStorage(dir)
			for _, bytes := range usage {
				result.Storage["headless"] += bytes
			}
			result.StorageComplete = result.StorageComplete && complete
		}
	}
	result.Journal, result.JournalComplete, err = coreRecentJournal(filepath.Join(registry.Root, "journal.jsonl"), selected.Identity())
	return result, err
}

// Inventory file sizes without opening capture/body contents or following links.
func coreStorage(root string) (map[string]int64, bool) {
	sizes := map[string]int64{"captures": 0, "body-exports": 0, "artifacts": 0, "headless": 0, "unmanaged": 0}
	complete := true
	count := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			complete = false
			return nil
		}
		count++
		if count > 100000 {
			complete = false
			return fs.SkipAll
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			complete = false
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		part := strings.Split(rel, string(filepath.Separator))[0]
		class := "unmanaged"
		switch part {
		case "sessions", "captures", "live.json", "store.json":
			class = "captures"
		case "body-exports":
			class = part
		case "artifacts", "shots", "reference":
			class = "artifacts"
		case "headless":
			class = part
		}
		sizes[class] += info.Size()
		return nil
	})
	if err != nil {
		complete = false
	}
	return sizes, complete
}
func coreRecentJournal(path, owner string) ([]map[string]any, bool, error) {
	entries := []map[string]any{}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return entries, true, nil
	}
	if err != nil {
		return entries, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return entries, false, err
	}
	offset := max(int64(0), info.Size()-(64<<10))
	_, err = f.Seek(offset, io.SeekStart)
	if err != nil {
		return entries, false, err
	}
	data, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return entries, false, err
	}
	lines := strings.Split(string(data), "\n")
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	for _, line := range lines {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) == nil && entry["task"] == owner {
			entries = append(entries, entry)
		}
	}
	complete := offset == 0
	if len(entries) > 20 {
		entries = entries[len(entries)-20:]
		complete = false
	}
	return entries, complete, nil
}
