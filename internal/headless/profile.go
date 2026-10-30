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
