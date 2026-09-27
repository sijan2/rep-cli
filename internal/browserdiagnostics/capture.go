package browserdiagnostics

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/repplus/rep-cli/internal/cdp"
	"github.com/repplus/rep-cli/internal/packetcapture"
)

const startupTimeout = 30 * time.Second

func Validate(o Options) error {
	for _, value := range []string{o.Binary, o.URL, o.Output, o.Interface, o.Filter} {
		if strings.ContainsRune(value, 0) || len(value) > 8192 {
			return errors.New("diagnostic options contain an invalid or oversized string")
		}
	}
	if !o.TLSKeys && !o.WebRTCRTP {
		return errors.New("request --tls-keys and/or --webrtc-rtp explicitly")
	}
	if strings.TrimSpace(o.Output) == "" {
		return errors.New("--output must name a new private directory")
	}
	if o.Duration < time.Millisecond || o.Duration > MaxDuration {
		return errors.New("--duration must be between 1ms and 10m")
	}
	if o.MaxLogBytes < 1024 || o.MaxLogBytes > 256<<20 {
		return errors.New("--max-log-bytes must be between 1024 and 268435456")
	}
	if o.MaxKeyLogBytes < 1024 || o.MaxKeyLogBytes > 16<<20 {
		return errors.New("--max-key-log-bytes must be between 1024 and 16777216")
	}
	if o.MaxPacketBytes < 24 || o.MaxPacketBytes > int64(packetcapture.MaxBytes) {
		return errors.New("--max-packet-bytes must be between 24 and 1073741824")
	}
	if (o.Interface == "") != (o.Filter == "") {
		return errors.New("--interface and --filter must be supplied together")
	}
	if o.TLSKeys && o.Interface == "" {
		return errors.New("--tls-keys requires --interface and --filter to collect matching wire packets")
	}
	if o.Interface != "" {
		p := packetcapture.DefaultOptions()
		p.Interface, p.Filter, p.Output, p.Duration, p.MaxBytes = o.Interface, o.Filter, "wire.pcap", o.Duration, uint64(o.MaxPacketBytes)
		if err := packetcapture.Validate(p); err != nil {
			return err
		}
	}
	if o.URL != "about:blank" {
		u, err := url.Parse(o.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
			return errors.New("--url must be an HTTP(S) URL without embedded credentials, or about:blank")
		}
	}
	return nil
}

type packetOutcome struct {
	result packetcapture.Result
	err    error
}

