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
}

type State struct {
	Version      int    `json:"version"`
	PID          int    `json:"pid"`
	ProcessStamp string `json:"process_stamp,omitempty"`
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
}

type Status struct {
	State
	Running         bool   `json:"running"`
	Connected       bool   `json:"connected"`
	IsolatedProfile bool   `json:"isolated_profile"`
	Reason          string `json:"reason,omitempty"`
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
	if state.PID <= 0 || state.ProcessStamp == "" || processStamp(state.PID) != state.ProcessStamp {
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
	logFile, err := os.OpenFile(filepath.Join(root, "browser.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return Status{}, err
	}
	defer logFile.Close()
	command := exec.Command(options.Binary, launchArguments(profile, options.Extension, options.Headed)...)
	command.Env = browserEnvironment(bridgeDir, filepath.Join(root, "staging.json"))
	command.Stdin = nil
	command.Stdout, command.Stderr = logFile, logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return Status{}, fmt.Errorf("start headless browser: %w", err)
	}
	state := State{Version: 1, PID: command.Process.Pid, ProcessStamp: processStamp(command.Process.Pid), Binary: options.Binary,
		Extension: options.Extension, ExtensionID: extensionID, Host: options.Host, Profile: profile, BridgeDir: bridgeDir,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Headed: options.Headed}
	complete := false
	defer func() {
		if !complete {
			_ = command.Process.Kill()
			_ = command.Wait()
			state.PID, state.Port, state.WebSocket, state.ProcessStamp = 0, 0, "", ""
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
	state.PID, state.Port, state.WebSocket, state.ProcessStamp = 0, 0, "", ""
	if err := writeState(dataDir, state); err != nil {
		return Status{}, err
	}
	return Status{State: state, IsolatedProfile: true, Reason: "stopped"}, nil
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
	resolved, _, err := resolveOptions(requested)
	if err != nil {
		return err
	}
	if resolved.Binary != prior.Binary || resolved.Extension != prior.Extension || resolved.Host != prior.Host {
		return errors.New("stop this task's browser before changing its configuration")
	}
	return nil
}

func launchArguments(profile, extension string, headed bool) []string {
	args := []string{"--user-data-dir=" + profile, "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0",
		"--no-first-run", "--no-default-browser-check", "--disable-background-networking", "--log-level=3",
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

func resolveOptions(options Options) (Options, string, error) {
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
