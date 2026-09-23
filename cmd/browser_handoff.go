package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
)

const browserSnapshotMaxBytes = 64 << 20
const scopedBrowserDescriptorLimit = 16

type browserCaptureCaller interface {
	Call(context.Context, string, interface{}, interface{}) error
}

type browserCaptureHandoff struct {
	Scoped           bool
	Immutable        bool
	HostInstance     string
	MaxSnapshotBytes int64
}

type browserSnapshotReference struct {
	Schema       int    `json:"schema"`
	SessionID    string `json:"session_id"`
	HostInstance string `json:"host_instance"`
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
	Bytes        int64  `json:"bytes"`
	Requests     int    `json:"requests"`
	Sealed       bool   `json:"sealed"`
}

// Negotiate before any browser capture. Old hosts are supported only for an
// explicitly global operation, where the historical live-file contract applies.
func prepareBrowserCapture(ctx context.Context, client browserCaptureCaller) (browserCaptureHandoff, error) {
	selected, err := scope.Current()
	if err != nil {
		return browserCaptureHandoff{}, err
	}
	handoff := browserCaptureHandoff{Scoped: selected.Scoped}
	var capabilities struct {
		Schema           int    `json:"schema"`
		Immutable        bool   `json:"immutable_snapshots"`
		HostInstance     string `json:"host_instance"`
		RequestChunks    bool   `json:"request_chunks"`
		MaxSnapshotBytes int64  `json:"max_snapshot_bytes"`
	}
	err = client.Call(ctx, "bridge.capture.capabilities", nil, &capabilities)
	if err == nil && capabilities.Schema == 1 && capabilities.Immutable && capabilities.HostInstance != "" {
		if !capabilities.RequestChunks || capabilities.MaxSnapshotBytes <= 0 {
			return handoff, fmt.Errorf("capture requires the updated native host with verified request chunks; restart the extension after installing rep-host")
		}
		handoff.Immutable, handoff.HostInstance = true, capabilities.HostInstance
		handoff.MaxSnapshotBytes = capabilities.MaxSnapshotBytes
		return handoff, nil
	}
	if selected.Scoped {
		return handoff, fmt.Errorf("task capture requires a native host with immutable capture support; finish active captures and restart the extension before retrying")
	}
	if ctx.Err() != nil {
		return handoff, ctx.Err()
	}
	return handoff, nil
}

func finishBrowserCapture(ctx context.Context, client browserCaptureCaller, result map[string]interface{}, handoff browserCaptureHandoff, note string, save bool) error {
	var export store.Export
	if handoff.Immutable {
		var err error
		export, err = readExactBrowserSnapshot(ctx, client, result, handoff.HostInstance, handoff.MaxSnapshotBytes)
		if err != nil {
			return err
		}
	} else {
		if handoff.Scoped {
			return fmt.Errorf("scoped capture cannot read global live data")
		}
		path, err := store.GetLiveFilePath()
		if err != nil {
			return err
		}
		export, err = loadLiveExport(path)
		if err != nil {
			return err
		}
		if err := validateBrowserCaptureResult(result, export, false); err != nil {
			return err
		}
	}
	if err := enrichBrowserCaptureSnapshot(result, export); err != nil {
		return err
	}
	if handoff.Scoped {
		if err := publishScopedBrowserCapture(export); err != nil {
			return err
		}
	}
	if handoff.Scoped || save {
		// Saving this exact value avoids a second live-file read after another
		// browser operation has started. Empty captures are useful scoped evidence.
		session, err := archiveBrowserCaptureSnapshot(note, export)
		if err != nil {
			return err
		}
		result["saved_session_id"], result["saved_hash_id"] = session.ID, session.HashID
	}
	result["capture_snapshot_verified"] = handoff.Immutable
	if handoff.Scoped {
		selected, err := scope.Current()
		if err != nil {
			return err
		}
		result["workspace"], result["task"] = selected.Workspace, selected.Task
		boundScopedBrowserCaptureResult(result)
	}
	return nil
}