// Run launches an isolated, bounded browser session and finalizes its private
// artifacts synchronously. A key log is never returned as inline output.
func Run(ctx context.Context, options Options) (result Result, runErr error) {
	if err := Validate(options); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	binary, err := discoverBinary(options.Binary)
	if err != nil {
		return result, err
	}
	options.Binary = binary
	output, err := filepath.Abs(options.Output)
	if err != nil {
		return result, err
	}
	if err := os.Mkdir(output, 0700); err != nil {
		return result, fmt.Errorf("create new private diagnostic directory: %w", err)
	}
	options.Output = output
	profile := filepath.Join(output, "profile")
	result = Result{Version: 1, Collector: "chromium-native-diagnostics", Status: "pending", StopReason: "setup", Output: output,
		ManifestPath: filepath.Join(output, "manifest.json"), ReadyPath: filepath.Join(output, "ready.json"), StartedAt: time.Now().UTC(),
		Browser: Browser{Binary: binary, Headless: options.Headless},
		Limits:  Limits{options.Duration.Milliseconds(), options.MaxLogBytes, options.MaxKeyLogBytes, options.MaxPacketBytes, "sampled_25ms_stop_then_truncate_to_complete_lines"},
		KeyLog:  KeyLogStatistics{State: "disabled"}, RTPState: "disabled", Artifacts: []Artifact{},
		Limitations: []string{"explicit_new_private_browser_session", "browser_diagnostics_can_affect_timing_no_undetectability_claim", "capture_interval_does_not_establish_wire_completeness", "tls_key_log_does_not_export_webrtc_dtls_srtp_keys", "rtp_datagrams_preserve_encoded_payloads_at_srtp_boundary_not_original_ip_udp_headers", "outgoing_rtp_logged_before_encryption_does_not_prove_transmission", "incoming_rtp_requires_successful_srtp_authentication", "application_level_encrypted_media_remains_encrypted", "native_rtp_inner_timestamp_may_have_incorrect_hours_and_minutes", "browser_log_does_not_provide_global_packet_loss_counters"}}
	if options.TLSKeys {
		result.KeyLog.State = "not_observed"
		result.Limitations = append(result.Limitations, "key_log_file_may_temporarily_exceed_budget_between_samples_and_during_shutdown")
	}
	if options.WebRTCRTP {
		result.RTPState = "not_observed"
	}
	defer func() {
		result.StoppedAt = time.Now().UTC()
		result.DurationMS = result.StoppedAt.Sub(result.StartedAt).Milliseconds()
		if runErr != nil {
			result.Status = "failed"
			result.Error = boundedError(runErr)
		}
		if result.Status == "pending" {
			result.Status = "failed"
		}
		sort.Slice(result.Artifacts, func(i, j int) bool { return result.Artifacts[i].Kind < result.Artifacts[j].Kind })
		if err := writeJSONExclusive(result.ManifestPath, result); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("write diagnostic manifest: %w", err))
			result.Status, result.Error = "failed", boundedError(runErr)
		}
	}()
	if err := os.Mkdir(profile, 0700); err != nil {
		return result, err
	}
	// This directory was created exclusively for this invocation. It is removed
	// only after the browser has been reaped by the later registered defer.
	browserStopped := true
	defer func() {
		if !browserStopped {
			result.Limitations = append(result.Limitations, "browser_exit_unconfirmed_profile_and_artifacts_unfinalized")
			return
		}
		if err := os.RemoveAll(profile); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("remove private browser profile: %w", err))
		} else {
			result.Browser.ProfileRemoved = true
		}
	}()
	logPath := filepath.Join(output, "browser.log")
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return result, err
	}
	stop := make(chan string, 1)
	workCtx, abortWork := context.WithCancel(ctx)
	defer abortWork()
	signalStop := func(reason string) {
		select {
		case stop <- reason:
		default:
		}
		abortWork()
	}
	logWriter := &boundedWriter{file: logFile, limit: options.MaxLogBytes, stop: stop, notify: signalStop}
	defer func() {
		if !browserStopped {
			return
		}
		result.Log, err = logWriter.finish()
		runErr = errors.Join(runErr, err)
		if result.Log.DroppedBytes > 0 && result.Status == "completed" {
			result.Status, result.StopReason = "partial", "log_byte_limit"
		}
		if err := addArtifact(&result, "browser_log", logPath); err != nil {
			runErr = errors.Join(runErr, err)
		}
		if options.WebRTCRTP {
			if err := extractRTP(&result, options); err != nil {
				runErr = errors.Join(runErr, err)
			}
		}
	}()
	keyPath := filepath.Join(output, "tls.keys")
	if options.TLSKeys {
		key, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return result, err
		}
		if err := key.Close(); err != nil {
			return result, err
		}
		defer func() {
			if !browserStopped {
				return
			}
			stats, err := finalizeKeys(keyPath, options.MaxKeyLogBytes)
			result.KeyLog = stats
			runErr = errors.Join(runErr, err)
			if (stats.LimitExceeded || stats.UnterminatedFinalLine || stats.InvalidLines > 0) && result.Status == "completed" {
				result.Status = "partial"
				result.StopReason = "key_log_partial"
			}
			if err := addArtifact(&result, "tls_key_log", keyPath); err != nil {
				runErr = errors.Join(runErr, err)
			}
		}()
		monitorCtx, stopMonitor := context.WithCancel(context.Background())
		monitorDone := make(chan struct{})
		go func() { defer close(monitorDone); monitorKeys(monitorCtx, keyPath, options.MaxKeyLogBytes, signalStop) }()
		defer func() { stopMonitor(); <-monitorDone }()
	}

	packetCtx, cancelPackets := context.WithCancel(context.Background())
	defer cancelPackets()
	var packetDone chan packetOutcome
	packetReady := make(chan struct{})
	if options.Interface != "" {
		p := packetcapture.DefaultOptions()
		p.Interface, p.Filter, p.Output = options.Interface, options.Filter, filepath.Join(output, "wire.pcap")
		p.Duration, p.MaxBytes = options.Duration+startupTimeout+20*time.Second, uint64(options.MaxPacketBytes)
		p.Ready = func() { close(packetReady) }
		packetDone = make(chan packetOutcome, 1)
		go func() { value, err := packetcapture.Capture(packetCtx, p); packetDone <- packetOutcome{value, err} }()
		defer func() {
			cancelPackets()
			if packetDone != nil {
				observation := <-packetDone
				result.PacketCapture = &observation.result
				runErr = errors.Join(runErr, observation.err)
			}
			if result.PacketCapture != nil && result.PacketCapture.Artifact.Path != "" {
				for kind, path := range map[string]string{"wire_packets": result.PacketCapture.Artifact.Path, "wire_metadata": result.PacketCapture.Artifact.MetadataPath} {
					if err := addArtifact(&result, kind, path); err != nil {
						runErr = errors.Join(runErr, err)
					}
				}
			}
		}()
	} else {
		close(packetReady)
	}

	command := exec.Command(binary, launchArguments(options, profile, keyPath)...)
	command.Env = diagnosticEnvironment(os.Environ())
	command.Stdout, command.Stderr = logWriter, logWriter
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = 2 * time.Second
	if err := command.Start(); err != nil {
		return result, fmt.Errorf("start native diagnostic browser: %w", err)
	}
	browserStopped = false
	result.Browser.PID = command.Process.Pid
	browserDone := make(chan struct{})
	var browserErr error
	go func() { browserErr = command.Wait(); close(browserDone) }()
	var browserSocket string
	defer func() {
		forced, err := stopBrowser(command, browserDone, browserSocket)
		select {
		case <-browserDone:
			browserStopped = !errors.Is(browserErr, exec.ErrWaitDelay)
		default:
		}
		if !browserStopped {
			err = errors.Join(err, errors.New("browser process or inherited diagnostic writers have not confirmed exit"))
		}
		if forced {
			result.Limitations = append(result.Limitations, "browser_required_forced_shutdown")
		}
		runErr = errors.Join(runErr, err)
	}()
	startupCtx, cancelStartup := context.WithTimeout(workCtx, startupTimeout)
	defer cancelStartup()
	port, socket, version, err := waitEndpoint(startupCtx, profile, browserDone)
	if err != nil {
		if interrupted(&result, ctx, stop) {
			return result, nil
		}
		result.StopReason = "browser_startup_failed"
		return result, err
	}
	browserSocket = socket
	result.Browser.Port, result.Browser.Version = port, version
	select {
	case <-packetReady:
	case observation := <-packetDone:
		packetDone = nil
		result.PacketCapture = &observation.result
		result.StopReason = "packet_setup_failed"
		return result, errors.Join(errors.New("native packet capture stopped before browser navigation"), observation.err)
	case <-browserDone:
		result.StopReason = "browser_exit"
		return result, browserFailure(browserErr)
	case reason := <-stop:
		result.Status, result.StopReason = "partial", reason
		return result, nil
	case <-startupCtx.Done():
		if interrupted(&result, ctx, stop) {
			return result, nil
		}
		result.StopReason = "startup_cancelled"
		return result, startupCtx.Err()
	}
	// Readiness can race a packet budget being reached immediately afterwards.
	// Do not navigate if the native collector has already stopped.
	select {
	case observation := <-packetDone:
		packetDone = nil
		result.PacketCapture = &observation.result
		result.Status, result.StopReason = "partial", "packet_"+observation.result.StopReason
		return result, observation.err
	default:
	}
	readyAt := time.Now().UTC()
	result.ReadyAt = &readyAt
	if err := writeJSONExclusive(result.ReadyPath, map[string]any{"version": 1, "pid": command.Process.Pid, "port": port, "ready_at": readyAt}); err != nil {
		return result, err
	}
	defer func() {
		if err := os.Remove(result.ReadyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			runErr = errors.Join(runErr, err)
		}
	}()
	if options.URL != "about:blank" {
		navigationCtx, cancel := context.WithTimeout(workCtx, 10*time.Second)
		err := navigate(navigationCtx, port, options.URL)
		cancel()
		if err != nil {
			if interrupted(&result, ctx, stop) {
				return result, nil
			}
			result.StopReason = "navigation_failed"
			return result, err
		}
	}
	result.Status = "completed"
	timer := time.NewTimer(options.Duration)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			result.Status, result.StopReason = "cancelled", "cancelled"
			return result, nil
		case <-timer.C:
			result.StopReason = "duration"
			return result, nil
		case reason := <-stop:
			result.Status, result.StopReason = "partial", reason
			return result, nil
		case observation := <-packetDone:
			packetDone = nil
			result.PacketCapture = &observation.result
			result.Status, result.StopReason = "partial", "packet_"+observation.result.StopReason
			return result, observation.err
		case <-browserDone:
			result.StopReason = "browser_exit"
			if browserErr != nil {
				return result, browserFailure(browserErr)
			}
			return result, nil
		}
	}
}

