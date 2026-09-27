package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ImportArtifact streams an ordinary local file into private content-addressed
// storage. It never invokes a decoder, subprocess, network request, or model.
// The original input is not modified. Matching bytes may share a blob while
// retaining distinct trial/collector manifests.
func (s *Store) ImportArtifact(runID string, spec ImportSpec) (Artifact, error) {
	if _, err := s.LoadRun(runID); err != nil {
		return Artifact{}, err
	}
	if spec.Path == "" {
		return Artifact{}, fmt.Errorf("artifact path is required")
	}
	if spec.Kind == "" {
		spec.Kind = "diagnostic"
	}
	if spec.Coverage.State == "" {
		spec.Coverage.State = "unknown"
	}
	switch spec.Coverage.State {
	case "unknown", "partial", "complete", "none":
	default:
		return Artifact{}, fmt.Errorf("coverage state must be unknown, partial, complete, or none")
	}
	if _, err := recordJSON(spec); err != nil {
		return Artifact{}, err
	}
	// Check before opening so a FIFO or device cannot block a routine import.
	info, err := os.Stat(spec.Path)
	if err != nil {
		return Artifact{}, err
	}
	if !info.Mode().IsRegular() {
		return Artifact{}, fmt.Errorf("artifact source must be a regular file")
	}
	source, err := os.Open(spec.Path)
	if err != nil {
		return Artifact{}, err
	}
	defer source.Close()
	before, err := source.Stat()
	if err != nil {
		return Artifact{}, err
	}
	if !before.Mode().IsRegular() {
		return Artifact{}, fmt.Errorf("artifact source must be a regular file")
	}
	tmp, err := os.CreateTemp(filepath.Join(s.dir, "blobs"), ".import-")
	if err != nil {
		return Artifact{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	defer tmp.Close()
	hash := sha256.New()
	size, err := io.CopyBuffer(io.MultiWriter(tmp, hash), source, make([]byte, 128<<10))
	if err != nil {
		return Artifact{}, err
	}
	after, err := source.Stat()
	if err != nil {
		return Artifact{}, err
	}
	if size != before.Size() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return Artifact{}, fmt.Errorf("artifact changed during import; close the recording and retry")
	}
	if err := tmp.Sync(); err != nil {
		return Artifact{}, err
	}
	if err := tmp.Chmod(0400); err != nil {
		return Artifact{}, err
	}
	if err := tmp.Close(); err != nil {
		return Artifact{}, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	blob := filepath.Join(s.dir, "blobs", digest)
	if err := os.Link(tmpName, blob); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return Artifact{}, err
		}
		// Never trust a filename alone when reusing existing content.
		if err := verifyBlob(blob, digest, size); err != nil {
			return Artifact{}, err
		}
	}
	if err := syncDir(filepath.Dir(blob)); err != nil {
		return Artifact{}, err
	}
	id, err := randomID("art_")
	if err != nil {
		return Artifact{}, err
	}
	a := Artifact{Version: Version, ID: id, RunID: runID, ImportedAt: time.Now().UTC(), Kind: spec.Kind,
		Collector: spec.Collector, Build: spec.Build, Device: spec.Device, Trial: spec.Trial,
		Coverage: spec.Coverage, Metadata: spec.Metadata, SourceName: filepath.Base(spec.Path),
		Path: artifactPath(digest), SHA256: digest, Size: size}
	dir, _ := s.runDir(runID)
	err = s.locked(dir, func() error {
		index := filepath.Join(dir, "artifacts.idx")
		n, err := indexCount(index, "art_")
		if err != nil {
			return err
		}
		a.Sequence = n + 1
		if err := writeJSON(filepath.Join(dir, "artifacts", id+".json"), a); err != nil {
			return err
		}
		return appendIndex(index, id)
	})
	return a, err
}

func artifactPath(hash string) string {
	return filepath.ToSlash(filepath.Join("evidence", "v1", "blobs", hash))
}

