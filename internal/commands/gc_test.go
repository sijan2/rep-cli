package commands

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"

	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
)

func gcTestScope(t *testing.T) scope.Scope {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	scope.Reset()
	t.Cleanup(scope.Reset)
	selected, err := scope.Configure(scope.Options{Workspace: "gc-tests", Task: "owner", WorkspaceSet: true, TaskSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(selected.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	return selected
}

func gcTestCapture(t *testing.T, dir string) []byte {
	t.Helper()
	raw, err := sonic.Marshal(map[string]any{"action": "session", "session": store.Session{ID: "archive", Requests: []store.Request{{ID: "h_gc12345678", Method: "GET", URL: "https://target.test/app.js", Timestamp: time.Now().Add(-48 * time.Hour).UnixMilli(), Response: &store.Response{Status: 200, Body: strings.Repeat("gc-body", 1000)}}}}})
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(filepath.Join(dir, "sessions.jsonl"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return raw
}

type gcFailWriter struct{}

func (gcFailWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }
