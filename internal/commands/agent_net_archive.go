package commands

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/store"
)

var coreRequestHandle = regexp.MustCompile(`^h_[0-9a-f]{32}$`)
var coreCaptureHandle = regexp.MustCompile(`^s_[0-9a-f]{32}$`)

// The registry is the only archive selector. Never consult live.json, latest,
// display-ID prefixes, or another archive after an addressed handle fails.
func loadCoreDataCapture(handle string) (agentstate.Capture, store.Export, error) {
	capture, snapshot, err := loadCoreCapture(handle)
	if err != nil {
		return capture, snapshot, err
	}
	index, err := coreStore()
	if err != nil {
		return capture, snapshot, err
	}
	state, err := index.Read()
	if err != nil {
		return capture, snapshot, err
	}
	seen := map[string]bool{}
	for _, r := range snapshot.Requests {
		registered, ok := state.Captures[r.ID]
		if !coreRequestHandle.MatchString(r.ID) || seen[r.ID] || !ok || registered.ID != capture.ID || registered.Archive != capture.Archive || registered.Owner != capture.Owner || registered.Request != r.ID {
			return capture, snapshot, agentstate.Failure("archive_invalid", "request IDs do not match their retained capture index", coreNext("scope"))
		}
		seen[r.ID] = true
	}
	return capture, snapshot, nil
}
func coreDataRequest(handle string) (agentstate.Capture, *store.Request, error) {
	if !coreRequestHandle.MatchString(handle) {
		return agentstate.Capture{}, nil, coreArgument("body requires an exact captured h_ handle")
	}
	capture, snapshot, err := loadCoreDataCapture(handle)
	if err != nil {
		return capture, nil, err
	}
	for i := range snapshot.Requests {
		if snapshot.Requests[i].ID == handle {
			return capture, &snapshot.Requests[i], nil
		}
	}
	return capture, nil, agentstate.Failure("request_not_found", "request handle is absent from its exact archive", coreNext("net", capture.ID))
}
func coreDataDigest(snapshot store.Export) string {
	data, _ := json.Marshal(snapshot)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
