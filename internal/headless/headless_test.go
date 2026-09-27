package headless

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskTransportPathsArePrivateDistinctAndShort(t *testing.T) {
	_, profileA, bridgeA := paths("/very/long/" + strings.Repeat("workspace", 30) + "/tasks/a")
	_, profileB, bridgeB := paths("/very/long/" + strings.Repeat("workspace", 30) + "/tasks/b")
	if profileA == profileB || bridgeA == bridgeB || len(filepath.Join(bridgeA, "bridge-123456789.sock")) >= 100 {
		t.Fatal("task transport paths are shared or exceed Unix socket limits")
	}
	dir := filepath.Join(t.TempDir(), "private")
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0700 {
		t.Fatal("directory is not private")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if privateDir(link) == nil {
		t.Fatal("accepted a shared directory symlink")
	}
}

func TestLifecycleLockAndStateRejectAnotherTask(t *testing.T) {
	dir := t.TempDir()
	guard, err := lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock(guard)
	if other, err := lock(dir); err == nil {
		unlock(other)
		t.Fatal("concurrent lifecycle acquired the same task")
	}
	_, profile, bridgeDir := paths(dir)
	state := State{Version: 1, Profile: profile, BridgeDir: bridgeDir}
	if err := writeState(dir, state); err != nil {
		t.Fatal(err)
	}
	if _, err := readState(dir); err != nil {
		t.Fatal(err)
	}
	state.Profile = filepath.Join(t.TempDir(), "another-profile")
	if err := writeState(dir, state); err != nil {
		t.Fatal(err)
	}
	if _, err := readState(dir); err == nil {
		t.Fatal("accepted another task's profile")
	}
}

func TestStopDoesNotSignalAnUnownedProcess(t *testing.T) {
	dir := t.TempDir()
	root, profile, bridgeDir := paths(dir)
	if err := privateDir(root); err != nil {
		t.Fatal(err)
	}
	// This is the test process itself. Even a matching PID/start stamp must
	// never authorize stopping a process lacking the owned browser profile.
	state := State{Version: 1, PID: os.Getpid(), ProcessStamp: processStamp(os.Getpid()), Profile: profile, BridgeDir: bridgeDir, Port: 1, WebSocket: "ws://127.0.0.1:1/devtools/browser/not-owned"}
	if err := writeState(dir, state); err != nil {
		t.Fatal(err)
	}
	if ownedProcess(state) {
		t.Fatal("test runner was mistaken for the task browser")
	}
	status, err := Stop(context.Background(), dir)
	if err != nil || status.Running || status.PID != 0 {
		t.Fatalf("stop unowned state: %+v %v", status, err)
	}
	if _, err := Connection(context.Background(), dir); err == nil {
		t.Fatal("stopped task selected a different bridge")
	}
}

func TestBridgeLookupRejectsForeignParentAndExtension(t *testing.T) {
	dir := t.TempDir()
	for _, registry := range []map[string]any{
		{"parent_pid": 123, "extension_id": "ours", "socket": filepath.Join(dir, "other.sock")},
		{"parent_pid": 456, "extension_id": "foreign", "socket": filepath.Join(dir, "other.sock")},
		{"parent_pid": 456, "extension_id": "ours", "socket": "/tmp/other-task.sock"},
	} {
		data, _ := json.Marshal(registry)
		if err := os.WriteFile(filepath.Join(dir, "bridge-1.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := connection(context.Background(), State{PID: 456, ExtensionID: "ours", BridgeDir: dir}); err == nil {
			t.Fatal("foreign bridge accepted")
		}
	}
}

func TestLaunchArgumentsKeepFullBrowserAndIsolatedLoopback(t *testing.T) {
	args := launchArguments("/task/profile", "/project/rep", false, "")
	joined := strings.Join(args, " ")
	for _, required := range []string{"--headless=new", "--user-data-dir=/task/profile", "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0", "--load-extension=/project/rep"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing %s", required)
		}
	}
	for _, unsafe := range []string{"--no-sandbox", "--disable-web-security", "--remote-allow-origins", "--remote-debugging-address=0.0.0.0"} {
		if strings.Contains(joined, unsafe) {
			t.Fatalf("unexpected flag %s", unsafe)
		}
	}
	if args[len(args)-1] != "about:blank" {
		t.Fatal("launch navigated to a website")
	}
}

func TestHeadedArgumentsAndProcessOwnershipMatchRequestedMode(t *testing.T) {
	const profile = "/task/private profile"
	for _, headed := range []bool{false, true} {
		args := launchArguments(profile, "/project/rep", headed, "1440x900")
		command := "/Applications/Chrome for Testing " + strings.Join(args, " ")
		state := State{Profile: profile, Headed: headed}
		if !ownedCommand(state, command) {
			t.Fatalf("rejected matching mode headed=%v", headed)
		}
		if ownedCommand(State{Profile: profile, Headed: !headed}, command) {
			t.Fatalf("accepted wrong mode headed=%v", headed)
		}
		if ownedCommand(State{Profile: profile + "-other", Headed: headed}, command) {
			t.Fatal("accepted another profile")
		}
		for _, foreign := range []string{
			strings.Replace(command, "--user-data-dir=", "--other-user-data-dir=", 1),
			strings.Replace(command, "--user-data-dir="+profile, "--user-data-dir="+profile+"-other", 1),
			command + " --headless=old",
		} {
			if ownedCommand(state, foreign) {
				t.Fatalf("accepted foreign or unsupported browser command: %s", foreign)
			}
		}
		if args[len(args)-1] != "about:blank" {
			t.Fatal("headed login mode navigated to a website")
		}
	}
}

func TestRunningBrowserRequiresStopBeforeModeOrConfigurationChange(t *testing.T) {
	dir := t.TempDir()
	extension := filepath.Join(dir, "rep")
	if err := os.Mkdir(extension, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extension, "manifest.json"), []byte(`{"name":"rep+","manifest_version":3}`), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "chromium")
	other := filepath.Join(dir, "other-chromium")
	for _, path := range []string{binary, other} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	canonicalExtension, _ := filepath.EvalSymlinks(extension)
	for _, headed := range []bool{false, true} {
		prior := State{Binary: binary, Extension: canonicalExtension, Host: binary, Headed: headed}
		if err := validateRunningOptions(prior, Options{Headed: headed}); err != nil {
			t.Fatalf("reuse same mode: %v", err)
		}
		for _, requested := range []Options{{Headed: !headed}, {Headed: headed, Binary: other}, {Headed: headed, Host: other}} {
			if err := validateRunningOptions(prior, requested); err == nil || !strings.Contains(err.Error(), "stop this task's browser") {
				t.Fatalf("configuration changed without stop: %+v, %v", requested, err)
			}
		}
	}
}

