package evidence

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Store struct{ dir string }

// Open has constant work independent of history size. It never loads history or
// changes a current-run pointer. The caller supplies the selected task directory.
func Open(taskDir string) (*Store, error) {
	if strings.TrimSpace(taskDir) == "" {
		return nil, fmt.Errorf("task directory is required")
	}
	dir, err := filepath.Abs(filepath.Join(taskDir, "evidence", "v1"))
	if err != nil {
		return nil, err
	}
	for _, path := range []string{dir, filepath.Join(dir, "runs"), filepath.Join(dir, "blobs")} {
		if err := privateDir(path); err != nil {
			return nil, err
		}
	}
	return &Store{dir: dir}, nil
}

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("evidence path is not a directory: %s", path)
	}
	return os.Chmod(path, 0700)
}

func randomID(prefix string) (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(data[:]), nil
}

func validID(id, prefix string) error {
	if !strings.HasPrefix(id, prefix) || len(id) != len(prefix)+32 {
		return fmt.Errorf("invalid %s identifier", strings.TrimSuffix(prefix, "_"))
	}
	if _, err := hex.DecodeString(id[len(prefix):]); err != nil || strings.ToLower(id) != id {
		return fmt.Errorf("invalid %s identifier", strings.TrimSuffix(prefix, "_"))
	}
	return nil
}

func (s *Store) runDir(id string) (string, error) {
	if err := validID(id, "run_"); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, "runs", id), nil
}

func (s *Store) BeginRun(intent, stop string, identity map[string]string) (Run, error) {
	if strings.TrimSpace(intent) == "" {
		return Run{}, fmt.Errorf("run intent is required")
	}
	id, err := randomID("run_")
	if err != nil {
		return Run{}, err
	}
	run := Run{Version: Version, ID: id, Intent: intent, Stop: stop, Identity: identity, CreatedAt: time.Now().UTC()}
	err = s.locked(s.dir, func() error {
		n, err := indexCount(filepath.Join(s.dir, "runs.idx"), "run_")
		if err != nil {
			return err
		}
		run.Sequence = n + 1
		if _, err := recordJSON(run); err != nil {
			return err
		}
		dir, _ := s.runDir(id)
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		for _, name := range []string{"operations", "artifacts"} {
			if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
				return err
			}
		}
		if err := writeJSON(filepath.Join(dir, "run.json"), run); err != nil {
			return err
		}
		if err := syncDir(filepath.Dir(dir)); err != nil {
			return err
		}
		return appendIndex(filepath.Join(s.dir, "runs.idx"), id)
	})
	return run, err
}

func (s *Store) LoadRun(id string) (Run, error) {
	dir, err := s.runDir(id)
	if err != nil {
		return Run{}, err
	}
	var run Run
	if err = readJSON(filepath.Join(dir, "run.json"), &run); err != nil {
		return Run{}, err
	}
	if run.ID != id || run.Version != Version || run.Sequence == 0 {
		return Run{}, fmt.Errorf("invalid run manifest")
	}
	return run, nil
}

// ListRuns returns the most recently created runs first, without scanning older
// manifests. ListRunPage provides chronological cursor pagination over all runs.
func (s *Store) ListRuns(limit int) ([]Run, error) {
	limit, err := pageLimit(limit)
	if err != nil {
		return nil, err
	}
	result := []Run{}
	err = s.locked(s.dir, func() error {
		n, err := indexCount(filepath.Join(s.dir, "runs.idx"), "run_")
		if err != nil {
			return err
		}
		var consumed int
		for i := n; i > 0 && len(result) < limit; i-- {
			id, err := indexID(filepath.Join(s.dir, "runs.idx"), "run_", i)
			if err != nil {
				return err
			}
			run, err := s.LoadRun(id)
			if err != nil {
				return err
			}
			data, _ := json.Marshal(run)
			if len(result) > 0 && consumed+len(data) > MaxPageBytes {
				break
			}
			consumed += len(data)
			result = append(result, run)
		}
		return nil
	})
	return result, err
}

func (s *Store) ListRunPage(after string, limit int) (RunPage, error) {
	page := RunPage{Runs: []Run{}}
	limit, err := pageLimit(limit)
	if err != nil {
		return page, err
	}
	var start uint64
	if after != "" {
		run, err := s.LoadRun(after)
		if err != nil {
			return page, err
		}
		start = run.Sequence
	}
	err = s.locked(s.dir, func() error {
		index := filepath.Join(s.dir, "runs.idx")
		n, err := indexCount(index, "run_")
		if err != nil {
			return err
		}
		page.Total = n
		if err := checkCursor(index, "run_", after, start, n); err != nil {
			return err
		}
		used := 0
		end := start
		for i := start + 1; i <= n && len(page.Runs) < limit; i++ {
			id, err := indexID(index, "run_", i)
			if err != nil {
				return err
			}
			run, err := s.LoadRun(id)
			if err != nil {
				return err
			}
			data, _ := json.Marshal(run)
			if len(page.Runs) > 0 && used+len(data) > MaxPageBytes {
				break
			}
			used += len(data)
			page.Runs = append(page.Runs, run)
			page.Next = id
			end = i
		}
		page.HasMore = end < n
		return nil
	})
	return page, err
}

func (s *Store) BeginOperation(runID string, op Operation) (Operation, error) {
	op.Phase = "started"
	op.Status = StatusPending
	op.Verification = "unverified"
	if op.StartedAt.IsZero() {
		op.StartedAt = time.Now().UTC()
	}
	op.FinishedAt = time.Time{}
	return s.RecordOperation(runID, op)
}

// FinishOperation appends a separate immutable result. The original pending
// record remains available if execution or persistence was interrupted.
func (s *Store) FinishOperation(runID, operationID string, result Operation) (Operation, error) {
	start, err := s.LoadOperation(runID, operationID)
	if err != nil {
		return Operation{}, err
	}
	if start.Phase != "started" || start.Status != StatusPending {
		return Operation{}, fmt.Errorf("operation is not a pending start")
	}
	if result.Status == "" || result.Status == StatusPending {
		return Operation{}, fmt.Errorf("finish status must be completed, failed, or unknown")
	}
	result.ID = ""
	result.ParentID = operationID
	result.Phase = "finished"
	result.Kind = start.Kind
	result.StartedAt = start.StartedAt
	if result.FinishedAt.IsZero() {
		result.FinishedAt = time.Now().UTC()
	}
	return s.RecordOperation(runID, result)
}

func (s *Store) RecordOperation(runID string, op Operation) (Operation, error) {
	if _, err := s.LoadRun(runID); err != nil {
		return Operation{}, err
	}
	if op.ID != "" || op.RunID != "" && op.RunID != runID {
		return Operation{}, fmt.Errorf("operation IDs are assigned by the store")
	}
	if strings.TrimSpace(op.Kind) == "" {
		return Operation{}, fmt.Errorf("operation kind is required")
	}
	switch op.Status {
	case StatusPending, StatusCompleted, StatusFailed, StatusUnknown:
	default:
		return Operation{}, fmt.Errorf("invalid operation status %q", op.Status)
	}
	if op.Phase == "" {
		op.Phase = "recorded"
	}
	switch op.Phase {
	case "started", "finished", "recorded":
	default:
		return Operation{}, fmt.Errorf("invalid operation phase")
	}
	if op.Verification == "" {
		op.Verification = "unverified"
	}
	switch op.Verification {
	case "unverified", "satisfied", "unsatisfied", "unknown":
	default:
		return Operation{}, fmt.Errorf("invalid verification status")
	}
	if op.ParentID != "" {
		if _, err := s.LoadOperation(runID, op.ParentID); err != nil {
			return Operation{}, fmt.Errorf("parent: %w", err)
		}
	}
	for _, id := range op.ArtifactIDs {
		if _, err := s.LoadArtifact(runID, id); err != nil {
			return Operation{}, fmt.Errorf("artifact: %w", err)
		}
	}
	for _, ref := range op.References {
		if ref.Kind == "" {
			return Operation{}, fmt.Errorf("reference kind is required")
		}
	}
	id, err := randomID("op_")
	if err != nil {
		return Operation{}, err
	}
	op.ID = id
	op.RunID = runID
	op.Version = Version
	op.RecordedAt = time.Now().UTC()
	dir, _ := s.runDir(runID)
	err = s.locked(dir, func() error {
		index := filepath.Join(dir, "operations.idx")
		n, err := indexCount(index, "op_")
		if err != nil {
			return err
		}
		op.Sequence = n + 1
		if err := writeJSON(filepath.Join(dir, "operations", id+".json"), op); err != nil {
			return err
		}
		return appendIndex(index, id)
	})
	return op, err
}

