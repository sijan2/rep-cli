package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Note represents a persistent finding or observation
type Note struct {
	ID        int       `json:"id"`
	HashID    string    `json:"hash_id"`
	Content   string    `json:"content"`
	Refs      []string  `json:"refs,omitempty"` // Request IDs
	Tags      []string  `json:"tags,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// NotesStore holds all notes for a session
type NotesStore struct {
	Session   string    `json:"session"`
	Notes     []Note    `json:"notes"`
	CreatedAt time.Time `json:"created_at"`
}

type noteRecord struct {
	HashID    string    `json:"id"`
	Content   string    `json:"content"`
	Refs      []string  `json:"refs,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

type noteLogEntry struct {
	Action    string      `json:"action"`
	Note      *noteRecord `json:"note,omitempty"`
	NoteID    string      `json:"note_id,omitempty"`
	Timestamp int64       `json:"timestamp,omitempty"`
}

// GetNotesFilePath returns the path to the notes log
func GetNotesFilePath() (string, error) {
	dataDir, err := GetStorePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "notes.jsonl"), nil
}

// LoadNotes loads the notes from disk
func LoadNotes() (*NotesStore, error) {
	logPath, err := GetNotesFilePath()
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(logPath); err == nil {
		return loadNotesFromLog(logPath)
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	legacyPath := filepath.Join(filepath.Dir(logPath), "notes.json")
	if _, err := os.Stat(legacyPath); err == nil {
		legacyStore, err := loadLegacyNotes(legacyPath)
		if err != nil {
			return nil, err
		}
		for i := range legacyStore.Notes {
			if legacyStore.Notes[i].HashID == "" {
				legacyStore.Notes[i].HashID = GenerateHashID(
					"n",
					legacyStore.Notes[i].Content,
					strconv.FormatInt(legacyStore.Notes[i].Timestamp.UnixNano(), 10),
					strings.Join(legacyStore.Notes[i].Refs, ","),
					strings.Join(legacyStore.Notes[i].Tags, ","),
				)
			}
			_ = AppendNoteLog(&legacyStore.Notes[i])
		}
		return legacyStore, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	return &NotesStore{
		Session:   time.Now().Format("2006-01-02"),
		Notes:     []Note{},
		CreatedAt: time.Now(),
	}, nil
}

// SaveNotes rewrites the notes log from the in-memory store
func SaveNotes(store *NotesStore) error {
	if store == nil {
		return nil
	}
	path, err := GetNotesFilePath()
	if err != nil {
		return err
	}

	if err := EnsureStoreDir(); err != nil {
		return err
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	file.Close()

	for i := range store.Notes {
		note := store.Notes[i]
		if note.HashID == "" {
			note.HashID = GenerateHashID(
				"n",
				note.Content,
				strconv.FormatInt(note.Timestamp.UnixNano(), 10),
				strings.Join(note.Refs, ","),
				strings.Join(note.Tags, ","),
			)
		}
		if err := AppendNoteLog(&note); err != nil {
			return err
		}
	}

	return nil
}

// AddNote adds a new note to the store
func (ns *NotesStore) AddNote(content string, refs []string, tags []string) Note {
	// Get next ID
	maxID := 0
	for _, n := range ns.Notes {
		if n.ID > maxID {
			maxID = n.ID
		}
	}

	ts := time.Now()
	note := Note{
		ID:        maxID + 1,
		HashID:    GenerateHashID("n", content, strconv.FormatInt(ts.UnixNano(), 10), strings.Join(refs, ","), strings.Join(tags, ",")),
		Content:   content,
		Refs:      refs,
		Tags:      tags,
		Timestamp: ts,
	}

	ns.Notes = append(ns.Notes, note)
	return note
}

// GetNote returns a note by ID
func (ns *NotesStore) GetNote(id int) *Note {
	for i := range ns.Notes {
		if ns.Notes[i].ID == id {
			return &ns.Notes[i]
		}
	}
	return nil
}

// DeleteNote removes a note by hash ID
func (ns *NotesStore) DeleteNote(hashID string) bool {
	for i := range ns.Notes {
		if ns.Notes[i].HashID == hashID {
			ns.Notes = append(ns.Notes[:i], ns.Notes[i+1:]...)
			return true
		}
	}
	return false
}

// ClearNotes removes all notes
func (ns *NotesStore) ClearNotes() {
	ns.Notes = []Note{}
}

func (ns *NotesStore) FindNoteByIdentifier(identifier string) *Note {
	if identifier == "" {
		return nil
	}
	if isDigits(identifier) {
		id, err := strconv.Atoi(identifier)
		if err == nil {
			return ns.GetNote(id)
		}
	}
	for i := range ns.Notes {
		if ns.Notes[i].HashID == identifier {
			return &ns.Notes[i]
		}
	}
	for i := range ns.Notes {
		if strings.HasPrefix(ns.Notes[i].HashID, identifier) {
			return &ns.Notes[i]
		}
	}
	return nil
}

func AppendNoteLog(note *Note) error {
	if note == nil {
		return nil
	}
	if note.HashID == "" {
		note.HashID = GenerateHashID("n", note.Content, strconv.FormatInt(note.Timestamp.UnixNano(), 10), strings.Join(note.Refs, ","), strings.Join(note.Tags, ","))
	}

	path, err := GetNotesFilePath()
	if err != nil {
		return err
	}

	record := noteRecord{
		HashID:    note.HashID,
		Content:   note.Content,
		Refs:      note.Refs,
		Tags:      note.Tags,
		Timestamp: note.Timestamp,
	}
	entry := noteLogEntry{
		Action: "note",
		Note:   &record,
	}
	return appendJSONL(path, entry)
}

func AppendNoteDelete(hashID string) error {
	if hashID == "" {
		return nil
	}
	path, err := GetNotesFilePath()
	if err != nil {
		return err
	}
	entry := noteLogEntry{
		Action:    "delete",
		NoteID:    hashID,
		Timestamp: time.Now().UnixMilli(),
	}
	return appendJSONL(path, entry)
}

func AppendNotesClear() error {
	path, err := GetNotesFilePath()
	if err != nil {
		return err
	}
	entry := noteLogEntry{
		Action:    "clear",
		Timestamp: time.Now().UnixMilli(),
	}
	return appendJSONL(path, entry)
}

func loadLegacyNotes(path string) (*NotesStore, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var store NotesStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	if store.Notes == nil {
		store.Notes = []Note{}
	}
	if store.Session == "" {
		store.Session = time.Now().Format("2006-01-02")
	}
	if store.CreatedAt.IsZero() {
		store.CreatedAt = time.Now()
	}
	return &store, nil
}

func loadNotesFromLog(path string) (*NotesStore, error) {
	type entry struct {
		line int
		ts   int64
		raw  noteLogEntry
	}

	var entries []entry
	var clearCutoff int64
	lineNum := 0

	readErr := readJSONLLines(path, func(line []byte) error {
		lineNum++
		var raw noteLogEntry
		if err := json.Unmarshal(line, &raw); err != nil {
			return err
		}
		ts := raw.Timestamp
		if raw.Note != nil && !raw.Note.Timestamp.IsZero() {
			ts = raw.Note.Timestamp.UnixMilli()
		}
		if raw.Action == "clear" && ts > clearCutoff {
			clearCutoff = ts
		}
		entries = append(entries, entry{
			line: lineNum,
			ts:   ts,
			raw:  raw,
		})
		return nil
	})
	if readErr != nil {
		return nil, readErr
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].ts == entries[j].ts {
			return entries[i].line < entries[j].line
		}
		return entries[i].ts < entries[j].ts
	})

	noteMap := make(map[string]Note)
	var order []string

	for _, entry := range entries {
		if entry.ts < clearCutoff {
			continue
		}
		switch entry.raw.Action {
		case "note":
			if entry.raw.Note == nil {
				continue
			}
			record := entry.raw.Note
			note := Note{
				HashID:    record.HashID,
				Content:   record.Content,
				Refs:      record.Refs,
				Tags:      record.Tags,
				Timestamp: record.Timestamp,
			}
			if note.HashID == "" {
				note.HashID = GenerateHashID("n", note.Content, strconv.FormatInt(note.Timestamp.UnixNano(), 10), strings.Join(note.Refs, ","), strings.Join(note.Tags, ","))
			}
			if _, exists := noteMap[note.HashID]; !exists {
				order = append(order, note.HashID)
			}
			noteMap[note.HashID] = note
		case "delete":
			if entry.raw.NoteID == "" {
				continue
			}
			delete(noteMap, entry.raw.NoteID)
		}
	}

	var notes []Note
	for _, hashID := range order {
		if note, ok := noteMap[hashID]; ok {
			notes = append(notes, note)
		}
	}
	sort.SliceStable(notes, func(i, j int) bool {
		if notes[i].Timestamp.Equal(notes[j].Timestamp) {
			return notes[i].HashID < notes[j].HashID
		}
		return notes[i].Timestamp.Before(notes[j].Timestamp)
	})
	for i := range notes {
		notes[i].ID = i + 1
	}

	return &NotesStore{
		Session:   time.Now().Format("2006-01-02"),
		Notes:     notes,
		CreatedAt: time.Now(),
	}, nil
}

func isDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != ""
}
