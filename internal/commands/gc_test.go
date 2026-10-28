package commands

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/retention"
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

func TestGCCommandModes(t *testing.T) {
	for _, tt := range []struct {
		name           string
		args           []string
		apply, refused bool
	}{
		{"default plan", nil, false, false},
		{"plan", []string{"plan"}, false, false},
		{"status", []string{"status"}, false, false},
		{"refuse apply", []string{"apply"}, false, true},
		{"apply", []string{"apply", "--yes"}, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selected := gcTestScope(t)
			original := gcTestCapture(t, selected.DataDir)
			previousMode, previousJSON := outputMode, jsonOutput
			outputMode, jsonOutput = "json", true
			t.Cleanup(func() { outputMode, jsonOutput = previousMode, previousJSON })
			var out bytes.Buffer
			command := gcNewCommand()
			command.SetOut(&out)
			command.SetArgs(tt.args)
			err := command.Execute()
			if (err != nil) != tt.refused {
				t.Fatalf("unexpected error %v: %s", err, out.String())
			}
			lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'})
			wantLines := 1
			if tt.apply {
				wantLines = 2
			}
			if len(lines) != wantLines {
				t.Fatalf("invalid JSON result framing: %s", out.String())
			}
			for _, line := range lines {
				var envelope struct {
					Command string `json:"command"`
					Data    struct {
						Phase  string                    `json:"phase"`
						Report retention.RetentionReport `json:"report"`
						Error  *struct {
							Code string `json:"code"`
						} `json:"error"`
					} `json:"data"`
				}
				if err := sonic.Unmarshal(line, &envelope); err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(envelope.Command, "gc ") || envelope.Data.Report.Task != "owner" || envelope.Data.Report.Workspace != "gc-tests" {
					t.Fatal("missing scoped envelope")
				}
				if tt.refused && (envelope.Data.Error == nil || envelope.Data.Error.Code != "confirmation_required") {
					t.Fatal("refusal is not machine readable")
				}
				for _, skip := range envelope.Data.Report.Skipped {
					if strings.Contains(skip.Reason, "writer_check_unavailable") {
						t.Skip("lsof unavailable")
					}
				}
			}
			current, _ := os.ReadFile(filepath.Join(selected.DataDir, "sessions.jsonl"))
			if tt.apply == bytes.Equal(original, current) {
				t.Fatalf("wrong mutation mode: %s", out.String())
			}
		})
	}
}

func TestGCCommandIsolationAndScope(t *testing.T) {
	for _, kind := range []string{"sibling task", "global", "default task"} {
		t.Run(kind, func(t *testing.T) {
			selected := gcTestScope(t)
			original := gcTestCapture(t, selected.DataDir)
			var siblingPath string
			var sibling []byte
			if kind == "sibling task" {
				siblingDir := filepath.Join(filepath.Dir(selected.DataDir), "other")
				if err := os.MkdirAll(siblingDir, 0700); err != nil {
					t.Fatal(err)
				}
				sibling = gcTestCapture(t, siblingDir)
				siblingPath = filepath.Join(siblingDir, "sessions.jsonl")
			} else {
				opts := scope.Options{Global: true}
				if kind == "default task" {
					opts = scope.Options{Workspace: "gc-tests", WorkspaceSet: true}
				}
				if _, err := scope.Configure(opts); err != nil {
					t.Fatal(err)
				}
			}
			command := gcNewCommand()
			var out bytes.Buffer
			command.SetOut(&out)
			command.SetArgs([]string{"apply", "--yes"})
			err := command.Execute()
			if kind != "sibling task" {
				if err == nil || !strings.Contains(out.String(), "scope_required") {
					t.Fatalf("unscoped GC allowed: %v %s", err, out.String())
				}
				current, _ := os.ReadFile(filepath.Join(selected.DataDir, "sessions.jsonl"))
				if !bytes.Equal(original, current) {
					t.Fatal("scope refusal modified data")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				current, _ := os.ReadFile(siblingPath)
				if !bytes.Equal(sibling, current) {
					t.Fatal("another task was touched")
				}
			}
		})
	}
}

type gcFailWriter struct{}

func (gcFailWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestGCFailedPlanOutputPreventsMutation(t *testing.T) {
	selected := gcTestScope(t)
	original := gcTestCapture(t, selected.DataDir)
	command := gcNewCommand()
	command.SetOut(gcFailWriter{})
	command.SetArgs([]string{"apply", "--yes"})
	if err := command.Execute(); err == nil {
		t.Fatal("failed plan output was ignored")
	}
	current, _ := os.ReadFile(filepath.Join(selected.DataDir, "sessions.jsonl"))
	if !bytes.Equal(original, current) {
		t.Fatal("mutated before displaying the plan")
	}
}