func (s *Store) LoadOperation(runID, id string) (Operation, error) {
	dir, err := s.runDir(runID)
	if err != nil {
		return Operation{}, err
	}
	if err := validID(id, "op_"); err != nil {
		return Operation{}, err
	}
	var op Operation
	if err := readJSON(filepath.Join(dir, "operations", id+".json"), &op); err != nil {
		return Operation{}, err
	}
	if op.ID != id || op.RunID != runID || op.Version != Version || op.Sequence == 0 {
		return Operation{}, fmt.Errorf("invalid operation manifest")
	}
	return op, nil
}

func (s *Store) ListOperations(runID, after string, limit int) (Page, error) {
	page := Page{RunID: runID, Operations: []Operation{}}
	limit, err := pageLimit(limit)
	if err != nil {
		return page, err
	}
	if _, err := s.LoadRun(runID); err != nil {
		return page, err
	}
	var start uint64
	if after != "" {
		op, err := s.LoadOperation(runID, after)
		if err != nil {
			return page, err
		}
		start = op.Sequence
	}
	dir, _ := s.runDir(runID)
	err = s.locked(dir, func() error {
		index := filepath.Join(dir, "operations.idx")
		n, err := indexCount(index, "op_")
		if err != nil {
			return err
		}
		page.Total = n
		if err := checkCursor(index, "op_", after, start, n); err != nil {
			return err
		}
		used := 0
		end := start
		for i := start + 1; i <= n && len(page.Operations) < limit; i++ {
			id, err := indexID(index, "op_", i)
			if err != nil {
				return err
			}
			op, err := s.LoadOperation(runID, id)
			if err != nil {
				return err
			}
			data, _ := json.Marshal(op)
			if len(page.Operations) > 0 && used+len(data) > MaxPageBytes {
				break
			}
			used += len(data)
			page.Operations = append(page.Operations, op)
			page.Next = id
			end = i
		}
		page.HasMore = end < n
		return nil
	})
	return page, err
}

func pageLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultLimit, nil
	}
	if limit < 1 || limit > MaxLimit {
		return 0, fmt.Errorf("limit must be between 1 and %d", MaxLimit)
	}
	return limit, nil
}

func (s *Store) locked(dir string, fn func() error) error {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return err
	}
	defer unlockFile(f)
	return fn()
}

func recordJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxRecordBytes {
		return nil, fmt.Errorf("manifest exceeds %d bytes; import large content as an artifact", MaxRecordBytes)
	}
	return append(data, '\n'), nil
}

func writeJSON(path string, value any) error {
	data, err := recordJSON(value)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return syncDir(filepath.Dir(path))
}

func readJSON(path string, value any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxRecordBytes+1 {
		return fmt.Errorf("invalid or oversized evidence manifest")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxRecordBytes+2))
	if err != nil {
		return err
	}
	if len(data) > MaxRecordBytes+1 {
		return fmt.Errorf("oversized evidence manifest")
	}
	return json.Unmarshal(data, value)
}

func indexCount(path, prefix string) (uint64, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	width := int64(len(prefix) + 33)
	if !info.Mode().IsRegular() || info.Size()%width != 0 {
		return 0, fmt.Errorf("incomplete evidence index %s; records have been preserved", filepath.Base(path))
	}
	return uint64(info.Size() / width), nil
}

func indexID(path, prefix string, seq uint64) (string, error) {
	if seq == 0 {
		return "", fmt.Errorf("invalid index position")
	}
	width := len(prefix) + 33
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data := make([]byte, width)
	if _, err := f.ReadAt(data, int64(seq-1)*int64(width)); err != nil {
		return "", err
	}
	id := string(data[:width-1])
	if data[width-1] != '\n' {
		return "", fmt.Errorf("incomplete evidence index record")
	}
	if err := validID(id, prefix); err != nil {
		return "", err
	}
	return id, nil
}

func checkCursor(path, prefix, after string, start, total uint64) error {
	if after == "" {
		return nil
	}
	if start == 0 || start > total {
		return fmt.Errorf("cursor is not in the committed index")
	}
	id, err := indexID(path, prefix, start)
	if err != nil {
		return err
	}
	if id != after {
		return fmt.Errorf("cursor does not match the committed index")
	}
	return nil
}

func appendIndex(path, id string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	data := []byte(id + "\n")
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