func interrupted(result *Result, ctx context.Context, stop <-chan string) bool {
	if ctx.Err() != nil {
		result.Status, result.StopReason = "cancelled", "cancelled"
		return true
	}
	select {
	case reason := <-stop:
		result.Status, result.StopReason = "partial", reason
		return true
	default:
		return false
	}
}

func monitorKeys(ctx context.Context, path string, max int64, stop func(string)) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				stop("key_log_unavailable")
				return
			}
			if info.Size() > max {
				stop("key_log_limit")
				return
			}
		}
	}
}

func launchArguments(o Options, profile, keyPath string) []string {
	args := []string{"--user-data-dir=" + profile, "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0", "--no-first-run", "--no-default-browser-check"}
	if o.Headless {
		args = append(args, "--headless=new")
	}
	if o.TLSKeys {
		args = append(args, "--ssl-key-log-file="+keyPath)
	}
	if o.WebRTCRTP {
		args = append(args, "--enable-logging=stderr", "--log-level=0", "--vmodule=srtp_session=1", "--force-fieldtrials=WebRTC-Debugging-RtpDump/Enabled/")
	}
	return append(args, "about:blank")
}

func diagnosticEnvironment(input []string) []string {
	output := make([]string, 0, len(input))
	for _, value := range input {
		name, _, _ := strings.Cut(value, "=")
		if name != "SSLKEYLOGFILE" && name != "CHROME_LOG_FILE" {
			output = append(output, value)
		}
	}
	return output
}

