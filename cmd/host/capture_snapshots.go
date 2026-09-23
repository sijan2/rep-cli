package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const (
	captureSnapshotLimit      = 32
	captureSnapshotMaxBytes   = 512 << 20
	captureSnapshotTotalBytes = 1 << 30
	captureSnapshotTTL        = time.Hour
)

var captureSessionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var captureDirectoryPattern = regexp.MustCompile(`^captures-([1-9][0-9]*)-[a-f0-9]{32}$`)

type captureSnapshotDescriptor struct {
	Schema       int       `json:"schema"`
	SessionID    string    `json:"session_id"`
	HostInstance string    `json:"host_instance"`
	Path         string    `json:"path"`
	SHA256       string    `json:"sha256"`
	Bytes        int64     `json:"bytes"`
	Requests     int       `json:"requests"`
	Sealed       bool      `json:"sealed"`
	CreatedAt    time.Time `json:"created_at"`
}

var captureSnapshots = struct {
	sync.Mutex
	dir      string
	host     string
	entries  map[string]captureSnapshotDescriptor
	failures map[string]captureFailure
}{}

type captureFailure struct {
	reason string
	at     time.Time
}

func rememberCaptureFailure(sessionID, reason string) {
	if sessionID == "" {
		return
	}
	captureSnapshots.Lock()
	defer captureSnapshots.Unlock()
	if captureSnapshots.failures == nil {
		captureSnapshots.failures = make(map[string]captureFailure)
	}
	if _, exists := captureSnapshots.failures[sessionID]; !exists {
		captureSnapshots.failures[sessionID] = captureFailure{reason: reason, at: time.Now()}
	}
	for id, failure := range captureSnapshots.failures {
		if time.Since(failure.at) > captureSnapshotTTL {
			delete(captureSnapshots.failures, id)
		}
	}
	if len(captureSnapshots.failures) > 128 {
		var oldestID string
		var oldest time.Time
		for id, failure := range captureSnapshots.failures {
			if oldestID == "" || failure.at.Before(oldest) {
				oldestID, oldest = id, failure.at
			}
		}
		delete(captureSnapshots.failures, oldestID)
	}
}

func initializeCaptureSnapshots() error {
	if err := configureCaptureLimits(); err != nil {
		return err
	}
	base, err := getBridgeDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return err
	}
	pruneAbandonedCaptureDirectories(base)
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	host := hex.EncodeToString(nonce[:])
	dir := filepath.Join(base, fmt.Sprintf("captures-%d-%s", os.Getpid(), host))
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	captureSnapshots.Lock()
	captureSnapshots.dir, captureSnapshots.host = dir, host
	captureSnapshots.entries = make(map[string]captureSnapshotDescriptor)
	captureSnapshots.failures = make(map[string]captureFailure)
	captureSnapshots.Unlock()
	return nil
}

func pruneAbandonedCaptureDirectories(base string) {
	entries, _ := os.ReadDir(base)
	for _, entry := range entries {
		match := captureDirectoryPattern.FindStringSubmatch(entry.Name())
		if len(match) == 0 || !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) <= captureSnapshotTTL {
			continue
		}
		pid, _ := strconv.Atoi(match[1])
		if err := syscall.Kill(pid, 0); err == nil || err == syscall.EPERM {
			continue
		}
		_ = os.RemoveAll(filepath.Join(base, entry.Name()))
	}
}

