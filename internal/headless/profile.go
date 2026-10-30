package headless

import (
	"github.com/repplus/rep-cli/internal/scope"
	"os"
	"path/filepath"
	"strings"
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
