package browserdiagnostics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestValidateRequiresExplicitCollectorsAndMatchingWireCapture(t *testing.T) {
	o := DefaultOptions()
	o.Output = filepath.Join(t.TempDir(), "capture")
	if Validate(o) == nil {
		t.Fatal("implicit collection accepted")
	}
	o.TLSKeys = true
	if Validate(o) == nil {
		t.Fatal("keys without matching wire capture accepted")
	}
	o.Interface, o.Filter = "lo0", "udp port 443"
	if err := Validate(o); err != nil {
		t.Fatal(err)
	}
	o.TLSKeys, o.WebRTCRTP, o.Interface, o.Filter = false, true, "", ""
	if err := Validate(o); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"https://user:password@example.test", "file:///private/data", "javascript:void(0)", "http://"} {
		o.URL = raw
		if Validate(o) == nil {
			t.Fatalf("accepted URL %q", raw)
		}
	}
	o.URL = "about:blank"
	o.Duration = MaxDuration + time.Millisecond
	if Validate(o) == nil {
		t.Fatal("duration outside the bound accepted")
	}
}

func TestDiagnosticLaunchLeavesKeysOptInAndBrowserSecurityEnabled(t *testing.T) {
	o := DefaultOptions()
	o.WebRTCRTP = true
	args := launchArguments(o, "/private/profile", "/private/tls.keys")
	joined := strings.Join(args, " ")
	for _, unwanted := range []string{"--ssl-key-log-file", "--proxy", "--ignore-certificate", "--disable-web-security", "--no-sandbox", "--load-extension", "--remote-allow-origins"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("unexpected option %s", unwanted)
		}
	}
	for _, wanted := range []string{"--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0", "--force-fieldtrials=WebRTC-Debugging-RtpDump/Enabled/", "--vmodule=srtp_session=1"} {
		if !strings.Contains(joined, wanted) {
			t.Fatalf("missing %s", wanted)
		}
	}
	if args[len(args)-1] != "about:blank" {
		t.Fatal("navigation before native capture readiness")
	}
	env := diagnosticEnvironment([]string{"PATH=/usr/bin", "SSLKEYLOGFILE=/unrelated/keys", "CHROME_LOG_FILE=/unrelated/log", "TOKEN=kept"})
	if !reflect.DeepEqual(env, []string{"PATH=/usr/bin", "TOKEN=kept"}) {
		t.Fatalf("unexpected environment %v", env)
	}
}

func TestBoundedWriterDrainsConcurrentOutputAndRetainsExactBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan string, 1)
	writer := &boundedWriter{file: file, limit: 1024, stop: stop}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data := []byte(strings.Repeat("x", 777))
			n, err := writer.Write(data)
			if err != nil || n != len(data) {
				t.Errorf("write did not drain: %d %v", n, err)
			}
		}()
	}
	wg.Wait()
	stats, err := writer.finish()
	if err != nil {
		t.Fatal(err)
	}
	if stats.ObservedBytes != 8*777 || stats.RetainedBytes != 1024 || stats.DroppedBytes != 8*777-1024 {
		t.Fatalf("wrong counters %+v", stats)
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) != 1024 {
		t.Fatalf("wrong retained length %d %v", len(data), err)
	}
	select {
	case reason := <-stop:
		if reason != "log_byte_limit" {
			t.Fatal(reason)
		}
	default:
		t.Fatal("budget did not signal stop")
	}
}