func TestBrowserEnvironmentDoesNotInheritTaskOverrides(t *testing.T) {
	for _, name := range []string{"REP_WORKSPACE", "REP_TASK", "REPLIVE_PATH", "REPANDROID_PATH", "REP_BRIDGE_DIR"} {
		t.Setenv(name, "foreign-task")
	}
	env := browserEnvironment("/task/bridge", "/task/staging.json")
	values := map[string]string{}
	for _, item := range env {
		name, value, _ := strings.Cut(item, "=")
		values[name] = value
	}
	for _, name := range []string{"REP_WORKSPACE", "REP_TASK", "REPANDROID_PATH"} {
		if _, ok := values[name]; ok {
			t.Fatalf("inherited %s", name)
		}
	}
	if values["REP_BRIDGE_DIR"] != "/task/bridge" || values["REPLIVE_PATH"] != "/task/staging.json" {
		t.Fatal("task transport overrides were lost")
	}
	if values["HOME"] != os.Getenv("HOME") {
		t.Fatal("browser launch changed the user's home")
	}
}

func TestResolveOptionsValidatesRepAndKeepsArgumentsLiteral(t *testing.T) {
	dir := t.TempDir()
	extension := filepath.Join(dir, "rep extension")
	if err := os.Mkdir(extension, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(extension, "manifest.json")
	if err := os.WriteFile(manifest, []byte(`{"name":"rep+","manifest_version":3}`), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "chromium")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	options, id, err := resolveOptions(Options{Binary: binary, Extension: extension, Host: binary})
	canonicalExtension, _ := filepath.EvalSymlinks(extension)
	if err != nil || len(id) != 32 || options.Extension != canonicalExtension {
		t.Fatalf("resolve options: %+v %s %v", options, id, err)
	}
	for _, letter := range id {
		if letter < 'a' || letter > 'p' {
			t.Fatal("invalid extension ID")
		}
	}
	if err := os.WriteFile(manifest, []byte(`{"name":"other extension","manifest_version":3}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveOptions(options); err == nil {
		t.Fatal("accepted unrelated extension")
	}
	options.Binary = "/Applications/Arc.app/Contents/MacOS/Arc"
	if _, _, err := resolveOptions(options); err == nil {
		t.Fatal("accepted user's Arc profile browser")
	}
}

func TestStartDiscardsOnlySessionRestoreState(t *testing.T) {
	profile := t.TempDir()
	base := filepath.Join(profile, "Default")
	for _, dir := range []string{"Sessions", "Sessions_Encrypted", "LocalStorage"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"Sessions/Session_1", "Sessions/Tabs_1", "Current Tabs", "Last Session", "Cookies", "LocalStorage/leveldb"} {
		if err := os.WriteFile(filepath.Join(base, file), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := discardSessionRestore(profile); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"Sessions", "Sessions_Encrypted", "Current Tabs", "Last Session"} {
		if _, err := os.Stat(filepath.Join(base, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s survived: %v", gone, err)
		}
	}
	for _, kept := range []string{"Cookies", "LocalStorage/leveldb"} {
		if _, err := os.Stat(filepath.Join(base, kept)); err != nil {
			t.Fatalf("login state %s was removed: %v", kept, err)
		}
	}
	if err := discardSessionRestore(filepath.Join(profile, "missing")); err != nil {
		t.Fatalf("a new profile must not fail: %v", err)
	}
}

func TestExtensionDigestTracksWorkerSourceOnly(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.json", `{"name":"rep+"}`)
	write("background.js", "import './js/a.js';")
	write("js/a.js", "export const a = 1;")
	write("css/panel.css", "body{}")
	write("tests/a.test.js", "test")
	first, err := extensionDigest(dir)
	if err != nil || len(first) != 64 {
		t.Fatalf("digest %q %v", first, err)
	}
	write("css/panel.css", "body{color:red}")
	write("tests/a.test.js", "changed")
	if same, _ := extensionDigest(dir); same != first {
		t.Fatal("files the worker never loads changed the digest")
	}
	write("js/a.js", "export const a = 2;")
	if changed, _ := extensionDigest(dir); changed == first {
		t.Fatal("a worker script change did not change the digest")
	}
	write("js/b.js", "")
	renamed, _ := extensionDigest(dir)
	if err := os.Rename(filepath.Join(dir, "js/b.js"), filepath.Join(dir, "js/c.js")); err != nil {
		t.Fatal(err)
	}
	if moved, _ := extensionDigest(dir); moved == renamed {
		t.Fatal("renaming a worker module did not change the digest")
	}
	if _, err := extensionDigest(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing extension produced a digest")
	}
}

func TestWorkerRefreshKeepsLoginState(t *testing.T) {
	profile := t.TempDir()
	base := filepath.Join(profile, "Default")
	for _, dir := range []string{"Service Worker/Database", "Service Worker/ScriptCache", "IndexedDB"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "Cookies"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := discardWorkerRegistrations(profile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "Service Worker")); !os.IsNotExist(err) {
		t.Fatalf("stale worker registration survived: %v", err)
	}
	for _, kept := range []string{"Cookies", "IndexedDB"} {
		if _, err := os.Stat(filepath.Join(base, kept)); err != nil {
			t.Fatalf("%s was removed: %v", kept, err)
		}
	}
}

func TestWindowSizeDefaultsToDesktopViewport(t *testing.T) {
	if joined := strings.Join(launchArguments("/p", "/e", false, ""), " "); !strings.Contains(joined, "--window-size=1280,900") {
		t.Fatalf("default window is not a desktop size: %s", joined)
	}
	if joined := strings.Join(launchArguments("/p", "/e", false, "1920x1080"), " "); !strings.Contains(joined, "--window-size=1920,1080") {
		t.Fatalf("explicit window size ignored: %s", joined)
	}
	for _, invalid := range []string{"1280", "0x0", "99999x10", "wide", "1280x-5"} {
		if _, _, ok := parseWindowSize(invalid); ok {
			t.Fatalf("accepted %q", invalid)
		}
	}
	if width, height, ok := parseWindowSize(" 1440X900 "); !ok || width != 1440 || height != 900 {
		t.Fatal("rejected a valid size")
	}
}
