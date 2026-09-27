package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/browserdiagnostics"
	"github.com/repplus/rep-cli/internal/output"
)

func runNativeBrowserCommand(t *testing.T, run func(context.Context, browserdiagnostics.Options) (browserdiagnostics.Result, error), args ...string) (nativeBrowserCaptureOutput, string, error) {
	t.Helper()
	command := newNativeBrowserCaptureCommandWith(run)
	command.SilenceUsage, command.SilenceErrors = true, true
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs(args)
	err := command.Execute()
	var result nativeBrowserCaptureOutput
	if len(out.Bytes()) > 0 {
		if parseErr := json.Unmarshal(out.Bytes(), &result); parseErr != nil {
			t.Fatalf("native command emitted invalid JSON: %v", parseErr)
		}
	}
	return result, out.String(), err
}

func TestNativeBrowserCaptureRejectsMissingOptInAndUnboundedSetup(t *testing.T) {
	packetTestScope(t)
	called := false
	run := func(context.Context, browserdiagnostics.Options) (browserdiagnostics.Result, error) {
		called = true
		return browserdiagnostics.Result{}, nil
	}
	base := []string{"http://127.0.0.1:8080", "--output", filepath.Join(t.TempDir(), "capture")}
	for _, flags := range [][]string{nil, {"--tls-keys"}, {"--webrtc-rtp", "--duration", "0s"}, {"--webrtc-rtp", "--duration", "11m"},
		{"--webrtc-rtp", "--max-log-bytes", "0"}, {"--webrtc-rtp", "--interface", "lo0"}} {
		args := append(append([]string{}, base...), flags...)
		if _, _, err := runNativeBrowserCommand(t, run, args...); err == nil || called {
			t.Fatalf("unsafe setup reached runner: %v called=%v err=%v", flags, called, err)
		}
	}
}

func TestNativeBrowserCaptureForwardsBoundsAndReportsFailureOnce(t *testing.T) {
	packetTestScope(t)
	run := func(ctx context.Context, options browserdiagnostics.Options) (browserdiagnostics.Result, error) {
		if ctx.Done() == nil || !options.Headless || !options.WebRTCRTP || !options.TLSKeys || options.Interface != "lo0" || options.Filter != "udp port 8443" || options.Duration != 5*time.Second || options.MaxLogBytes != 2<<20 {
			t.Fatalf("native capture options lost: %+v", options)
		}
		return browserdiagnostics.Result{Version: 1, Status: "failed", Error: "fixture setup failed"}, errors.New("fixture setup failed")
	}
	result, text, err := runNativeBrowserCommand(t, run, "http://127.0.0.1:8080", "--output", filepath.Join(t.TempDir(), "capture"),
		"--webrtc-rtp", "--tls-keys", "--interface", "lo0", "--filter", "udp port 8443", "--duration", "5s", "--headless", "--max-log-bytes", "2097152")
	if err == nil || !output.Reported(err) || result.CommandError == nil || result.Status != "failed" || strings.Count(text, "\n") != 1 {
		t.Fatalf("failure lost or duplicated: %s %v", text, err)
	}
}

func TestNativeBrowserEvidenceImportsManifestWithoutCopyingSessionSecrets(t *testing.T) {
	store, evidenceRun := operationEvidenceFixture(t)
	dir := filepath.Join(t.TempDir(), "capture")
	run := func(_ context.Context, options browserdiagnostics.Options) (browserdiagnostics.Result, error) {
		if err := os.Mkdir(options.Output, 0700); err != nil {
			t.Fatal(err)
		}
		keyPath := filepath.Join(options.Output, "tls.keys")
		if err := os.WriteFile(keyPath, []byte("PRIVATE_TEST_SECRET"), 0600); err != nil {
			t.Fatal(err)
		}
		result := browserdiagnostics.Result{Version: 1, Status: "completed", Output: options.Output,
			Artifacts: []browserdiagnostics.Artifact{{Kind: "tls_key_log", Path: keyPath, Bytes: 19, SHA256: strings.Repeat("a", 64)}}}
		manifest, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(options.Output, "manifest.json"), manifest, 0600); err != nil {
			t.Fatal(err)
		}
		return result, nil
	}
	result, text, err := runNativeBrowserCommand(t, run, "http://127.0.0.1:8080", "--webrtc-rtp", "--output", dir)
	if err != nil || result.Evidence == nil || result.Evidence.RunID != evidenceRun.ID || strings.Contains(text, "PRIVATE_TEST_SECRET") {
		t.Fatalf("native evidence result incorrect: %s %v", text, err)
	}
	page, err := store.ListOperations(evidenceRun.ID, "", 20)
	if err != nil || len(page.Operations) != 2 {
		t.Fatalf("operation lifecycle missing: %+v %v", page, err)
	}
	finished := page.Operations[1]
	if finished.Kind != "browser.native_capture" || finished.Status != "completed" || len(finished.ArtifactIDs) != 1 || len(finished.References) != 1 || finished.References[0].Metadata["imported"] != false {
		t.Fatalf("manifest/reference policy lost: %+v", finished)
	}
}