func discoverBinary(supplied string) (string, error) {
	if supplied == "" {
		supplied = os.Getenv("REP_HEADLESS_BINARY")
	}
	if supplied != "" {
		path, err := exec.LookPath(supplied)
		if err != nil {
			return "", errors.New("browser binary is not an executable file")
		}
		return filepath.Abs(path)
	}
	candidates := []string{"/Applications/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing", "/Applications/Chromium.app/Contents/MacOS/Chromium", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}
	if home, err := os.UserHomeDir(); err == nil {
		for _, pattern := range []string{filepath.Join(home, "Library/Caches/ms-playwright/chromium-*/chrome-mac-*/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"), filepath.Join(home, ".cache/ms-playwright/chromium-*/chrome-linux*/chrome")} {
			matches, _ := filepath.Glob(pattern)
			sort.Sort(sort.Reverse(sort.StringSlice(matches)))
			candidates = append(candidates, matches...)
		}
	}
	candidates = append(candidates, "chromium", "chromium-browser", "google-chrome", "google-chrome-for-testing")
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return filepath.Abs(path)
		}
	}
	return "", errors.New("no Chromium browser found; provide --binary or REP_HEADLESS_BINARY")
}

func waitEndpoint(ctx context.Context, profile string, done <-chan struct{}) (int, string, string, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		port, socket := readEndpoint(profile)
		if port != 0 {
			checkCtx, cancel := context.WithTimeout(ctx, time.Second)
			version, err := cdp.GetVersion(checkCtx, port)
			cancel()
			if err == nil && version.WebSocketDebuggerURL == socket {
				return port, socket, version.Browser, nil
			}
		}
		select {
		case <-ctx.Done():
			return 0, "", "", fmt.Errorf("native browser startup: %w", ctx.Err())
		case <-done:
			return 0, "", "", errors.New("native browser exited before its private debugging endpoint became ready")
		case <-ticker.C:
		}
	}
}

