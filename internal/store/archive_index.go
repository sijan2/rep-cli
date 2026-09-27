package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

const maxArchiveIndexBytes = 8 << 20
const archiveIndexVersion = 1

var errArchiveIndexLimit = errors.New("archive metadata index exceeds its byte budget")

type archiveStamp struct {
	Device   uint64 `json:"device"`
	Inode    uint64 `json:"inode"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified_ns"`
}

type archiveOffset struct {
	Action    string `json:"action"`
	ID        string `json:"id,omitempty"`
	HashID    string `json:"hash_id,omitempty"`
	Timestamp int64  `json:"timestamp"`
	Offset    int64  `json:"offset"`
	Length    int64  `json:"length"`
}

type archiveOffsetIndex struct {
	Version int             `json:"version"`
	Source  archiveStamp    `json:"source"`
	Entries []archiveOffset `json:"entries"`
}

// One immutable metadata index is cached per process, not one for every task.
// No response payloads enter this cache. The on-disk representation is capped.
var archiveCache struct {
	sync.Mutex
	path  string
	index *archiveOffsetIndex
}

// LoadIndexedSession resolves an explicit archive without loading other archived
// response bodies. handled=false requests the existing legacy-store fallback
// when the JSONL source is absent or the bounded metadata index cannot fit.
// A present but corrupt source is an error, never an empty successful lookup.
func LoadIndexedSession(selector string) (*Session, bool, error) {
	if selector == "" {
		return nil, true, fmt.Errorf("saved session selector is required")
	}
	path, err := GetSessionsFilePath()
	if err != nil {
		return nil, true, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH); err != nil {
		return nil, true, err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	info, err := file.Stat()
	if err != nil {
		return nil, true, err
	}
	stamp, err := archiveFileStamp(info)
	if err != nil {
		return nil, true, err
	}
	index := readArchiveIndex(path, stamp)
	if index == nil {
		index, err = buildArchiveIndex(file, stamp)
		if errors.Is(err, errArchiveIndexLimit) {
			return nil, false, nil
		}
		if err != nil {
			return nil, true, err
		}
		// Index storage is a cache. Read-only directories must not prevent an
		// otherwise valid exact archive read.
		_ = writeArchiveIndex(path, index)
		cacheArchiveIndex(path, index)
	}
	selected, err := selectArchiveOffset(index.Entries, selector)
	if err != nil || selected == nil {
		return nil, true, err
	}
	reader := io.NewSectionReader(file, selected.Offset, selected.Length)
	entry, err := decodeArchiveIndexEntry(json.NewDecoder(reader), true)
	if err != nil {
		return nil, true, err
	}
	if entry.Action != "session" || entry.Session == nil {
		return nil, true, fmt.Errorf("archive index points to an invalid session")
	}
	session := entry.Session
	if session.HashID == "" {
		session.HashID = GenerateHashID("s", session.ID, strconv.FormatInt(session.Timestamp, 10))
	}
	ts := session.Timestamp
	if ts == 0 {
		ts = entry.Timestamp
	}
	if session.ID != selected.ID || session.HashID != selected.HashID || ts != selected.Timestamp {
		return nil, true, fmt.Errorf("archive index metadata differs from saved session")
	}
	for i := range session.Requests {
		ComputeRequestFields(&session.Requests[i])
	}
	return session, true, nil
}

func archiveFileStamp(info os.FileInfo) (archiveStamp, error) {
	if !info.Mode().IsRegular() {
		return archiveStamp{}, fmt.Errorf("session log must be a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return archiveStamp{}, fmt.Errorf("session log file identity is unavailable")
	}
	return archiveStamp{Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Size: info.Size(), Modified: info.ModTime().UnixNano()}, nil
}

func archiveIndexPath(path string) string {
	return filepath.Join(filepath.Dir(path), ".sessions-offsets-v1.json")
}

func readArchiveIndex(path string, stamp archiveStamp) *archiveOffsetIndex {
	archiveCache.Lock()
	if archiveCache.path == path && archiveCache.index != nil && archiveCache.index.Source == stamp {
		index := archiveCache.index
		archiveCache.Unlock()
		return index
	}
	archiveCache.Unlock()
	f, err := os.Open(archiveIndexPath(path))
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxArchiveIndexBytes {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, maxArchiveIndexBytes+1))
	if err != nil || len(data) > maxArchiveIndexBytes {
		return nil
	}
	var index archiveOffsetIndex
	if json.Unmarshal(data, &index) != nil || index.Version != archiveIndexVersion || index.Source != stamp {
		return nil
	}
	for _, entry := range index.Entries {
		if entry.Offset < 0 || entry.Length <= 0 || entry.Offset > stamp.Size || entry.Length > stamp.Size-entry.Offset {
			return nil
		}
		if entry.Action != "clear" && entry.Action != "session" {
			return nil
		}
	}
	cacheArchiveIndex(path, &index)
	return &index
}

func cacheArchiveIndex(path string, index *archiveOffsetIndex) {
	archiveCache.Lock()
	archiveCache.path = path
	archiveCache.index = index
	archiveCache.Unlock()
}

func writeArchiveIndex(path string, index *archiveOffsetIndex) error {
	data, err := json.Marshal(index)
	if err != nil {
		return err
	}
	if len(data) > maxArchiveIndexBytes {
		return errArchiveIndexLimit
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rep-archive-index-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp.Name(), archiveIndexPath(path))
}

func buildArchiveIndex(file *os.File, stamp archiveStamp) (*archiveOffsetIndex, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	index := &archiveOffsetIndex{Version: archiveIndexVersion, Source: stamp, Entries: []archiveOffset{}}
	decoder := json.NewDecoder(file)
	bytesUsed := 256
	for {
		offset := decoder.InputOffset()
		entry, err := decodeArchiveIndexEntry(decoder, false)
		if err == io.EOF {
			return index, nil
		}
		if err != nil {
			return nil, err
		}
		metadata, ok := archiveMetadata(entry, offset, decoder.InputOffset()-offset)
		if !ok {
			continue
		}
		data, _ := json.Marshal(metadata)
		bytesUsed += len(data) + 1
		if bytesUsed > maxArchiveIndexBytes {
			return nil, errArchiveIndexLimit
		}
		index.Entries = append(index.Entries, metadata)
	}
}

func archiveMetadata(entry sessionLogEntry, offset, length int64) (archiveOffset, bool) {
	result := archiveOffset{Action: entry.Action, Offset: offset, Length: length, Timestamp: entry.Timestamp}
	if entry.Action == "clear" {
		return result, true
	}
	if entry.Action != "session" || entry.Session == nil {
		return archiveOffset{}, false
	}
	session := entry.Session
	result.ID = session.ID
	result.HashID = session.HashID
	if result.HashID == "" {
		result.HashID = GenerateHashID("s", session.ID, strconv.FormatInt(session.Timestamp, 10))
	}
	if session.Timestamp != 0 {
		result.Timestamp = session.Timestamp
	}
	return result, true
}

func selectArchiveOffset(entries []archiveOffset, selector string) (*archiveOffset, error) {
	var cutoff int64
	for _, entry := range entries {
		if entry.Action == "clear" && entry.Timestamp > cutoff {
			cutoff = entry.Timestamp
		}
	}
	active := make([]archiveOffset, 0, len(entries))
	for _, entry := range entries {
		if entry.Action == "session" && entry.Timestamp >= cutoff && entry.HashID != "" {
			active = append(active, entry)
		}
	}
	sort.SliceStable(active, func(i, j int) bool { return active[i].Timestamp < active[j].Timestamp })
	seen := map[string]bool{}
	unique := active[:0]
	for _, entry := range active {
		if !seen[entry.HashID] {
			unique = append(unique, entry)
			seen[entry.HashID] = true
		}
	}
	if len(unique) == 0 {
		return nil, nil
	}
	if selector == "latest" || selector == "last" {
		return &unique[len(unique)-1], nil
	}
	for _, hash := range []bool{true, false} {
		var match *archiveOffset
		for i := range unique {
			value := unique[i].ID
			if hash {
				value = unique[i].HashID
			}
			if value != selector {
				continue
			}
			if match != nil {
				return nil, fmt.Errorf("saved session ID is ambiguous; use its full hash ID")
			}
			match = &unique[i]
		}
		if match != nil {
			return match, nil
		}
	}
	var match *archiveOffset
	for i := range unique {
		if !strings.HasPrefix(unique[i].ID, selector) && !strings.HasPrefix(unique[i].HashID, selector) {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("saved session prefix is ambiguous; use its full hash ID")
		}
		match = &unique[i]
	}
	return match, nil
}

func decodeArchiveIndexEntry(decoder *json.Decoder, requests bool) (entry sessionLogEntry, err error) {
	token, err := decoder.Token()
	if err != nil {
		return entry, err
	}
	defer func() {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
	}()
	if token != json.Delim('{') {
		return entry, fmt.Errorf("session log entry must be an object")
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return entry, err
		}
		switch key {
		case "action":
			err = decoder.Decode(&entry.Action)
		case "timestamp":
			err = decoder.Decode(&entry.Timestamp)
		case "session":
			if requests {
				entry.Session, err = decodeSessionStream(decoder)
			} else {
				entry.Session, err = decodeArchiveSessionMetadata(decoder)
			}
		default:
			err = skipArchiveValue(decoder)
		}
		if err != nil {
			return entry, err
		}
	}
	_, err = decoder.Token()
	return entry, err
}

