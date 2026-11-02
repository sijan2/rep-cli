package agentstate

import (
	"sort"
	"strings"
	"time"
)

// Health records only liveness, never endpoint, credentials or page data.
func (s *Store) Health(owner string, live bool) error {
	return s.Update(func(state *State) error {
		value := task(state, owner)
		now := s.now()
		if live {
			value.BridgeLiveAt = &now
			value.BridgeDownSince = nil
		} else if value.BridgeDownSince == nil {
			value.BridgeDownSince = &now
		}
		return nil
	})
}
func (s *Store) RecentlyLive(owner string) bool {
	state, err := s.Read()
	if err != nil {
		return false
	}
	value := state.Tasks[owner]
	return value != nil && value.BridgeLiveAt != nil && s.now().Sub(*value.BridgeLiveAt) < 10*time.Minute
}

func lifecycleKey(browser Browser) string {
	if browser.PrivateName != "" {
		return "private:" + browser.PrivateName
	}
	return "profile:" + browser.Profile
}
func lifecycleBusy(state *State, browser Browser, now time.Time) bool {
	lease := state.Lifecycle[lifecycleKey(browser)]
	return lease != nil && lease.Until.After(now)
}

// BeginLifecycle excludes new claims/leases while a destructive lifecycle
// operation checks holders and stops/restarts the browser. RPCs run unlocked.
func (s *Store) BeginLifecycle(browser Browser, owner, session string, duration time.Duration, pinned bool) (string, error) {
	id := Token("l_")
	err := s.Update(func(state *State) error {
		now := s.now()
		if lifecycleBusy(state, browser, now) {
			return Failure("browser_in_use", "another browser lifecycle operation is active", "rep scope")
		}
		holders := []string{}
		ownTabs := false
		for _, tab := range state.Tabs {
			if tab.Closed || lifecycleKey(tab.Browser) != lifecycleKey(browser) {
				continue
			}
			if tab.Owner == owner {
				ownTabs = true
			}
			if (browser.PrivateName != "" && tab.Owner != "" && tab.Owner != owner) || tab.Busy(now) {
				holders = append(holders, tab.Handle)
			}
			if browser.PrivateName != "" && tab.Owner == owner && tab.Session != "" && tab.Session != session {
				holders = append(holders, tab.Handle)
			}
		}
		// Same-session siblings have indistinguishable derived identities. Require an
		// explicit task pin before a lifecycle operation can destroy their tabs. This
		// check shares the registry lock with claims, so a racing new tab is protected.
		if browser.PrivateName != "" && ownTabs && !pinned && strings.HasPrefix(session, "agent-") {
			return Failure("ambiguous_target", "pin the task before restarting or stopping a profile with owned tabs", "rep tabs")
		}
		if value := state.Tasks[owner]; browser.PrivateName != "" && ownTabs && value != nil {
			for key, at := range value.Sessions {
				if key != session && now.Sub(at) < 10*time.Minute {
					return Failure("task_shared", "another session shares this task; close addressed tabs before stopping the profile", "rep tabs")
				}
			}
		}
		if len(holders) > 0 {
			sort.Strings(holders)
			return &Error{Code: "browser_in_use", Message: "profile has tabs or leases held by another operation", Next: "rep tabs", Retry: "needs-agent", Candidates: holders}
		}
		if state.Lifecycle == nil {
			state.Lifecycle = map[string]*Lease{}
		}
		state.Lifecycle[lifecycleKey(browser)] = &Lease{ID: id, Until: now.Add(duration), Purpose: "lifecycle"}
		return nil
	})
	return id, err
}
func (s *Store) EndLifecycle(browser Browser, id string, closed bool) error {
	return s.Update(func(state *State) error {
		key := lifecycleKey(browser)
		if lease := state.Lifecycle[key]; lease == nil || lease.ID != id {
			return Failure("lease_lost", "browser lifecycle reservation expired", "rep scope")
		}
		if closed {
			for _, tab := range state.Tabs {
				if lifecycleKey(tab.Browser) == key {
					tab.Closed = true
					tab.Lease = nil
					tab.Refs = nil
				}
			}
		}
		delete(state.Lifecycle, key)
		return nil
	})
}

func adoptable(state *State, tab *Tab, now time.Time) bool {
	if tab.Busy(now) || tab.HasInput || tab.Dialog {
		return false
	}
	if tab.Owner == "" {
		return true
	}
	if now.Sub(tab.LastUsed) < 24*time.Hour {
		return false
	}
	if owner := state.Tasks[tab.Owner]; owner != nil {
		for _, at := range owner.Sessions {
			if now.Sub(at) < 24*time.Hour {
				return false
			}
		}
	}
	return true
}
func (s *Store) Adoptable(state *State, tab *Tab) bool { return adoptable(state, tab, s.now()) }

// Inventory mints handles for unclaimed user tabs without adopting or touching
// their document. Only a live, complete inventory may mark missing tabs closed.
func (s *Store) Inventory(browser Browser, ids []int) error {
	return s.Update(func(state *State) error {
		if lifecycleBusy(state, browser, s.now()) {
			return Failure("browser_in_use", "browser lifecycle operation is active", "rep scope")
		}
		present := map[int]bool{}
		for _, id := range ids {
			present[id] = true
		}
		for _, tab := range state.Tabs {
			if sameBrowser(tab.Browser, browser) && !tab.Closed && tab.ChromeID >= 0 {
				if !present[tab.ChromeID] && !tab.Busy(s.now()) {
					tab.Closed = true
					tab.Refs = nil
				}
				delete(present, tab.ChromeID)
			}
		}
		sorted := []int{}
		for id := range present {
			sorted = append(sorted, id)
		}
		sort.Ints(sorted)
		for _, id := range sorted {
			state.NextTab++
			handle := tabHandle(state.NextTab)
			state.Tabs[handle] = &Tab{Handle: handle, Browser: browser, ChromeID: id, Created: s.now(), Refs: map[string]Ref{}}
		}
		return nil
	})
}
