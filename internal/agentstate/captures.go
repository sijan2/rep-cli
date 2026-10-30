package agentstate

import (
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
