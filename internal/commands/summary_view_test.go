package commands

import (
	"encoding/json"
	"os"
	"path/filepath"

	"testing"

	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
)

func summaryTestScope(t *testing.T, workspace, task string) scope.Scope {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("REPLIVE_PATH", "")
	t.Setenv("REP_WORKSPACE", "")
	t.Setenv("REP_TASK", "")
	selected, err := scope.Configure(scope.Options{Workspace: workspace, Task: task})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scope.Reset)
	return selected
}

func writeSummaryCapture(t *testing.T, path string, capture store.Export) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func summaryFixture(id, host string) store.Request {
	return store.Request{ID: id, Method: "GET", URL: "https://" + host + "/form/2027?token=secret-query", Response: &store.Response{Status: 200, Body: "private form body"}}
}

func TestSummaryArchiveSelectionPrefersExactAndRejectsDuplicateIDs(t *testing.T) {
	sessions := []store.Session{
		{ID: "flow-one", HashID: "s_1111"}, {ID: "flow-two", HashID: "s_2222"}, {ID: "flow", HashID: "s_3333"},
	}
	selected, err := selectSummaryArchive(sessions, "flow")
	if err != nil || selected.HashID != "s_3333" {
		t.Fatal("prefixes obscured a full ID match")
	}
	sessions = append(sessions, store.Session{ID: "flow", HashID: "s_4444"})
	if _, err := selectSummaryArchive(sessions, "flow"); err == nil {
		t.Fatal("duplicate timestamp IDs selected an arbitrary archive")
	}
	selected, err = selectSummaryArchive(sessions, "s_4444")
	if err != nil || selected.HashID != "s_4444" {
		t.Fatal("full hash could not disambiguate archive")
	}
}