func readEndpoint(profile string) (int, string) {
	path := filepath.Join(profile, "DevToolsActivePort")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return 0, ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "/devtools/browser/") || strings.ContainsAny(lines[1], " ?#\r\t") {
		return 0, ""
	}
	port, err := strconv.Atoi(lines[0])
	if err != nil || port < 1 || port > 65535 {
		return 0, ""
	}
	return port, "ws://127.0.0.1:" + strconv.Itoa(port) + lines[1]
}

func navigate(ctx context.Context, port int, targetURL string) error {
	targets, err := cdp.GetTargets(ctx, port)
	if err != nil {
		return errors.New("read owned browser page target failed")
	}
	for _, target := range targets {
		if target.Type != "page" || target.URL != "about:blank" {
			continue
		}
		u, err := url.Parse(target.WebSocketDebuggerURL)
		if err != nil || u.Host != "127.0.0.1:"+strconv.Itoa(port) {
			return errors.New("owned page endpoint does not match the browser port")
		}
		raw, err := cdp.Call(ctx, target.WebSocketDebuggerURL, "", "Page.navigate", map[string]string{"url": targetURL})
		if err != nil {
			return errors.New("native browser navigation failed")
		}
		var response struct {
			ErrorText string `json:"errorText"`
		}
		if json.Unmarshal(raw, &response) != nil || response.ErrorText != "" {
			return errors.New("native browser reported a navigation error")
		}
		return nil
	}
	return errors.New("native browser has no owned about:blank page to navigate")
}

func stopBrowser(command *exec.Cmd, done <-chan struct{}, socket string) (bool, error) {
	select {
	case <-done:
		return false, nil
	default:
	}
	if socket != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, _ = cdp.Call(ctx, socket, "", "Browser.close", nil)
		cancel()
		select {
		case <-done:
			return false, nil
		case <-time.After(1500 * time.Millisecond):
		}
	}
	// Signal the retained os.Process handle, whose Signal/Wait coordination
	// prevents signaling a recycled PID after the child has been reaped.
	select {
	case <-done:
		return false, nil
	default:
	}
	_ = command.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
		return true, nil
	case <-time.After(1500 * time.Millisecond):
	}
	_ = command.Process.Kill()
	select {
	case <-done:
		return true, nil
	case <-time.After(3 * time.Second):
		return true, errors.New("owned browser did not exit after termination")
	}
}

func browserFailure(err error) error {
	if err == nil {
		return errors.New("native browser exited before capture was ready")
	}
	return fmt.Errorf("native browser process exited: %w", err)
}

type boundedWriter struct {
	mu     sync.Mutex
	file   *os.File
	limit  int64
	stats  LogStatistics
	stop   chan<- string
	notify func(string)
	err    error
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats.ObservedBytes += int64(len(p))
	keep := int64(len(p))
	if remaining := w.limit - w.stats.RetainedBytes; keep > remaining {
		keep = remaining
	}
	if keep > 0 && w.err == nil {
		n, err := w.file.Write(p[:keep])
		w.stats.RetainedBytes += int64(n)
		if err != nil {
			w.err = err
			w.signal("log_write_error")
		}
	}
	w.stats.DroppedBytes = w.stats.ObservedBytes - w.stats.RetainedBytes
	if w.stats.DroppedBytes > 0 {
		w.signal("log_byte_limit")
	}
	// Consume every byte even after the retention budget is reached so the
	// browser cannot deadlock on a full stderr pipe during shutdown.
	return len(p), nil
}

func (w *boundedWriter) signal(reason string) {
	if w.notify != nil {
		w.notify(reason)
		return
	}
	select {
	case w.stop <- reason:
	default:
	}
}

func (w *boundedWriter) finish() (LogStatistics, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	err := errors.Join(w.err, w.file.Sync(), w.file.Close())
	return w.stats, err
}

