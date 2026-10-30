package agentstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func fixtureStore(t *testing.T) *Store { t.Helper(); return &Store{Root: t.TempDir()} }
func fixtureBrowser() Browser {
	return Browser{Name: "headless", Profile: "profile-fixture", Epoch: "epoch-one", PrivateName: "default"}
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var stateError *Error
	if !errors.As(err, &stateError) || stateError.Code != code {
		t.Fatalf("want %s got %v", code, err)
	}
}
func TestConcurrentGlobalHandlesAndExclusiveLeases(t *testing.T) {
	store := fixtureStore(t)
	var wg sync.WaitGroup
	results := make(chan *Tab, 32)
	failures := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tab, err := store.Register(fixtureBrowser(), i+1, fmt.Sprintf("default/task-%d", i), fmt.Sprintf("agent-%d", i), true)
			if err != nil {
				failures <- err
			} else {
				results <- tab
			}
		}(i)
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	seen := map[string]bool{}
	for tab := range results {
		if seen[tab.Handle] {
			t.Fatal("reused global handle")
		}
		seen[tab.Handle] = true
	}
	if len(seen) != 32 {
		t.Fatal(len(seen))
	}
	state, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	tab := state.Tabs["t1"]
	_, lease, err := store.Reserve(tab.Browser, tab.Owner, tab.Session, tab.Handle, true, true, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Reserve(tab.Browser, tab.Owner, tab.Session, tab.Handle, true, true, time.Minute)
	requireCode(t, err, "tab_busy")
	if err := store.Release(tab.Handle, lease); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"handles.json", "handles.lock"} {
		info, err := os.Stat(filepath.Join(store.Root, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private registry permissions: %v %v", info, err)
		}
	}
}
func TestRefsNeverRecycleAcrossReloadsAndCursorsOutliveRenderer(t *testing.T) {
	now := time.Now()
	store := fixtureStore(t)
	store.Now = func() time.Time { return now }
	tab, err := store.Register(fixtureBrowser(), 1, "default/a", "agent-a", true)
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.MintRefs(tab.Handle, "", "document-one", []string{"e_old"})
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := store.SaveCursor(Cursor{Tab: tab.Handle, Owner: tab.Owner, Kind: "page", Offset: 10000, Budget: 10000})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := store.MintRefs(tab.Handle, "", "document-two", []string{"e_new"})
	if err != nil || old[0] == fresh[0] {
		t.Fatalf("ref reused: %v %v %v", old, fresh, err)
	}
	_, err = store.ResolveRef(old[0])
	requireCode(t, err, "reference_stale")
	now = now.Add(3 * time.Minute)
	_, err = store.ResolveRef(fresh[0])
	requireCode(t, err, "reference_stale")
	persisted, err := store.Cursor(cursor)
	if err != nil || persisted.Offset != 10000 {
		t.Fatalf("cursor expired with renderer: %+v %v", persisted, err)
	}
}
func TestDerivedSiblingBareWriteAndExplicitOwnerMismatchFailClosed(t *testing.T) {
	store := fixtureStore(t)
	tab, err := store.Register(fixtureBrowser(), 1, "default/shared", "agent-same", true)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Reserve(tab.Browser, tab.Owner, "agent-same", "", false, true, time.Minute)
	requireCode(t, err, "ambiguous_target")
	owner, err := store.Owner(tab.Handle)
	if err != nil || owner != tab.Owner {
		t.Fatalf("child handle identity: %s %v", owner, err)
	}
	_, _, err = store.Reserve(tab.Browser, "default/other", "agent-other", tab.Handle, true, true, time.Minute)
	requireCode(t, err, "owned_by_other_task")
	_, lease, err := store.Reserve(tab.Browser, owner, "agent-child", tab.Handle, false, true, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Release(tab.Handle, lease)
	other := tab.Browser
	other.Epoch = "epoch-two"
	_, _, err = store.Reserve(other, owner, "agent-child", tab.Handle, true, true, time.Minute)
	requireCode(t, err, "tab_gone")
}
func TestTabCapNeverEvictsAndCorruptionNeverResetsCounter(t *testing.T) {
	store := fixtureStore(t)
	for i := 0; i < TabCap; i++ {
		_, err := store.Register(fixtureBrowser(), i, "default/a", "", true)
		if err != nil {
			t.Fatal(err)
		}
	}
	state, _ := store.Read()
	for _, tab := range state.Tabs {
		_, _, err := store.Reserve(tab.Browser, tab.Owner, "", tab.Handle, true, true, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := store.Reserve(fixtureBrowser(), "default/a", "", "", true, true, time.Minute)
	requireCode(t, err, "ambiguous_target")
	if err := os.WriteFile(filepath.Join(store.Root, "handles.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Register(fixtureBrowser(), 99, "default/a", "", true); err == nil {
		t.Fatal("corrupt counter reset")
	}
	data, _ := os.ReadFile(filepath.Join(store.Root, "handles.json"))
	if string(data) != "broken" {
		t.Fatal("registry clobbered")
	}
}