func decodeArchiveSessionMetadata(decoder *json.Decoder) (*Session, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if token == nil {
		return nil, nil
	}
	if token != json.Delim('{') {
		return nil, fmt.Errorf("session must be an object")
	}
	session := &Session{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch key {
		case "id":
			err = decoder.Decode(&session.ID)
		case "hash_id":
			err = decoder.Decode(&session.HashID)
		case "timestamp":
			err = decoder.Decode(&session.Timestamp)
		default:
			err = skipArchiveValue(decoder)
		}
		if err != nil {
			return nil, err
		}
	}
	_, err = decoder.Token()
	return session, err
}

// Decoder tokens validate skipped JSON while retaining at most one scalar value,
// instead of unmarshalling request arrays or every historical response body.
func skipArchiveValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return fmt.Errorf("unexpected JSON delimiter")
	}
	depth := 1
	for depth > 0 {
		token, err = decoder.Token()
		if err != nil {
			return err
		}
		if delim, ok = token.(json.Delim); ok {
			switch delim {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

func encodeArchiveClear(writer io.Writer, entry sessionLogEntry) error {
	return json.NewEncoder(writer).Encode(entry)
}

// The log's exclusive append lock is held by the caller. Missing or stale
// indexes are invalidated for lazy rebuilding; successful appends never depend
// on cache availability and never rescan historical payloads here.
func updateArchiveIndexAfterAppend(path string, file *os.File, before os.FileInfo, offset, length int64, entry sessionLogEntry) {
	oldStamp, err := archiveFileStamp(before)
	if err != nil {
		return
	}
	index := readArchiveIndex(path, oldStamp)
	if index == nil && offset == 0 {
		index = &archiveOffsetIndex{Version: archiveIndexVersion, Source: oldStamp}
	}
	if index == nil {
		return
	}
	info, err := file.Stat()
	if err != nil {
		return
	}
	stamp, err := archiveFileStamp(info)
	if err != nil {
		return
	}
	updated := &archiveOffsetIndex{Version: archiveIndexVersion, Source: stamp, Entries: append([]archiveOffset(nil), index.Entries...)}
	if metadata, ok := archiveMetadata(entry, offset, length); ok {
		updated.Entries = append(updated.Entries, metadata)
	}
	if err := writeArchiveIndex(path, updated); err == nil {
		cacheArchiveIndex(path, updated)
	}
}