func closeCaptureSnapshots() {
	captureSnapshots.Lock()
	dir := captureSnapshots.dir
	captureSnapshots.dir = ""
	captureSnapshots.entries = nil
	captureSnapshots.failures = nil
	captureSnapshots.Unlock()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// A capture is frozen before its RPC result is released to the CLI. A later
// capture may replace live.json without replacing this operation's evidence.
func sealCaptureSnapshot() error {
	liveMu.Lock()
	defer liveMu.Unlock()
	if liveData == nil || liveData.Browser == nil || liveData.Browser.FinishedAt == "" || !captureSessionPattern.MatchString(liveData.SessionID) {
		return fmt.Errorf("capture is not sealed or has an invalid session id")
	}
	if liveData.Browser.CaptureError != "" {
		return fmt.Errorf("capture transport is incomplete: %s", liveData.Browser.CaptureError)
	}
	liveData.ExportedAt = time.Now().UTC().Format(time.RFC3339Nano)
	sessionID, count := liveData.SessionID, len(liveData.Requests)
	nameDigest := sha256.Sum256([]byte(sessionID))
	captureSnapshots.Lock()
	defer captureSnapshots.Unlock()
	if captureSnapshots.dir == "" {
		return fmt.Errorf("capture snapshots are unavailable")
	}
	if _, ok := captureSnapshots.entries[sessionID]; ok {
		return fmt.Errorf("capture session is already sealed")
	}
	path := filepath.Join(captureSnapshots.dir, hex.EncodeToString(nameDigest[:])+".json")
	digest, size, err := writeLiveDataFile(path, liveData, activeCaptureLimits.snapshotBytes)
	if err != nil {
		return err
	}
	captureSnapshots.entries[sessionID] = captureSnapshotDescriptor{
		Schema: 1, SessionID: sessionID, HostInstance: captureSnapshots.host,
		Path: path, SHA256: digest, Bytes: size,
		Requests: count, Sealed: true, CreatedAt: time.Now().UTC(),
	}
	pruneCaptureSnapshotsLocked(time.Now())
	return nil
}

func pruneCaptureSnapshotsLocked(now time.Time) {
	entries := make([]captureSnapshotDescriptor, 0, len(captureSnapshots.entries))
	var total int64
	for _, entry := range captureSnapshots.entries {
		entries = append(entries, entry)
		total += entry.Bytes
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CreatedAt.Before(entries[j].CreatedAt) })
	for _, entry := range entries {
		if now.Sub(entry.CreatedAt) <= captureSnapshotTTL && len(captureSnapshots.entries) <= captureSnapshotLimit && total <= activeCaptureLimits.totalSnapshotBytes {
			continue
		}
		delete(captureSnapshots.entries, entry.SessionID)
		total -= entry.Bytes
		_ = os.Remove(entry.Path)
	}
}

func handleCaptureRPC(request RPCRequest) (RPCResponse, bool) {
	response := RPCResponse{ID: request.ID}
	switch request.Method {
	case "bridge.capture.capabilities":
		captureSnapshots.Lock()
		host := captureSnapshots.host
		captureSnapshots.Unlock()
		response.Result, _ = json.Marshal(map[string]interface{}{
			"schema": 1, "immutable_snapshots": true, "max_snapshot_bytes": activeCaptureLimits.snapshotBytes,
			"request_chunks": true, "request_chunk_bytes": requestChunkBytes,
			"max_request_bytes": activeCaptureLimits.requestBytes, "max_requests": activeCaptureLimits.requests,
			"total_snapshot_bytes": activeCaptureLimits.totalSnapshotBytes,
			"host_instance":        host,
			"retention_seconds":    int(captureSnapshotTTL.Seconds()), "max_snapshots": captureSnapshotLimit,
		})
		return response, true
	case "bridge.capture.get":
		var params struct {
			SessionID string `json:"session_id"`
		}
		decoder := json.NewDecoder(bytes.NewReader(request.Params))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&params); err != nil || !captureSessionPattern.MatchString(params.SessionID) {
			response.Error = &RPCError{Code: "invalid_argument", Message: "a valid capture session_id is required"}
			return response, true
		}
		captureSnapshots.Lock()
		pruneCaptureSnapshotsLocked(time.Now())
		entry, ok := captureSnapshots.entries[params.SessionID]
		failure, failed := captureSnapshots.failures[params.SessionID]
		captureSnapshots.Unlock()
		if failed {
			response.Error = &RPCError{Code: "capture_incomplete", Message: "capture was not published: " + failure.reason}
		} else if !ok {
			response.Error = &RPCError{Code: "capture_unavailable", Message: "exact sealed capture is unavailable or expired; global live data was not substituted"}
		} else {
			response.Result, _ = json.Marshal(entry)
		}
		return response, true
	default:
		return response, false
	}
}
