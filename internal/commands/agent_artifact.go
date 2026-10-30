package commands

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/scope"
)

func coreArtifact(path, suffix string, data []byte) (map[string]any, error) {
	if path == "" {
		selected, err := scope.Current()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(selected.DataDir, "artifacts", agentstate.Token("a_")+suffix)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	path = absolute
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, coreErrorf("artifact_exists_or_unwritable", "artifact destination is unavailable")
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return map[string]any{"path": path, "bytes": len(data), "sha256": fmt.Sprintf("%x", sha256.Sum256(data))}, nil
}
func coreViewport(value string) (int, int, error) {
	switch value {
	case "desktop":
		return 1440, 900, nil
	case "mobile":
		return 390, 844, nil
	}
	parts := strings.Split(strings.ToLower(value), "x")
	if len(parts) == 2 {
		w, e := strconv.Atoi(parts[0])
		h, f := strconv.Atoi(parts[1])
		if e == nil && f == nil && w >= 240 && w <= 7680 && h >= 240 && h <= 7680 {
			return w, h, nil
		}
	}
	return 0, 0, coreArgument("viewport must be desktop, mobile, or WIDTHxHEIGHT (240–7680)")
}
func applyCoreViewport(ctx context.Context, store *agentstate.Store, tab *agentstate.Tab, lease string, session *browserfast.Session, value string) error {
	if value == "" {
		value = tab.Viewport
	}
	if value == "" {
		return nil
	}
	w, h, err := coreViewport(value)
	if err != nil {
		return err
	}
	var ignored any
	err = session.Inspect(ctx, "", "Emulation.setDeviceMetricsOverride", map[string]any{"width": w, "height": h, "deviceScaleFactor": 1, "mobile": value == "mobile"}, &ignored)
	if err != nil {
		return err
	}
	tab.Viewport = value
	return store.Change(tab.Handle, lease, func(t *agentstate.Tab) error { t.Viewport = value; return nil })
}
