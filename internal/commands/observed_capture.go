package commands

import (
	"github.com/repplus/rep-cli/internal/store"
)

// observedCapture selects one dataset and retains its collector metadata.
// An absent live capture never falls back to an archive or another task.
type observedCapture struct {
	Export     store.Export
	Provenance observedCaptureProvenance
}

type observedCaptureProvenance struct {
	Workspace        string `json:"workspace,omitempty"`
	Task             string `json:"task,omitempty"`
	Source           string `json:"source"`
	SourceStatus     string `json:"source_status"`
	SavedID          string `json:"saved_id,omitempty"`
	SavedHash        string `json:"saved_hash,omitempty"`
	CaptureSessionID string `json:"capture_session_id,omitempty"`
	LegacyGlobal     bool   `json:"legacy_global"`
}

func (c observedCapture) coverage() CaptureCoverageReport {
	source := c.Provenance.Source
	if c.Provenance.SourceStatus == "no_capture" {
		source = "no_capture"
	}
	return buildCaptureCoverage(c.Export, source)
}