func (s *Store) LoadArtifact(runID, id string) (Artifact, error) {
	dir, err := s.runDir(runID)
	if err != nil {
		return Artifact{}, err
	}
	if err := validID(id, "art_"); err != nil {
		return Artifact{}, err
	}
	var a Artifact
	if err := readJSON(filepath.Join(dir, "artifacts", id+".json"), &a); err != nil {
		return Artifact{}, err
	}
	digest, err := hex.DecodeString(a.SHA256)
	if a.ID != id || a.RunID != runID || a.Version != Version || a.Sequence == 0 || a.Size < 0 || err != nil || len(digest) != sha256.Size || a.Path != artifactPath(a.SHA256) {
		return Artifact{}, fmt.Errorf("invalid artifact manifest")
	}
	return a, nil
}

func (s *Store) ListArtifacts(runID, after string, limit int) (ArtifactPage, error) {
	page := ArtifactPage{RunID: runID, Artifacts: []Artifact{}}
	limit, err := pageLimit(limit)
	if err != nil {
		return page, err
	}
	if _, err := s.LoadRun(runID); err != nil {
		return page, err
	}
	var start uint64
	if after != "" {
		a, err := s.LoadArtifact(runID, after)
		if err != nil {
			return page, err
		}
		start = a.Sequence
	}
	dir, _ := s.runDir(runID)
	err = s.locked(dir, func() error {
		index := filepath.Join(dir, "artifacts.idx")
		n, err := indexCount(index, "art_")
		if err != nil {
			return err
		}
		page.Total = n
		if err := checkCursor(index, "art_", after, start, n); err != nil {
			return err
		}
		used := 0
		end := start
		for i := start + 1; i <= n && len(page.Artifacts) < limit; i++ {
			id, err := indexID(index, "art_", i)
			if err != nil {
				return err
			}
			a, err := s.LoadArtifact(runID, id)
			if err != nil {
				return err
			}
			data, _ := json.Marshal(a)
			if len(page.Artifacts) > 0 && used+len(data) > MaxPageBytes {
				break
			}
			used += len(data)
			page.Artifacts = append(page.Artifacts, a)
			page.Next = id
			end = i
		}
		page.HasMore = end < n
		return nil
	})
	return page, err
}

// ReadArtifact returns a bounded byte range, not an assertion of hash validity
// or collection completeness. VerifyArtifact checks the full stored byte stream.
func (s *Store) ReadArtifact(runID, id string, offset int64, length int) (ArtifactBytes, error) {
	if offset < 0 || length < 1 || length > MaxReadBytes {
		return ArtifactBytes{}, fmt.Errorf("offset must be nonnegative and length between 1 and %d", MaxReadBytes)
	}
	a, err := s.LoadArtifact(runID, id)
	if err != nil {
		return ArtifactBytes{}, err
	}
	if offset > a.Size {
		return ArtifactBytes{}, fmt.Errorf("offset exceeds artifact size")
	}
	path := filepath.Join(s.dir, "blobs", a.SHA256)
	info, err := os.Lstat(path)
	if err != nil {
		return ArtifactBytes{}, err
	}
	if !info.Mode().IsRegular() || info.Size() != a.Size {
		return ArtifactBytes{}, fmt.Errorf("artifact bytes differ from manifest")
	}
	f, err := os.Open(path)
	if err != nil {
		return ArtifactBytes{}, err
	}
	defer f.Close()
	want := int64(length)
	if remaining := a.Size - offset; want > remaining {
		want = remaining
	}
	data := make([]byte, int(want))
	if _, err := f.ReadAt(data, offset); err != nil && !(err == io.EOF && len(data) == 0) {
		return ArtifactBytes{}, err
	}
	return ArtifactBytes{ArtifactID: id, SHA256: a.SHA256, Offset: offset, NextOffset: offset + want, Size: a.Size, HasMore: offset+want < a.Size, Data: data}, nil
}

func (s *Store) VerifyArtifact(runID, id string) error {
	a, err := s.LoadArtifact(runID, id)
	if err != nil {
		return err
	}
	return verifyBlob(filepath.Join(s.dir, "blobs", a.SHA256), a.SHA256, a.Size)
}

func verifyBlob(path, digest string, size int64) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return fmt.Errorf("artifact integrity mismatch")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.CopyBuffer(hash, f, make([]byte, 128<<10))
	if err != nil {
		return err
	}
	if n != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("artifact integrity mismatch")
	}
	return nil
}
