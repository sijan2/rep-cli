package jevdom

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/repplus/rep-cli/internal/bridge"
)

type Binding struct {
	Generation            string `json:"generation"`
	FrameID               string `json:"frame_id"`
	SessionID             string `json:"session_id,omitempty"`
	DocumentGeneration    string `json:"document_generation"`
	FrameURL              string `json:"frame_url,omitempty"`
	BackendDOMNodeID      int64  `json:"backend_dom_node_id"`
	SnapshotFingerprint   string `json:"snapshot_fingerprint"`
	ObservationMode       string `json:"observation_mode"`
	Kind                  string `json:"kind,omitempty"`
	Origin                string `json:"origin,omitempty"`
	ScopeFrameID          string `json:"scope_frame_id,omitempty"`
	ScopeFrameURL         string `json:"scope_frame_url,omitempty"`
	ScopeBackendDOMNodeID int64  `json:"root_backend_dom_node_id,omitempty"`
}

func observationParams(options Options) map[string]any {
	return map[string]any{"tab_id": options.TabID, "owner": options.Owner, "lease_id": options.LeaseID, "kind": options.Kind, "origin": options.Origin, "frame_id": options.FrameID, "frame_url": options.FrameURL, "root_backend_dom_node_id": options.ScopeBackendDOMNodeID}
}

func unsupportedObservation(err error) bool {
	var rpc *bridge.RPCError
	if !errors.As(err, &rpc) {
		return false
	}
	switch rpc.Code {
	case "unknown_method", "method_not_found", "unsupported_method", "not_implemented":
		return true
	}
	return false
}

func (selector Selector) observe(ctx context.Context, options Options) (Snapshot, bool, error) {
	if selector.Browser == nil {
		return Snapshot{}, false, errors.New("a browser is required")
	}
	if options.ObservationMode != "legacy" && options.Owner != "" {
		var snapshot Snapshot
		err := selector.Browser.Call(ctx, "browser.observe", observationParams(options), &snapshot)
		if err == nil {
			if err = validateSnapshot(&snapshot); err != nil {
				return Snapshot{}, true, err
			}
			return filterFrame(snapshot, options.FrameID, options.FrameURL), true, nil
		}
		if !unsupportedObservation(err) {
			return Snapshot{}, true, err
		}
	}
	snapshot, err := captureOptions(ctx, selector.Browser, options)
	return filterFrame(snapshot, options.FrameID, options.FrameURL), false, err
}

func filterFrame(snapshot Snapshot, frameID, frameURL string) Snapshot {
	if frameID == "" && frameURL == "" {
		return snapshot
	}
	filtered := make([]Candidate, 0, len(snapshot.Candidates))
	for _, candidate := range snapshot.Candidates {
		if (frameID == "" || candidate.FrameID == frameID) && (frameURL == "" || candidate.FrameURL == frameURL) {
			filtered = append(filtered, candidate)
		}
	}
	snapshot.Candidates = filtered
	snapshot.Coverage.TotalCandidates = len(filtered)
	return snapshot
}

var fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var candidatePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// The projection is an explicitly versioned browser contract. Unknown schemas,
// malformed evidence, and unsupported identity never become cached handles.
func validateSnapshot(snapshot *Snapshot) error {
	invalid := errors.New("browser returned an invalid semantic observation")
	if snapshot.Schema != 1 || snapshot.Generation == "" || len(snapshot.Generation) > 512 || !fingerprintPattern.MatchString(snapshot.Fingerprint) || len(snapshot.Candidates) > 100000 {
		return invalid
	}
	c := snapshot.Coverage
	if c.TotalCandidates != len(snapshot.Candidates) || c.FramesRead < 0 || c.UnavailableFrames < 0 || c.OmittedNodes < 0 || c.TextTruncated < 0 || c.Considered < 0 {
		return invalid
	}
	seen := map[string]bool{}
	for i := range snapshot.Candidates {
		candidate := &snapshot.Candidates[i]
		if !candidatePattern.MatchString(candidate.ID) || candidate.ID == "none" || seen[candidate.ID] || candidate.FrameID == "" || len(candidate.FrameID) > 256 || candidate.BackendDOMNodeID <= 0 || candidate.DocumentGeneration == "" || len(candidate.DocumentGeneration) > 512 || len(candidate.SessionID) > 256 || len(candidate.FrameURL) > 65536 || len(candidate.Role) > 48 || len(candidate.Name) > 180 || len(candidate.Context) > 120 || len(candidate.ContextRelations) > 8 {
			return invalid
		}
		seen[candidate.ID] = true
		candidate.Name = scrub(candidate.Name, 180)
		candidate.Context = scrub(candidate.Context, 120)
		if strings.TrimSpace(candidate.Name) == "" {
			return invalid
		}
		for _, relation := range candidate.ContextRelations {
			if len(relation.Role) > 48 || len(relation.Name) > 240 {
				return invalid
			}
		}
		for j := range candidate.ContextRelations {
			candidate.ContextRelations[j].Name = scrub(candidate.ContextRelations[j].Name, 240)
		}
		for key, value := range candidate.States {
			if !safeStates[key] {
				return invalid
			}
			switch value := value.(type) {
			case bool:
			case string:
				if !safeEnums[value] {
					return invalid
				}
			default:
				return invalid
			}
		}
	}
	return nil
}

// Validate synchronously refreshes the observation that justified a binding.
// A surviving node alone does not establish that its competing entities or
// relational labels are unchanged.
func (selector Selector) Validate(ctx context.Context, options Options, binding Binding) (bool, error) {
	if selector.Browser == nil {
		return false, errors.New("a browser is required")
	}
	if binding.Kind != "" {
		options.Kind, options.Origin = binding.Kind, binding.Origin
		options.FrameID, options.FrameURL, options.ScopeBackendDOMNodeID = binding.ScopeFrameID, binding.ScopeFrameURL, binding.ScopeBackendDOMNodeID
	}
	if binding.ObservationMode == "compact" {
		params := observationParams(options)
		params["fingerprint"], params["generation"] = binding.SnapshotFingerprint, binding.Generation
		var response struct {
			Fresh    bool     `json:"fresh"`
			Snapshot Snapshot `json:"snapshot"`
		}
		if err := selector.Browser.Call(ctx, "browser.validate", params, &response); err != nil {
			return false, err
		}
		if err := validateSnapshot(&response.Snapshot); err != nil {
			return false, err
		}
		return response.Fresh && response.Snapshot.Fingerprint == binding.SnapshotFingerprint && response.Snapshot.Generation == binding.Generation, nil
	}
	current, err := captureOptions(ctx, selector.Browser, options)
	return err == nil && current.Fingerprint == binding.SnapshotFingerprint && current.Generation == binding.Generation, err
}