// Persist every request first, then disclose a bounded chronological preview.
// Archive IDs and omission counts make this projection explicitly incomplete.
func boundScopedBrowserCaptureResult(result map[string]interface{}) {
	descriptors, ok := result["captured_requests"].([]browserCapturedRequest)
	if ok {
		total := len(descriptors)
		shown := min(total, scopedBrowserDescriptorLimit)
		result["captured_requests"] = append([]browserCapturedRequest{}, descriptors[:shown]...)
		result["captured_requests_total"] = total
		result["captured_requests_omitted"] = total - shown
		result["captured_requests_complete"] = shown == total
	}
	if outcome, ok := result["terminal_outcome"].(*browserTerminalOutcome); ok && outcome != nil {
		outcome.RequestIDsTotal = len(outcome.RequestIDs)
		if len(outcome.RequestIDs) > scopedBrowserDescriptorLimit {
			outcome.RequestIDsOmitted = len(outcome.RequestIDs) - scopedBrowserDescriptorLimit
			outcome.RequestIDs = append([]string(nil), outcome.RequestIDs[:scopedBrowserDescriptorLimit]...)
		}
		outcome.LaterFormFailureIDsTotal = len(outcome.LaterFormFailureIDs)
		if len(outcome.LaterFormFailureIDs) > scopedBrowserDescriptorLimit {
			outcome.LaterFormFailureIDsOmitted = len(outcome.LaterFormFailureIDs) - scopedBrowserDescriptorLimit
			outcome.LaterFormFailureIDs = append([]string(nil), outcome.LaterFormFailureIDs[:scopedBrowserDescriptorLimit]...)
		}
	}
}

func readExactBrowserSnapshot(ctx context.Context, client browserCaptureCaller, result map[string]interface{}, host string, limits ...int64) (store.Export, error) {
	var export store.Export
	maxBytes := int64(browserSnapshotMaxBytes)
	if len(limits) > 0 && limits[0] > 0 {
		maxBytes = limits[0]
	}
	sessionID, ok := result["session_id"].(string)
	if !ok || strings.TrimSpace(sessionID) == "" {
		return export, fmt.Errorf("browser result has no capture session id")
	}
	var reference browserSnapshotReference
	if err := client.Call(ctx, "bridge.capture.get", map[string]interface{}{"session_id": sessionID}, &reference); err != nil {
		return export, fmt.Errorf("get exact browser capture: %w", err)
	}
	if reference.Schema != 1 || !reference.Sealed || reference.SessionID != sessionID || reference.HostInstance != host || host == "" || reference.Bytes <= 0 || reference.Bytes > maxBytes || reference.Requests < 0 || !filepath.IsAbs(reference.Path) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(reference.SHA256) {
		return export, fmt.Errorf("invalid sealed browser capture reference")
	}
	file, err := os.Open(reference.Path)
	if err != nil {
		return export, fmt.Errorf("exact browser capture is no longer available")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != reference.Bytes {
		return export, fmt.Errorf("browser capture size or file type mismatch")
	}
	digest := sha256.New()
	export, err = store.DecodeExport(io.TeeReader(io.LimitReader(file, reference.Bytes), digest))
	if err != nil {
		return export, fmt.Errorf("invalid browser capture snapshot")
	}
	if hex.EncodeToString(digest.Sum(nil)) != reference.SHA256 {
		return export, fmt.Errorf("browser capture digest mismatch")
	}
	if export.SessionID != sessionID || len(export.Requests) != reference.Requests || export.BrowserSession == nil || export.BrowserSession.FinishedAt == "" {
		return export, fmt.Errorf("browser capture provenance mismatch")
	}
	if err := validateBrowserCaptureResult(result, export, true); err != nil {
		return export, err
	}
	export.CaptureDigest = reference.SHA256
	return export, nil
}

func validateBrowserCaptureResult(result map[string]interface{}, export store.Export, strict bool) error {
	sessionID, _ := result["session_id"].(string)
	if (strict && (sessionID == "" || export.SessionID == "")) || (sessionID != "" && export.SessionID != "" && sessionID != export.SessionID) {
		return fmt.Errorf("browser capture session mismatch")
	}
	count, ok := browserResultRequestCount(result["requests"])
	if (strict && !ok) || (ok && count != len(export.Requests)) {
		return fmt.Errorf("browser capture request count mismatch")
	}
	return nil
}

func publishScopedBrowserCapture(export store.Export) error {
	selected, err := scope.Current()
	if err != nil {
		return err
	}
	if !selected.Scoped {
		return fmt.Errorf("task capture publication requires a task scope")
	}
	path, err := store.GetLiveFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".capture-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if err := store.EncodeExport(file, export); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func archiveBrowserCaptureSnapshot(note string, export store.Export) (*store.Session, error) {
	persistent, err := store.Get()
	if err != nil {
		return nil, err
	}
	id := store.GenerateSessionID(note)
	if export.SessionID != "" {
		id = "capture-" + export.SessionID
	}
	session, err := persistent.AddSession(id, note, export.Requests, &export)
	if err != nil {
		return nil, err
	}
	if err := persistent.Save(); err != nil {
		return nil, err
	}
	return session, nil
}
