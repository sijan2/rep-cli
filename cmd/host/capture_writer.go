package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type countedCaptureWriter struct {
	target io.Writer
	bytes  int64
	limit  int64
}

func (w *countedCaptureWriter) Write(data []byte) (int, error) {
	if w.limit > 0 && int64(len(data)) > w.limit-w.bytes {
		return 0, fmt.Errorf("capture exceeds REP_CAPTURE_MAX_SNAPSHOT_BYTES (%d)", w.limit)
	}
	n, err := w.target.Write(data)
	w.bytes += int64(n)
	return n, err
}

// Encode records one at a time so serialization does not allocate a second
// contiguous copy of the entire capture. The caller holds liveMu while writing.
func writeLiveDataFile(path string, data *LiveData, maxBytes int64) (string, int64, error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".rep-capture-*.tmp")
	if err != nil {
		return "", 0, err
	}
	temp := file.Name()
	defer func() { _ = file.Close(); _ = os.Remove(temp) }()
	if err = file.Chmod(0600); err != nil {
		return "", 0, err
	}
	digest := sha256.New()
	counted := &countedCaptureWriter{target: io.MultiWriter(file, digest), limit: maxBytes}
	buffer := bufio.NewWriterSize(counted, 64<<10)
	header, err := json.Marshal(struct {
		Version    string          `json:"version"`
		ExportedAt string          `json:"exported_at"`
		SessionID  string          `json:"session_id,omitempty"`
		Browser    *BrowserSession `json:"browser_session,omitempty"`
	}{data.Version, data.ExportedAt, data.SessionID, data.Browser})
	if err != nil {
		return "", 0, err
	}
	if _, err = buffer.Write(header[:len(header)-1]); err != nil {
		return "", 0, err
	}
	if _, err = buffer.WriteString(",\"requests\":["); err != nil {
		return "", 0, err
	}
	encoder := json.NewEncoder(buffer)
	for index := range data.Requests {
		if index > 0 {
			if err = buffer.WriteByte(','); err != nil {
				return "", 0, err
			}
		}
		if err = encoder.Encode(&data.Requests[index]); err != nil {
			return "", 0, err
		}
	}
	if _, err = buffer.WriteString("]}\n"); err != nil {
		return "", 0, err
	}
	if err = buffer.Flush(); err != nil {
		return "", 0, err
	}
	if err = file.Sync(); err != nil {
		return "", 0, err
	}
	if err = file.Close(); err != nil {
		return "", 0, err
	}
	if err = os.Rename(temp, path); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(digest.Sum(nil)), counted.bytes, nil
}
