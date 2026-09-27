// Package headless owns one full Chromium instance per task. It loads Rep's
// existing extension and native bridge in a private profile; user profiles and
// the global browser bridge directory are never reused.
package headless

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/cdp"
)

type Options struct {
	Binary    string
	Extension string
	Host      string
	Headed    bool
	// WindowSize is WIDTHxHEIGHT for the browser window; empty keeps the saved
	// size or DefaultWindowSize.
	WindowSize string
}

// Chromium's default headless window yields a ~756x413 viewport, below common
// 768px breakpoints, so responsive sites served agents their mobile layout.
// 1280x900 gives a 1280x757 desktop viewport in new headless mode.
const DefaultWindowSize = "1280x900"

type State struct {
	Version      int    `json:"version"`
	PID          int    `json:"pid"`
	ProcessStamp string `json:"process_stamp,omitempty"`
	// ProcessStart is the kernel start time in microseconds where the platform
	// exposes it. It lets ownership checks skip ps; older states omit it.
	ProcessStart int64  `json:"process_start_us,omitempty"`
	Binary       string `json:"binary"`
	Extension    string `json:"extension"`
	ExtensionID  string `json:"extension_id"`
	Host         string `json:"host"`
	Profile      string `json:"profile"`
	BridgeDir    string `json:"bridge_dir"`
	Port         int    `json:"port"`
	WebSocket    string `json:"websocket,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	Headed       bool   `json:"headed"`
	// ExtensionDigest identifies the extension source whose worker this
	// profile registered at its last start.
	ExtensionDigest string `json:"extension_digest,omitempty"`
	WindowSize      string `json:"window_size,omitempty"`
}

type Status struct {
	State
	Running         bool   `json:"running"`
	Connected       bool   `json:"connected"`
	IsolatedProfile bool   `json:"isolated_profile"`
	Reason          string `json:"reason,omitempty"`
	// ExtensionStale reports a running browser whose extension source changed
	// after it started; restart it to load the current code.
	ExtensionStale bool `json:"extension_stale,omitempty"`
}

func paths(dataDir string) (root, profile, bridgeDir string) {
	root = filepath.Join(dataDir, "headless")
	profile = filepath.Join(root, "profile")
	sum := sha256.Sum256([]byte(filepath.Clean(dataDir)))
	// macOS Unix sockets have a short path limit. Keep transport paths short
	// even when the workspace/task data directory has a long name.
	bridgeDir = filepath.Join("/tmp", fmt.Sprintf("rep-headless-%d-%x", os.Getuid(), sum[:10]))
	return
}

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("headless directory must be a regular directory")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("headless directory belongs to another user")
	}
	return os.Chmod(path, 0700)
}

func lock(dataDir string) (*os.File, error) {
	if !filepath.IsAbs(dataDir) {
		return nil, errors.New("headless task data directory must be absolute")
	}
	root, _, _ := paths(dataDir)
	if err := privateDir(root); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(root, "lifecycle.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("another headless lifecycle operation is active for this task")
	}
	return file, nil
}

func unlock(file *os.File) { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }

func readState(dataDir string) (State, error) {
	root, profile, bridgeDir := paths(dataDir)
	data, err := os.ReadFile(filepath.Join(root, "state.json"))
	if err != nil {
		return State{}, err
	}
	if len(data) > 16384 {
		return State{}, errors.New("headless state exceeds its size limit")
	}
	var state State
	if json.Unmarshal(data, &state) != nil || state.Version != 1 || state.Profile != profile || state.BridgeDir != bridgeDir || state.PID < 0 {
		return State{}, errors.New("headless state does not match this task")
	}
	return state, nil
}

func writeState(dataDir string, state State) error {
	root, _, _ := paths(dataDir)
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(root, ".state-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(root, "state.json"))
}

func processStamp(pid int) string {
	if pid <= 0 {
		return ""
	}
	output, err := exec.Command("/bin/ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func ownedProcess(state State) bool {
	if state.PID <= 0 || state.ProcessStamp == "" {
		return false
	}
	if state.ProcessStart != 0 && processStartMicros(state.PID) == state.ProcessStart {
		if command, ok := processCommand(state.PID); ok {
			return ownedCommand(state, command)
		}
	} else if processStamp(state.PID) != state.ProcessStamp {
		return false
	}
	output, err := exec.Command("/bin/ps", "-p", strconv.Itoa(state.PID), "-o", "command=").Output()
	if err != nil {
		return false
	}
	return ownedCommand(state, strings.TrimSpace(string(output)))
}

func ownedCommand(state State, command string) bool {
	marker := "--user-data-dir=" + state.Profile
	index := strings.Index(command, marker)
	end := index + len(marker)
	if index < 0 || (index > 0 && command[index-1] != ' ') || (end != len(command) && command[end] != ' ') {
		return false
	}
	headless := false
	for _, argument := range strings.Fields(command) {
		if argument == "--headless" || strings.HasPrefix(argument, "--headless=") {
			if state.Headed || argument != "--headless=new" {
				return false
			}
			headless = true
		}
	}
	return state.Headed || headless
}

func verifyEndpoint(ctx context.Context, state State) error {
	if state.Port <= 0 || state.Port > 65535 || state.WebSocket == "" {
		return errors.New("headless debugging endpoint is not ready")
	}
	version, err := cdp.GetVersion(ctx, state.Port)
	if err != nil || version.WebSocketDebuggerURL != state.WebSocket {
		return errors.New("headless endpoint no longer matches this task's browser")
	}
	return nil
}

func GetStatus(ctx context.Context, dataDir string) (Status, error) {
	state, err := readState(dataDir)
	if os.IsNotExist(err) {
		return Status{IsolatedProfile: true, Reason: "not_started"}, nil
	}
	if err != nil {
		return Status{}, err
	}
	status := Status{State: state, IsolatedProfile: true}
	if !ownedProcess(state) {
		status.Reason = "stopped"
		return status, nil
	}
	status.Running = true
	if digest, err := extensionDigest(state.Extension); err == nil && state.ExtensionDigest != "" && digest != state.ExtensionDigest {
		status.ExtensionStale = true
	}
	if err := verifyEndpoint(ctx, state); err != nil {
		status.Reason = err.Error()
		return status, nil
	}
	_, err = connection(ctx, state)
	status.Connected = err == nil
	if err != nil {
		status.Reason = err.Error()
	}
	return status, nil
}

// Connection never discovers a global bridge or another task's browser.
func Connection(ctx context.Context, dataDir string) (*bridge.Client, error) {
	state, err := readState(dataDir)
	if err != nil {
		return nil, errors.New("this task has no headless browser; run browser headless start")
	}
	if !ownedProcess(state) {
		return nil, errors.New("this task's headless browser is stopped")
	}
	if err := verifyEndpoint(ctx, state); err != nil {
		return nil, err
	}
	return connection(ctx, state)
}

func connection(ctx context.Context, state State) (*bridge.Client, error) {
	entries, _ := filepath.Glob(filepath.Join(state.BridgeDir, "bridge-*.json"))
	for _, path := range entries {
		data, err := os.ReadFile(path)
		var registry bridge.Registry
		if err != nil || len(data) > 16384 || json.Unmarshal(data, &registry) != nil {
			continue
		}
		if registry.ParentPID != state.PID || registry.ExtensionID != state.ExtensionID || filepath.Dir(registry.Socket) != state.BridgeDir {
			continue
		}
		client := &bridge.Client{Registry: registry}
		var status struct {
			Connected bool `json:"connected"`
		}
		if err := client.Call(ctx, "bridge.ping", nil, &status); err == nil && status.Connected {
			return client, nil
		}
	}
	return nil, errors.New("this task's headless Rep bridge is not connected")
}

func Start(ctx context.Context, dataDir string, options Options) (Status, error) {
	guard, err := lock(dataDir)
	if err != nil {
		return Status{}, err
	}
	defer unlock(guard)
	prior, priorErr := readState(dataDir)
	if priorErr == nil && ownedProcess(prior) {
		if err := validateRunningOptions(prior, options); err != nil {
			return Status{}, err
		}
		return GetStatus(ctx, dataDir)
	}
	if priorErr != nil && !os.IsNotExist(priorErr) {
		return Status{}, priorErr
	}
	if options.Binary == "" {
		options.Binary = prior.Binary
	}
	if options.Extension == "" {
		options.Extension = prior.Extension
	}
	if options.Host == "" {
		options.Host = prior.Host
	}
	if options.WindowSize == "" {
		options.WindowSize = prior.WindowSize
	}
	options, extensionID, err := resolveOptions(options)
	if err != nil {
		return Status{}, err
	}
	root, profile, bridgeDir := paths(dataDir)
	for _, dir := range []string{profile, bridgeDir, filepath.Join(profile, "NativeMessagingHosts")} {
		if err := privateDir(dir); err != nil {
			return Status{}, err
		}
	}
	manifest, _ := json.Marshal(map[string]any{
		"name": "com.repplus.host", "description": "Rep task-owned headless bridge", "path": options.Host,
		"type": "stdio", "allowed_origins": []string{"chrome-extension://" + extensionID + "/"},
	})
	if err := os.WriteFile(filepath.Join(profile, "NativeMessagingHosts", "com.repplus.host.json"), manifest, 0600); err != nil {
		return Status{}, err
	}
	_ = os.Remove(filepath.Join(profile, "DevToolsActivePort"))
	if err := discardSessionRestore(profile); err != nil {
		return Status{}, err
	}
	digest, err := extensionDigest(options.Extension)
	if err != nil {
		return Status{}, fmt.Errorf("read Rep extension source: %w", err)
	}
	if digest != prior.ExtensionDigest {
		if err := discardWorkerRegistrations(profile); err != nil {
			return Status{}, err
		}
	}
	logFile, err := os.OpenFile(filepath.Join(root, "browser.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return Status{}, err
	}
	defer logFile.Close()
	command := exec.Command(options.Binary, launchArguments(profile, options.Extension, options.Headed, options.WindowSize)...)
	command.Env = browserEnvironment(bridgeDir, filepath.Join(root, "staging.json"))
	command.Stdin = nil
	command.Stdout, command.Stderr = logFile, logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return Status{}, fmt.Errorf("start headless browser: %w", err)
	}
	state := State{Version: 1, PID: command.Process.Pid, ProcessStamp: processStamp(command.Process.Pid), ProcessStart: processStartMicros(command.Process.Pid), Binary: options.Binary,
		Extension: options.Extension, ExtensionID: extensionID, Host: options.Host, Profile: profile, BridgeDir: bridgeDir,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Headed: options.Headed, ExtensionDigest: digest, WindowSize: options.WindowSize}
	complete := false
	defer func() {
		if !complete {
			_ = command.Process.Kill()
			_ = command.Wait()
			state.PID, state.Port, state.WebSocket, state.ProcessStamp, state.ProcessStart = 0, 0, "", "", 0
			_ = writeState(dataDir, state)
		}
	}()
	if err := writeState(dataDir, state); err != nil {
		return Status{}, err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !ownedProcess(state) {
			return Status{}, errors.New("headless browser exited before connecting; inspect this task's headless/browser.log")
		}
		if state.Port == 0 {
			data, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
			if err == nil {
				lines := strings.Split(strings.TrimSpace(string(data)), "\n")
				if len(lines) == 2 && strings.HasPrefix(lines[1], "/devtools/browser/") {
					port, _ := strconv.Atoi(lines[0])
					if port > 0 && port <= 65535 {
						state.Port, state.WebSocket = port, "ws://127.0.0.1:"+strconv.Itoa(port)+lines[1]
						if err := writeState(dataDir, state); err != nil {
							return Status{}, err
						}
					}
				}
			}
		}
		if state.Port != 0 && verifyEndpoint(ctx, state) == nil {
			if _, err := connection(ctx, state); err == nil {
				complete = true
				_ = command.Process.Release()
				return Status{State: state, Running: true, Connected: true, IsolatedProfile: true}, nil
			}
		}
		select {
		case <-ctx.Done():
			return Status{}, errors.New("headless browser did not connect before the startup deadline; inspect this task's headless/browser.log")
		case <-ticker.C:
		}
	}
}

func Stop(ctx context.Context, dataDir string) (Status, error) {
	guard, err := lock(dataDir)
	if err != nil {
		return Status{}, err
	}
	defer unlock(guard)
	state, err := readState(dataDir)
	if os.IsNotExist(err) {
		return Status{IsolatedProfile: true, Reason: "not_started"}, nil
	}
	if err != nil {
		return Status{}, err
	}
	if ownedProcess(state) {
		if verifyEndpoint(ctx, state) == nil {
			_, _ = cdp.Call(ctx, state.WebSocket, "", "Browser.close", nil)
		}
		deadline := time.Now().Add(2 * time.Second)
		for ownedProcess(state) && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return Status{}, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		if ownedProcess(state) {
			if err := syscall.Kill(state.PID, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
				return Status{}, err
			}
			deadline = time.Now().Add(3 * time.Second)
			for ownedProcess(state) && time.Now().Before(deadline) {
				select {
				case <-ctx.Done():
					return Status{}, ctx.Err()
				case <-time.After(100 * time.Millisecond):
				}
			}
			if ownedProcess(state) {
				return Status{}, errors.New("task browser has not exited; retry stop")
			}
		}
	}
	state.PID, state.Port, state.WebSocket, state.ProcessStamp, state.ProcessStart = 0, 0, "", "", 0
	if err := writeState(dataDir, state); err != nil {
		return Status{}, err
	}
	return Status{State: state, IsolatedProfile: true, Reason: "stopped"}, nil
}

// Chromium restores every previous tab when a profile starts, even after a
// clean Browser.close. Restored tabs reload their pages with the task's cookies,
// sending requests nobody asked for and accumulating across restarts. Tab IDs
// never survive a restart, so only window/tab session state is discarded;
// cookies, storage, and logins remain in the profile.
func discardSessionRestore(profile string) error {
	base := filepath.Join(profile, "Default")
	for _, name := range []string{"Sessions", "Sessions_Encrypted", "Current Session", "Current Tabs", "Last Session", "Last Tabs"} {
		if err := os.RemoveAll(filepath.Join(base, name)); err != nil {
			return fmt.Errorf("discard previous browser session: %w", err)
		}
	}
	return nil
}

// Chromium keeps a registered MV3 service-worker script until the extension's
// manifest version changes, so a restarted profile would silently keep running
// stale extension code after a source update (runtime.reload() does not help:
// a command-line extension's worker does not restart in headless mode).
// Removing the worker database makes Chromium register the current script.
// Cookies, storage, and logins are elsewhere; site worker caches in this task
// profile are rebuilt on demand. This runs only when the source changed.
func discardWorkerRegistrations(profile string) error {
	if err := os.RemoveAll(filepath.Join(profile, "Default", "Service Worker")); err != nil {
		return fmt.Errorf("refresh extension service worker: %w", err)
	}
	return nil
}

const maxDigestFiles = 4096
const maxDigestBytes = 64 << 20

// extensionDigest covers every script the extension worker can load.
func extensionDigest(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("extension path is empty")
	}
	paths := []string{"manifest.json", "background.js"}
	for _, sub := range []string{"js", "lib"} {
		err := filepath.WalkDir(filepath.Join(dir, sub), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if entry.IsDir() || !(strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".mjs") || strings.HasSuffix(path, ".json")) {
				return nil
			}
			if len(paths) >= maxDigestFiles {
				return errors.New("extension source has too many files")
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			paths = append(paths, rel)
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	sort.Strings(paths)
	hash := sha256.New()
	total := 0
	for _, rel := range paths {
		data, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return "", err
		}
		if total += len(data); total > maxDigestBytes {
			return "", errors.New("extension source exceeds the digest budget")
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", filepath.ToSlash(rel), len(data))
		hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Restart stops this task's browser and starts it again with the same saved
// binary, extension, and host unless options override them. It loads changed
// extension source; tabs do not survive a restart.
func Restart(ctx context.Context, dataDir string, options Options) (Status, error) {
	if _, err := Stop(ctx, dataDir); err != nil {
		return Status{}, err
	}
	return Start(ctx, dataDir, options)
}

func validateRunningOptions(prior State, requested Options) error {
	if requested.Headed != prior.Headed {
		return errors.New("stop this task's browser before changing between headed and headless mode")
	}
	if requested.Binary == "" {
		requested.Binary = prior.Binary
	}
	if requested.Extension == "" {
		requested.Extension = prior.Extension
	}
	if requested.Host == "" {
		requested.Host = prior.Host
	}
	if requested.WindowSize == "" {
		requested.WindowSize = prior.WindowSize
	}
	resolved, _, err := resolveOptions(requested)
	if err != nil {
		return err
	}
	if prior.WindowSize == "" {
		// States from before window sizing ran at Chromium's default.
		resolved.WindowSize = ""
	}
	if resolved.Binary != prior.Binary || resolved.Extension != prior.Extension || resolved.Host != prior.Host || resolved.WindowSize != prior.WindowSize {
		return errors.New("stop this task's browser before changing its configuration")
	}
	return nil
}

func launchArguments(profile, extension string, headed bool, windowSize string) []string {
	width, height, ok := parseWindowSize(windowSize)
	if !ok {
		width, height, _ = parseWindowSize(DefaultWindowSize)
	}
	args := []string{"--user-data-dir=" + profile, "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0",
		"--no-first-run", "--no-default-browser-check", "--disable-background-networking", "--log-level=3",
		fmt.Sprintf("--window-size=%d,%d", width, height),
		"--load-extension=" + extension, "--disable-extensions-except=" + extension, "about:blank"}
	if !headed {
		args = append([]string{"--headless=new"}, args...)
	}
	return args
}

func browserEnvironment(bridgeDir, livePath string) []string {
	env := []string{}
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		switch name {
		case "REP_WORKSPACE", "REP_TASK", "REPLIVE_PATH", "REPANDROID_PATH", "REP_BRIDGE_DIR":
			continue
		}
		env = append(env, item)
	}
	return append(env, "REP_BRIDGE_DIR="+bridgeDir, "REPLIVE_PATH="+livePath)
}

// parseWindowSize accepts WIDTHxHEIGHT within practical display bounds.
func parseWindowSize(value string) (int, int, bool) {
	left, right, found := strings.Cut(strings.ToLower(strings.TrimSpace(value)), "x")
	width, widthErr := strconv.Atoi(left)
	height, heightErr := strconv.Atoi(right)
	if !found || widthErr != nil || heightErr != nil || width < 320 || width > 7680 || height < 240 || height > 4320 {
		return 0, 0, false
	}
	return width, height, true
}

func resolveOptions(options Options) (Options, string, error) {
	if options.WindowSize == "" {
		options.WindowSize = os.Getenv("REP_HEADLESS_WINDOW_SIZE")
	}
	if options.WindowSize == "" {
		options.WindowSize = DefaultWindowSize
	}
	if width, height, ok := parseWindowSize(options.WindowSize); ok {
		options.WindowSize = fmt.Sprintf("%dx%d", width, height)
	} else {
		return options, "", fmt.Errorf("window size must be WIDTHxHEIGHT between 320x240 and 7680x4320")
	}
	if options.Binary == "" {
		options.Binary = os.Getenv("REP_HEADLESS_BINARY")
	}
	if options.Binary == "" {
		home, _ := os.UserHomeDir()
		patterns := []string{filepath.Join(home, "Library/Caches/ms-playwright/chromium-*/chrome-mac-*/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"), filepath.Join(home, ".cache/ms-playwright/chromium-*/chrome-linux*/chrome")}
		var candidates []string
		for _, pattern := range patterns {
			found, _ := filepath.Glob(pattern)
			candidates = append(candidates, found...)
		}
		sort.Slice(candidates, func(i, j int) bool {
			left, right := browserRevision(candidates[i]), browserRevision(candidates[j])
			if left != right {
				return left > right
			}
			return candidates[i] > candidates[j]
		})
		if len(candidates) > 0 {
			options.Binary = candidates[0]
		} else {
			for _, name := range []string{"chromium", "chromium-browser", "google-chrome-for-testing"} {
				if path, err := exec.LookPath(name); err == nil {
					options.Binary = path
					break
				}
			}
		}
	}
	if options.Binary == "" {
		return options, "", errors.New("Chrome for Testing or Chromium is required; provide --binary or REP_HEADLESS_BINARY")
	}
	if strings.Contains(strings.ToLower(options.Binary), "headless_shell") || strings.Contains(options.Binary, "Arc.app") || strings.Contains(options.Binary, "Google Chrome.app") {
		return options, "", errors.New("use full Chrome for Testing or Chromium; Arc, branded Chrome, and headless_shell are not supported")
	}
	if options.Extension == "" {
		options.Extension = os.Getenv("REP_EXTENSION_PATH")
	}
	if options.Extension == "" {
		for _, path := range []string{"extension", "../rep", "."} {
			if _, err := os.Stat(filepath.Join(path, "manifest.json")); err == nil {
				options.Extension = path
				break
			}
		}
	}
	if options.Extension == "" {
		return options, "", errors.New("provide --extension /path/to/rep or REP_EXTENSION_PATH")
	}
	var err error
	options.Extension, err = filepath.EvalSymlinks(options.Extension)
	if err == nil {
		options.Extension, err = filepath.Abs(options.Extension)
	}
	if err != nil {
		return options, "", fmt.Errorf("resolve Rep extension: %w", err)
	}
	manifest, err := os.ReadFile(filepath.Join(options.Extension, "manifest.json"))
	var parsed struct {
		Name            string `json:"name"`
		Key             string `json:"key"`
		ManifestVersion int    `json:"manifest_version"`
	}
	if err != nil || json.Unmarshal(manifest, &parsed) != nil || parsed.Name != "rep+" || parsed.ManifestVersion != 3 || parsed.Key != "" {
		return options, "", errors.New("extension must be the unpacked Rep MV3 source without a manifest key")
	}
	if options.Host == "" {
		executable, _ := os.Executable()
		options.Host = filepath.Join(filepath.Dir(executable), "rep-host")
	}
	for _, path := range []*string{&options.Binary, &options.Host} {
		*path, err = filepath.Abs(*path)
		if err != nil {
			return options, "", err
		}
		info, err := os.Stat(*path)
		if err != nil || info.IsDir() || info.Mode()&0111 == 0 {
			return options, "", fmt.Errorf("headless executable is missing or not executable: %s", *path)
		}
	}
	sum := sha256.Sum256([]byte(options.Extension))
	raw := hex.EncodeToString(sum[:16])
	var id strings.Builder
	for _, value := range raw {
		if value >= '0' && value <= '9' {
			id.WriteRune('a' + value - '0')
		} else {
			id.WriteRune('k' + value - 'a')
		}
	}
	return options, id.String(), nil
}

func browserRevision(path string) int {
	for _, component := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.HasPrefix(component, "chromium-") {
			value, _ := strconv.Atoi(strings.TrimPrefix(component, "chromium-"))
			return value
		}
	}
	return 0
}
