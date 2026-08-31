package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

type sessionLogEntry struct {
	Action    string   `json:"action"`
	Session   *Session `json:"session,omitempty"`
	Timestamp int64    `json:"timestamp,omitempty"`
}

func GetSessionsFilePath() (string, error) {
	dataDir, err := GetStorePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "sessions.jsonl"), nil
}

func AppendSessionLog(session *Session) error {
	if session == nil {
		return nil
	}
	if session.HashID == "" {
		session.HashID = GenerateHashID("s", session.ID, strconv.FormatInt(session.Timestamp, 10))
	}

	path, err := GetSessionsFilePath()
	if err != nil {
		return err
	}

	entry := sessionLogEntry{
		Action:  "session",
		Session: session,
	}
	return appendJSONL(path, entry)
}

func AppendSessionClear(ts time.Time) error {
	path, err := GetSessionsFilePath()
	if err != nil {
		return err
	}
	entry := sessionLogEntry{
		Action:    "clear",
		Timestamp: ts.UnixMilli(),
	}
	return appendJSONL(path, entry)
}

func LoadSessionsLog() ([]Session, bool, error) {
	path, err := GetSessionsFilePath()
	if err != nil {
		return nil, false, err
	}

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}

	type entry struct {
		line   int
		ts     int64
		record Session
	}

	var entries []entry
	var clearCutoff int64
	lineNum := 0

	readErr := readJSONLLines(path, func(line []byte) error {
		lineNum++
		var raw sessionLogEntry
		if err := json.Unmarshal(line, &raw); err != nil {
			return err
		}

		switch raw.Action {
		case "clear":
			if raw.Timestamp > clearCutoff {
				clearCutoff = raw.Timestamp
			}
		case "session":
			if raw.Session == nil {
				return nil
			}
			record := *raw.Session
			if record.HashID == "" {
				record.HashID = GenerateHashID("s", record.ID, strconv.FormatInt(record.Timestamp, 10))
			}
			ts := record.Timestamp
			if ts == 0 {
				ts = raw.Timestamp
			}
			entries = append(entries, entry{
				line:   lineNum,
				ts:     ts,
				record: record,
			})
		}
		return nil
	})
	if readErr != nil {
		return nil, true, readErr
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].ts == entries[j].ts {
			return entries[i].line < entries[j].line
		}
		return entries[i].ts < entries[j].ts
	})

	seen := make(map[string]bool)
	var sessions []Session
	for _, entry := range entries {
		if entry.ts < clearCutoff {
			continue
		}
		if entry.record.HashID == "" {
			continue
		}
		if seen[entry.record.HashID] {
			continue
		}
		seen[entry.record.HashID] = true
		sessions = append(sessions, entry.record)
	}

	return sessions, true, nil
}
