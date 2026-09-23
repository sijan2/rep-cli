package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/repplus/rep-cli/internal/scope"
)

func setupScopeStore(t *testing.T) string {
	t.Helper()
	scope.Reset()
	t.Cleanup(scope.Reset)
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("REP_WORKSPACE", "")
	t.Setenv("REP_TASK", "")
	t.Setenv("REPLIVE_PATH", "")
	t.Setenv("REPANDROID_PATH", "")
	t.Chdir(dir)
	return dir
}

func TestWorkspaceTaskStoresDoNotInheritGlobalOrOtherTask(t *testing.T) {
	setupScopeStore(t)
	global := NewStore()
	global.SetPrimary("legacy.example")
	if err := global.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := global.AddSession("global", "global capture", []Request{{ID: "global-request", URL: "https://legacy.example/"}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REP_WORKSPACE", "project")
	t.Setenv("REP_TASK", "agent-a")
	first, err := Load()
	if err != nil || len(first.Sessions) != 0 || len(first.PrimaryDomains) != 0 {
		t.Fatalf("inherited legacy data: %+v %v", first, err)
	}
	first.SetPrimary("task-a.example")
	if err := first.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := first.AddSession("task-a", "task a", []Request{{ID: "task-a-request", URL: "https://task-a.example/"}}); err != nil {
		t.Fatal(err)
	}
	notes, _ := LoadNotes()
	note := notes.AddNote("agent a private note", nil, nil)
	if err := AppendNoteLog(&note); err != nil {
		t.Fatal(err)
	}
	firstPath, _ := GetLiveFilePath()
	t.Setenv("REP_TASK", "agent-b")
	second, err := Load()
	if err != nil || len(second.Sessions) != 0 || len(second.PrimaryDomains) != 0 {
		t.Fatalf("inherited another task: %+v %v", second, err)
	}
	notes, err = LoadNotes()
	if err != nil || len(notes.Notes) != 0 {
		t.Fatalf("inherited another task's notes: %+v %v", notes, err)
	}
	secondPath, _ := GetLiveFilePath()
	if firstPath == secondPath {
		t.Fatal("tasks share live capture path")
	}
	t.Setenv("REP_TASK", "agent-a")
	first, err = Load()
	if err != nil || !first.IsPrimary("task-a.example") || len(first.Sessions) != 1 || first.Sessions[0].ID != "task-a" {
		t.Fatalf("lost task a data: %+v %v", first, err)
	}
}

func TestScopedPathsRejectInheritedOverrides(t *testing.T) {
	dir := setupScopeStore(t)
	t.Setenv("REP_WORKSPACE", "project")
	t.Setenv("REPLIVE_PATH", filepath.Join(dir, "shared-live.json"))
	t.Setenv("REPANDROID_PATH", filepath.Join(dir, "shared-android.json"))
	if _, err := GetLiveFilePath(); err == nil {
		t.Fatal("live override bypassed workspace isolation")
	}
	if _, err := GetAndroidFilePath(); err == nil {
		t.Fatal("Android override bypassed workspace isolation")
	}
	if _, err := scope.Configure(scope.Options{Global: true}); err != nil {
		t.Fatal(err)
	}
	if got, err := GetLiveFilePath(); err != nil || got != filepath.Join(dir, "shared-live.json") {
		t.Fatalf("explicit global override: %s %v", got, err)
	}
}

func TestStoreGetDoesNotReuseAnotherNamespace(t *testing.T) {
	setupScopeStore(t)
	if _, err := scope.Configure(scope.Options{Workspace: "project", Task: "agent-a"}); err != nil {
		t.Fatal(err)
	}
	first, err := Get()
	if err != nil {
		t.Fatal(err)
	}
	first.SetPrimary("agent-a.example")
	if err := first.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := scope.Configure(scope.Options{Workspace: "project", Task: "agent-b"}); err != nil {
		t.Fatal(err)
	}
	second, err := Get()
	if err != nil || len(second.PrimaryDomains) != 0 || second == first {
		t.Fatalf("Get reused the previous task: %+v %v", second, err)
	}
	if _, err := scope.Configure(scope.Options{Workspace: "project", Task: "agent-a"}); err != nil {
		t.Fatal(err)
	}
	firstAgain, err := Get()
	if err != nil || !firstAgain.IsPrimary("agent-a.example") {
		t.Fatalf("Get failed to return task a: %+v %v", firstAgain, err)
	}
}

func TestSessionLogLoadsWithoutMetadataAndPreservesCaptureProvenance(t *testing.T) {
	setupScopeStore(t)
	t.Setenv("REP_WORKSPACE", "project")
	original := NewStore()
	export := &Export{SessionID: "capture-one", CaptureDigest: "digest", BrowserSession: &BrowserSession{TabID: 41, URL: "https://example.test/"}}
	if _, err := original.AddSession("saved", "note", []Request{{ID: "request", URL: "https://example.test/"}}, export); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil || len(loaded.Sessions) != 1 {
		t.Fatalf("failed loading standalone log: %+v %v", loaded, err)
	}
	session := loaded.Sessions[0]
	if session.CaptureSessionID != export.SessionID || session.CaptureDigest != export.CaptureDigest || session.BrowserSession.TabID != 41 {
		t.Fatalf("lost provenance: %+v", session)
	}
	logPath, _ := GetSessionsFilePath()
	info, err := os.Stat(logPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("archive permissions: %v %v", info, err)
	}
}

func TestStoreReplacementIsPrivateAndNeverPartial(t *testing.T) {
	setupScopeStore(t)
	t.Setenv("REP_WORKSPACE", "project")
	store := NewStore()
	store.SetPrimary("one.example")
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	path, _ := GetStoreFilePath()
	var wait sync.WaitGroup
	errors := make(chan error, 1)
	done := make(chan struct{})
	wait.Add(1)
	go func() {
		defer wait.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			data, err := os.ReadFile(path)
			if err == nil {
				var value map[string]interface{}
				err = json.Unmarshal(data, &value)
			}
			if err != nil {
				errors <- err
				return
			}
		}
	}()
	for index := 0; index < 30; index++ {
		if err := store.Save(); err != nil {
			close(done)
			wait.Wait()
			t.Fatal(err)
		}
	}
	close(done)
	wait.Wait()
	select {
	case err := <-errors:
		t.Fatalf("reader saw partial metadata: %v", err)
	default:
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("metadata permissions: %v %v", info, err)
	}
}
