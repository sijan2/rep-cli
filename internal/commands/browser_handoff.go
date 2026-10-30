package commands

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

type browserCaptureCaller interface {
	Call(context.Context, string, interface{}, interface{}) error
}

type browserCaptureHandoff struct {
	Scoped           bool
	Immutable        bool
	HostInstance     string
	MaxSnapshotBytes int64
	MaxRequests      int
	MaxRequestBytes  int64
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
		MaxRequests      int    `json:"max_requests"`
		MaxRequestBytes  int64  `json:"max_request_bytes"`
	}
	err = client.Call(ctx, "bridge.capture.capabilities", nil, &capabilities)
	if err == nil && capabilities.Schema == 1 && capabilities.Immutable && capabilities.HostInstance != "" {
		if !capabilities.RequestChunks || capabilities.MaxSnapshotBytes <= 0 {
			return handoff, fmt.Errorf("capture requires the updated native host with verified request chunks; restart the extension after installing rep-host")
		}
		handoff.Immutable, handoff.HostInstance = true, capabilities.HostInstance
		handoff.MaxSnapshotBytes = capabilities.MaxSnapshotBytes
		handoff.MaxRequests, handoff.MaxRequestBytes = capabilities.MaxRequests, capabilities.MaxRequestBytes
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