func finalizeKeys(path string, max int64) (KeyLogStatistics, error) {
	stats := KeyLogStatistics{State: "not_observed"}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return stats, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return stats, errors.New("key log is not a regular file")
	}
	stats.ObservedBytes = info.Size()
	stats.LimitExceeded = info.Size() > max
	data, err := io.ReadAll(io.LimitReader(file, max))
	if err != nil {
		return stats, err
	}
	// A process killed during a write can leave a plausible-looking prefix of
	// a secret. Only newline-terminated records are retained and counted.
	stats.UnterminatedFinalLine = !stats.LimitExceeded && len(data) > 0 && data[len(data)-1] != '\n'
	if stats.LimitExceeded || stats.UnterminatedFinalLine {
		end := bytes.LastIndexByte(data, '\n') + 1
		if err := file.Truncate(int64(end)); err != nil {
			return stats, err
		}
		data = data[:end]
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), 8192)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) == 0 || strings.HasPrefix(parts[0], "#") {
			continue
		}
		// Count valid structure without ever exposing or retaining key values.
		if validKeyLogFields(parts) {
			stats.ValidLines++
		} else {
			stats.InvalidLines++
		}
	}
	if err := scanner.Err(); err != nil {
		return stats, errors.New("key log contains an oversized or unreadable line")
	}
	info, err = file.Stat()
	if err != nil {
		return stats, err
	}
	stats.RetainedBytes = info.Size()
	stats.DiscardedBytes = stats.ObservedBytes - stats.RetainedBytes
	if stats.ValidLines > 0 {
		stats.State = "observed"
	}
	return stats, file.Sync()
}

func validKeyLogFields(parts []string) bool {
	if len(parts) != 3 || len(parts[1]) != 64 {
		return false
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		return false
	}
	if _, err := hex.DecodeString(parts[2]); err != nil {
		return false
	}
	if parts[0] == "CLIENT_RANDOM" {
		return len(parts[2]) == 96
	}
	known := false
	switch parts[0] {
	case "CLIENT_EARLY_TRAFFIC_SECRET", "CLIENT_HANDSHAKE_TRAFFIC_SECRET", "SERVER_HANDSHAKE_TRAFFIC_SECRET", "EXPORTER_SECRET", "EARLY_EXPORTER_SECRET":
		known = true
	default:
		for _, prefix := range []string{"CLIENT_TRAFFIC_SECRET_", "SERVER_TRAFFIC_SECRET_"} {
			if suffix, ok := strings.CutPrefix(parts[0], prefix); ok {
				index, err := strconv.ParseUint(suffix, 10, 32)
				known = err == nil && index < 1000000
			}
		}
	}
	return known && (len(parts[2]) == 64 || len(parts[2]) == 96)
}

func extractRTP(result *Result, options Options) error {
	input, err := os.Open(filepath.Join(options.Output, "browser.log"))
	if err != nil {
		return err
	}
	defer input.Close()
	path := filepath.Join(options.Output, "webrtc-rtp.jsonl")
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	limits := DefaultRTPLimits()
	limits.MaxBytes = options.MaxLogBytes
	limits.MaxInputBytes = options.MaxLogBytes
	parsed, parseErr := ParseRTPLog(context.Background(), input, output, limits)
	closeErr := errors.Join(output.Sync(), output.Close())
	result.RTP = &parsed
	if parsed.Packets > 0 {
		result.RTPState = "observed"
	}
	if (parsed.Truncated || parsed.MalformedRecords > 0 || parsed.OverlongLines > 0) && result.Status == "completed" {
		result.Status = "partial"
		result.StopReason = "rtp_parse_partial"
	}
	return errors.Join(parseErr, closeErr, addArtifact(result, "webrtc_rtp", path))
}

func addArtifact(result *Result, kind, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("diagnostic artifact is not a regular file")
	}
	h := sha256.New()
	n, err := io.Copy(h, file)
	if err != nil {
		return err
	}
	result.Artifacts = append(result.Artifacts, Artifact{Kind: kind, Path: path, Bytes: n, SHA256: hex.EncodeToString(h.Sum(nil))})
	return nil
}

func writeJSONExclusive(path string, value any) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(value); err != nil {
		return err
	}
	return file.Sync()
}

func boundedError(err error) string {
	text := err.Error()
	if len(text) > 2048 {
		text = text[:2048]
	}
	return text
}
