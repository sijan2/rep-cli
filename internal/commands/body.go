package commands

import (
	"fmt"
	"os"

	"github.com/repplus/rep-cli/internal/store"
)

func findRequestByAnyID(s *store.Store, requestID string) *store.Request {
	var requests []store.Request
	for _, session := range s.Sessions {
		requests = append(requests, session.Requests...)
	}
	return store.BuildIndex(requests).GetByAny(requestID)
}

// A canonical live ID explicitly identifies the current capture. Otherwise
// exact archived IDs win over live prefixes, and aliases/prefixes must be
// unique across all candidate requests. --saved pins one immutable source.
func findBodyWebRequest(requestID, saved string) (*store.Request, error) {
	if saved != "" {
		if session, handled, err := store.LoadIndexedSession(saved); handled {
			if err != nil {
				return nil, err
			}
			if session == nil {
				return nil, fmt.Errorf("saved session not found in this task")
			}
			return store.BuildIndex(session.Requests).GetByAny(requestID), nil
		}
		persistent, err := store.Load()
		if err != nil {
			return nil, fmt.Errorf("cannot read this task's archives")
		}
		var session *store.Session
		if saved == "latest" || saved == "last" {
			session = persistent.GetLatestSession()
		} else {
			session, err = selectSummaryArchive(persistent.Sessions, saved)
			if err != nil {
				return nil, err
			}
		}
		if session == nil {
			return nil, fmt.Errorf("saved session not found in this task")
		}
		return store.BuildIndex(session.Requests).GetByAny(requestID), nil
	}
	livePath, err := store.GetLiveFilePath()
	if err != nil {
		return nil, err
	}
	export, err := loadLiveExport(livePath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("this task's live capture is unreadable; use --saved to select an archive explicitly")
	}
	for index := len(export.Requests) - 1; index >= 0; index-- {
		if export.Requests[index].ID == requestID {
			store.ComputeRequestFields(&export.Requests[index])
			return &export.Requests[index], nil
		}
	}
	persistent, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("cannot read this task's archives")
	}
	var requests []store.Request
	for _, session := range persistent.Sessions {
		requests = append(requests, session.Requests...)
	}
	requests = append(requests, export.Requests...)
	return store.BuildIndex(requests).GetByAny(requestID), nil
}
