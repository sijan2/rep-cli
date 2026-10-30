package agentstate

import (
	"testing"
	"time"
)

func TestHealthMarkersPersistWithoutRefreshingOnFailure(t *testing.T) {
	now := time.Now().UTC()
	s := &Store{Root: t.TempDir(), Now: func() time.Time { return now }}
	if s.RecentlyLive("fixture/a") {
		t.Fatal("invented liveness")
	}
	_ = s.Health("fixture/a", true)
	if !s.RecentlyLive("fixture/a") {
		t.Fatal("lost live marker")
	}
	now = now.Add(time.Minute)
	_ = s.Health("fixture/a", false)
	state, _ := s.Read()
	first := *state.Tasks["fixture/a"].BridgeDownSince
	now = now.Add(time.Minute)
	_ = s.Health("fixture/a", false)
	state, _ = s.Read()
	if !state.Tasks["fixture/a"].BridgeDownSince.Equal(first) {
		t.Fatal("reset down-since marker")
	}
	now = now.Add(10 * time.Minute)
	if s.RecentlyLive("fixture/a") {
		t.Fatal("stale bridge eligible for heal")
	}
	_ = s.Health("fixture/a", true)
	state, _ = s.Read()
	if state.Tasks["fixture/a"].BridgeDownSince != nil {
		t.Fatal("successful heal retained failure")
	}
}
func TestLifecycleReservationProtectsHoldersAndBlocksRacingClaims(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	browser := Browser{Name: "headless", Profile: "p", Epoch: "e", PrivateName: "fixture"}
	tab, _ := s.Register(browser, 1, "fixture/other", "other-session", true)
	if _, err := s.BeginLifecycle(browser, "fixture/a", "a", time.Minute); err == nil {
		t.Fatal("restart killed another holder")
	}
	_ = s.Change(tab.Handle, "", func(tab *Tab) error { tab.Closed = true; return nil })
	id, err := s.BeginLifecycle(browser, "fixture/a", "a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(browser, 2, "fixture/a", "a", true); err == nil {
		t.Fatal("registered during restart")
	}
	if _, _, err := s.Reserve(browser, "fixture/a", "a", "", true, true, time.Minute); err == nil {
		t.Fatal("navigated during restart")
	}
	if err := s.EndLifecycle(browser, id, true); err != nil {
		t.Fatal(err)
	}
	tab, err = s.Register(browser, 3, "fixture/a", "a", true)
	if err != nil {
		t.Fatal(err)
	}
	_, lease, err := s.Reserve(browser, "fixture/a", "a", tab.Handle, true, false, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginLifecycle(browser, "fixture/a", "a", time.Minute); err == nil {
		t.Fatal("restarted active same-task execute lease")
	}
	_ = s.Release(tab.Handle, lease)
	if _, err := s.BeginLifecycle(browser, "fixture/a", "another-session", time.Minute); err == nil {
		t.Fatal("stopped another session's task tab")
	}
}
func TestInventoryClaimsUserTabsAndRefusesActiveOwner(t *testing.T) {
	now := time.Now().UTC()
	s := &Store{Root: t.TempDir(), Now: func() time.Time { return now }}
	browser := Browser{Name: "dia", Profile: "p", Epoch: "e"}
	if err := s.Inventory(browser, []int{7}); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Read()
	tab := state.Tabs["t1"]
	if tab.Owner != "" || tab.CreatedByRep || !s.Adoptable(state, tab) {
		t.Fatalf("inventory adopted user tab: %+v", tab)
	}
	claimed, lease, err := s.Reserve(browser, "fixture/a", "a", tab.Handle, true, false, time.Minute)
	if err != nil || claimed.Owner != "fixture/a" || claimed.CreatedByRep {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	_ = s.Release(tab.Handle, lease)
	if _, _, err := s.Reserve(browser, "fixture/b", "b", tab.Handle, true, false, time.Minute); err == nil {
		t.Fatal("stole live owner's tab")
	}
	now = now.Add(25 * time.Hour)
	claimed, lease, err = s.Reserve(browser, "fixture/b", "b", tab.Handle, true, false, time.Minute)
	if err != nil || claimed.Owner != "fixture/b" {
		t.Fatalf("idle adoption: %+v %v", claimed, err)
	}
	_ = s.Release(tab.Handle, lease)
	_ = s.Inventory(browser, nil)
	if _, err = s.Tab(tab.Handle); err == nil {
		t.Fatal("inventory retained disappeared tab")
	}
}

func TestLifecycleCanStopEmptyFormerlySharedTask(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	browser := Browser{Name: "headless", Profile: "p", Epoch: "e", PrivateName: "fixture"}
	_ = s.Remember("fixture/a", "other-session", browser)
	tab, _ := s.Register(browser, 7, "fixture/a", "other-session", true)
	_ = s.Change(tab.Handle, "", func(tab *Tab) error { tab.Closed = true; return nil })
	if _, err := s.BeginLifecycle(browser, "fixture/a", "this-session", time.Minute); err != nil {
		t.Fatalf("empty shared task kept a phantom holder: %v", err)
	}
}

func TestTransportReservationHonorsLifecycleGuard(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	browser := Browser{Name: "headless", Profile: "p", Epoch: "e", PrivateName: "fixture"}
	id, err := s.BeginLifecycle(browser, "fixture/a", "a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReserveTransport(browser, "fixture/a", "a", "https://fixture.test", time.Minute); err == nil {
		t.Fatal("fetch/replay opened a tab during restart")
	}
	_ = s.EndLifecycle(browser, id, false)
	tab, lease, err := s.ReserveTransport(browser, "fixture/a", "a", "https://fixture.test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginLifecycle(browser, "fixture/a", "a", time.Minute); err == nil {
		t.Fatal("restart interrupted active transport")
	}
	_ = s.Release(tab.Handle, lease)
}
