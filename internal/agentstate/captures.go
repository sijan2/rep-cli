package agentstate

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

func (s *Store) RegisterCapture(id, owner, tab, path string, requests []string) error {
	return s.Update(func(state *State) error {
		if _, exists := state.Captures[id]; exists {
			return Failure("capture_collision", "capture handle already exists", "rep net")
		}
		for _, request := range requests {
			if _, exists := state.Captures[request]; exists {
				return Failure("request_collision", "request handle already belongs to an archive", "rep net")
			}
		}
		capture := Capture{ID: id, Owner: owner, Tab: tab, Archive: path, Created: s.now()}
		if target := state.Tabs[tab]; target != nil {
			capture.Browser = target.Browser
		}
		state.Captures[id] = capture
		for _, request := range requests {
			entry := capture
			entry.Request = request
			state.Captures[request] = entry
		}
		return nil
	})
}
func (s *Store) Capture(handle, owner string) (Capture, error) {
	state, err := s.Read()
	if err != nil {
		return Capture{}, err
	}
	if handle != "" {
		capture, ok := state.Captures[handle]
		if !ok {
			return Capture{}, Failure("capture_not_found", "the exact capture or request handle is unavailable", "rep net")
		}
		return capture, nil
	}
	candidates := []Capture{}
	for key, capture := range state.Captures {
		if strings.HasPrefix(key, "s_") && capture.Owner == owner && s.now().Sub(capture.Created) <= 10*time.Minute {
			candidates = append(candidates, capture)
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if len(candidates) == 0 {
		return Capture{}, Failure("capture_required", "record a page or action before querying traffic", "rep page --capture --reload")
	}
	problem := Failure("ambiguous_target", "multiple recent captures require an s_ handle", "rep scope")
	for _, capture := range candidates {
		problem.Candidates = append(problem.Candidates, capture.ID)
	}
	return Capture{}, problem
}

// ReserveTransport allocates a background tab atomically, independently of the
// task's foreground target. Its lease covers navigation, capture and transport.
func (s *Store) ReserveTransport(browser Browser, owner, session, origin string, duration time.Duration) (*Tab, string, error) {
	var result *Tab
	lease := Token("l_")
	err := s.Update(func(state *State) error {
		if lifecycleBusy(state, browser, s.now()) {
			return Failure("browser_in_use", "browser lifecycle operation is active", "rep scope")
		}
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || origin != parsed.Scheme+"://"+parsed.Host {
			return Failure("invalid_argument", "transport requires an HTTP(S) origin", "rep fetch --help")
		}
		if browser.Profile == "" || browser.Epoch == "" {
			return Failure("bridge_outdated", "bridge has no profile identity", "rep doctor")
		}
		count := 0
		for _, tab := range state.Tabs {
			if tab.Owner != owner || tab.Closed || !sameBrowser(tab.Browser, browser) {
				continue
			}
			count++
			if tab.TransportOrigin == origin {
				result = tab
			}
		}
		if result != nil && result.Busy(s.now()) {
			return &Error{Code: "tab_busy", Message: "background transport has an active execute lease", Next: "rep tabs", Retry: "retryable"}
		}
		if result == nil {
			if count >= TabCap {
				return tabCapFailure(state, browser, owner, s.now())
			}
			state.NextTab++
			result = &Tab{Handle: fmt.Sprintf("t%d", state.NextTab), Browser: browser, ChromeID: -1, Owner: owner, Session: session, Created: s.now(), CreatedByRep: true, TransportOrigin: origin, Refs: map[string]Ref{}}
			state.Tabs[result.Handle] = result
		}
		result.LastUsed = s.now()
		result.Lease = &Lease{ID: lease, Until: s.now().Add(duration), Purpose: "execute"}
		return nil
	})
	return result, lease, err
}
