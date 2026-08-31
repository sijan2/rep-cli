package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/store"
)

func TestBrowserHeaderValueIsCaseInsensitive(t *testing.T) {
	headers := map[string]interface{}{
		"Content-Type": "application/zip",
		"x-list":       []interface{}{"first", "second"},
	}
	if got := browserHeaderValue(headers, "content-type"); got != "application/zip" {
		t.Fatalf("content type = %q", got)
	}
	if got := browserHeaderValue(headers, "X-LIST"); got != "first" {
		t.Fatalf("list header = %q", got)
	}
	if got := browserHeaderValue(headers, "missing"); got != "" {
		t.Fatalf("missing header = %q", got)
	}
}

func TestPrepareBrowserDownloadDestinationRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "artifact.ipa")
	if err := os.WriteFile(destination, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareBrowserDownloadDestination(destination, false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected no-overwrite error, got %v", err)
	}
	absPath, parent, err := prepareBrowserDownloadDestination(destination, true)
	if err != nil {
		t.Fatal(err)
	}
	if absPath != destination || parent != dir {
		t.Fatalf("unexpected destination: %q %q", absPath, parent)
	}
}

func TestBrowserDownloadRejectsInvalidRequestBeforeBridge(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "artifact.ipa")
	_, err := downloadCapturedRequestInBrowser(context.Background(), &store.Request{
		Method: "POST", URL: "https://example.test/file.ipa",
	}, destination, browserDownloadOptions{Timeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "is not a GET") {
		t.Fatalf("expected method error, got %v", err)
	}
	_, err = downloadCapturedRequestInBrowser(context.Background(), &store.Request{
		Method: "GET", URL: "file:///private/tmp/file.ipa",
	}, destination, browserDownloadOptions{Timeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "absolute HTTP(S)") {
		t.Fatalf("expected URL error, got %v", err)
	}
}
