package commands

import (
	"errors"
	"fmt"
	"os"

	"github.com/repplus/rep-cli/internal/scope"
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

func loadObservedCapture(saved string) (observedCapture, error) {
	var result observedCapture
	selected, err := scope.Current()
	if err != nil {
		return result, err
	}
	result.Provenance = observedCaptureProvenance{
		Workspace: selected.Workspace, Task: selected.Task,
		Source: "live", SourceStatus: "no_capture", LegacyGlobal: !selected.Scoped,
	}
	if saved == "" {
		path, err := store.GetLiveFilePath()
		if err != nil {
			return result, err
		}
		result.Export, err = loadLiveExport(path)
		if os.IsNotExist(err) {
			return result, nil
		}
		if err != nil {
			return result, fmt.Errorf("selected live capture is unreadable: %w", err)
		}
		result.Provenance.SourceStatus = "captured"
	} else {
		session, indexed, err := store.LoadIndexedSession(saved)
		if err != nil {
			return result, err
		}
		if !indexed {
			persistent, err := store.Load()
			if err != nil {
				return result, err
			}
			if saved == "latest" || saved == "last" {
				session = persistent.GetLatestSession()
			} else {
				session, err = selectSummaryArchive(persistent.Sessions, saved)
				if err != nil {
					return result, err
				}
			}
		}
		if session == nil {
			return result, errors.New("saved session not found in this task")
		}
		result.Export = captureCoverageExport(session)
		result.Provenance.Source = "saved"
		result.Provenance.SourceStatus = "saved"
		result.Provenance.SavedID = session.ID
		result.Provenance.SavedHash = session.HashID
	}
	result.Provenance.CaptureSessionID = result.Export.SessionID
	return result, nil
}

func (c observedCapture) coverage() CaptureCoverageReport {
	source := c.Provenance.Source
	if c.Provenance.SourceStatus == "no_capture" {
		source = "no_capture"
	}
	return buildCaptureCoverage(c.Export, source)
}