func TestFinalizeKeysRejectsIncompleteAndUnrecognizedSecrets(t *testing.T) {
	clientRandom := strings.Repeat("a", 64)
	secret := strings.Repeat("b", 64)
	valid := "CLIENT_TRAFFIC_SECRET_0 " + clientRandom + " " + secret + "\n"
	for _, tc := range []struct {
		name, data        string
		max               int64
		valid, invalid    int64
		incomplete, limit bool
	}{
		{"complete", valid, 1024, 1, 0, false, false},
		{"partial_last_line", valid + strings.TrimSuffix(valid, "\n"), 1024, 1, 0, true, false},
		{"unknown_label", "NOT_A_SECRET " + clientRandom + " " + secret + "\n", 1024, 0, 1, false, false},
		{"wrong_secret_length", "CLIENT_TRAFFIC_SECRET_0 " + clientRandom + " " + strings.Repeat("b", 32) + "\n", 1024, 0, 1, false, false},
		{"byte_cap", valid + valid, int64(len(valid) + 10), 1, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tls.keys")
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			stats, err := finalizeKeys(path, tc.max)
			if err != nil {
				t.Fatal(err)
			}
			if stats.ValidLines != tc.valid || stats.InvalidLines != tc.invalid || stats.UnterminatedFinalLine != tc.incomplete || stats.LimitExceeded != tc.limit {
				t.Fatalf("wrong stats %+v", stats)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if int64(len(data)) > tc.max || (len(data) > 0 && data[len(data)-1] != '\n') {
				t.Fatal("partial or overbudget key material retained")
			}
			if stats.ObservedBytes != int64(len(tc.data)) || stats.RetainedBytes != int64(len(data)) || stats.DiscardedBytes != int64(len(tc.data)-len(data)) {
				t.Fatal("wrong byte accounting")
			}
		})
	}
}

func TestKeyMonitorRunsBeforeBrowserReadiness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tls.keys")
	if err := os.WriteFile(path, make([]byte, 1025), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan string, 1)
	done := make(chan struct{})
	go func() { defer close(done); monitorKeys(ctx, path, 1024, func(reason string) { stopped <- reason }) }()
	select {
	case reason := <-stopped:
		if reason != "key_log_limit" {
			t.Fatal(reason)
		}
	case <-time.After(time.Second):
		t.Fatal("key cap was not observed")
	}
	<-done
}

func TestRunFinalizesFailureAndPreservesExistingDirectories(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-browser")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'synthetic browser startup failure\\n' >&2\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	o := DefaultOptions()
	o.Binary = binary
	o.Output = filepath.Join(dir, "capture")
	o.WebRTCRTP = true
	result, err := Run(context.Background(), o)
	if err == nil || result.Status != "failed" || !result.Browser.ProfileRemoved {
		t.Fatalf("wrong outcome %+v %v", result, err)
	}
	var manifest Result
	data, err := os.ReadFile(result.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Status != "failed" || !manifest.Browser.ProfileRemoved {
		t.Fatalf("failure not persisted %+v", manifest)
	}
	if _, err := os.Stat(filepath.Join(o.Output, "profile")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("profile retained: %v", err)
	}
	if _, err := os.Stat(result.ReadyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed browser published ready: %v", err)
	}
	for _, artifact := range result.Artifacts {
		bytes, err := os.ReadFile(artifact.Path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(bytes)
		if artifact.SHA256 != hex.EncodeToString(sum[:]) || artifact.Bytes != int64(len(bytes)) {
			t.Fatal("incorrect artifact hash")
		}
		info, err := os.Stat(artifact.Path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("artifact permissions %v %v", info, err)
		}
	}
	if _, err := Run(context.Background(), o); err == nil {
		t.Fatal("existing capture directory reused")
	}
}

func TestRunStopsOnLogBudgetBeforeReady(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-browser")
	script := "#!/bin/sh\ni=0\nwhile [ \"$i\" -lt 1000 ]; do printf 'synthetic native diagnostic log line\\n' >&2; i=$((i+1)); done\nexit 0\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	o := DefaultOptions()
	o.Binary = binary
	o.Output = filepath.Join(dir, "capture")
	o.WebRTCRTP = true
	o.MaxLogBytes = 1024
	result, err := Run(context.Background(), o)
	if err != nil || result.Status != "partial" || result.StopReason != "log_byte_limit" || result.Log.RetainedBytes != 1024 || result.Log.DroppedBytes == 0 {
		t.Fatalf("wrong bounded result %+v %v", result, err)
	}
}

func TestRunHonorsCancellationBeforeReadiness(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-browser")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	o := DefaultOptions()
	o.Binary = binary
	o.Output = filepath.Join(dir, "capture")
	o.WebRTCRTP = true
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result, err := Run(ctx, o)
	if err != nil || result.Status != "cancelled" || result.StopReason != "cancelled" || !result.Browser.ProfileRemoved {
		t.Fatalf("wrong cancellation %+v %v", result, err)
	}
}
