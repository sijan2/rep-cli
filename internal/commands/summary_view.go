package commands

import (
	"errors"

	"strings"

	"github.com/repplus/rep-cli/internal/store"
)

// Match full hashes first, full IDs second, then a unique prefix. Timestamp IDs
// can collide; selecting an arbitrary first archive would hide source changes.
func selectSummaryArchive(sessions []store.Session, selector string) (*store.Session, error) {
	for _, byHash := range []bool{true, false} {
		var exact *store.Session
		for index := range sessions {
			candidate := &sessions[index]
			value := candidate.ID
			if byHash {
				value = candidate.HashID
			}
			if value != selector {
				continue
			}
			if exact != nil {
				return nil, errors.New("saved session ID is ambiguous; use its full hash ID")
			}
			exact = candidate
		}
		if exact != nil {
			return exact, nil
		}
	}
	var match *store.Session
	for index := range sessions {
		candidate := &sessions[index]
		if strings.HasPrefix(candidate.ID, selector) || strings.HasPrefix(candidate.HashID, selector) {
			if match != nil {
				return nil, errors.New("saved session prefix is ambiguous; use its full hash ID")
			}
			match = candidate
		}
	}
	return match, nil
}
