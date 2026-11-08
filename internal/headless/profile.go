package headless

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/repplus/rep-cli/internal/scope"
)

// NamedProfileDir is independent of an agent session, allowing sign-ins to
// survive new sessions. Lifecycle operations already flock this directory.
func NamedProfileDir(name string) (string, error) {
	if name == "" {
		name = strings.TrimSpace(os.Getenv("REP_PROFILE"))
	}
	if name == "" {
		name = "default"
	}
	if err := scope.ValidateName(name); err != nil {
		return "", err
	}
	base, err := scope.GlobalDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "profiles", name), nil
}

// Inspect reads local lifecycle facts without probing a socket or endpoint.
func Inspect(dataDir string) (Status, error) {
	state, err := readState(dataDir)
	if os.IsNotExist(err) {
		return Status{IsolatedProfile: true, Reason: "not_started"}, nil
	}
	if err != nil {
		return Status{}, err
	}
	running := ownedProcess(state)
	reason := "stopped"
	if running {
		reason = "not_probed"
	}
	return Status{State: state, Running: running, IsolatedProfile: true, Reason: reason}, nil
}

// ExtensionDigest uses the same source digest advertised by extension hello.
func ExtensionDigest(directory string) (string, error) { return extensionDigest(directory) }

// PrepareExtension refreshes the metadata read by the worker on startup. Source
// edits otherwise survive a reload with the old advertised digest, making doctor
// time out waiting for a version the worker cannot announce. Publish atomically so
// another worker starting during repair never reads a partial JSON document.
func PrepareExtension(directory string) (digest string, changed bool, err error) {
	digest, err = extensionDigest(directory)
	if err != nil {
		return "", false, err
	}
	path := filepath.Join(directory, "build-info.json")
	var info struct {
		Digest string `json:"digest"`
	}
	if data, e := os.ReadFile(path); e == nil && json.Unmarshal(data, &info) == nil && info.Digest == digest {
		return digest, false, nil
	}
	info.Digest = digest
	data, err := json.Marshal(info)
	if err != nil {
		return "", false, err
	}
	file, err := os.CreateTemp(directory, ".build-info-*")
	if err != nil {
		return "", false, err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(append(data, '\n')); err != nil {
		file.Close()
		return "", false, err
	}
	if err = file.Close(); err != nil {
		return "", false, err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return "", false, err
	}
	return digest, true, nil
}

// ConfiguredExtension is recorded by installation; discovery is only a legacy
// fallback, so doctor and private profiles do not depend on the working directory.
func ConfiguredExtension() string {
	if path := strings.TrimSpace(os.Getenv("REP_EXTENSION_PATH")); path != "" {
		return path
	}
	config := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if config == "" {
		config, _ = os.UserConfigDir()
	}
	data, err := os.ReadFile(filepath.Join(config, "rep", "extension-path"))
	if err != nil || len(data) > 8192 {
		return ""
	}
	path := strings.TrimSpace(string(data))
	if !filepath.IsAbs(path) {
		return ""
	}
	return path
}
