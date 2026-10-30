package commands

import (
	"github.com/repplus/rep-cli/internal/store"
)

func findRequestByID(id string) *store.Request {
	// Try live.json first
	livePath, err := store.GetLiveFilePath()
	if err == nil {
		if export, err := loadLiveExport(livePath); err == nil {
			idx := store.BuildIndex(export.Requests)
			if req := idx.GetByAny(id); req != nil {
				return req
			}
		}
	}

	// Try store
	s, err := store.Get()
	if err != nil {
		return nil
	}

	return findRequestByAnyID(s, id)
}
