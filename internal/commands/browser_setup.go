package commands

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"

	"path/filepath"
	"runtime"
	"strings"
)

type nativeHostManifest struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Path           string   `json:"path"`
	Type           string   `json:"type"`
	AllowedOrigins []string `json:"allowed_origins"`
}

func nativeManifestPaths(selector string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return nativeManifestPathsFor(runtime.GOOS, home, os.Getenv("XDG_CONFIG_HOME"), selector)
}

func nativeManifestPathsFor(goos, home, xdgConfig, selector string) ([]string, error) {
	const file = "com.repplus.host.json"
	var paths map[string]string
	defaults := []string{"dia", "arc", "chromium"}
	if goos == "linux" {
		// Linux browsers read user-level hosts from their config directory.
		// Dia does not ship for Linux; its default selector maps to Chrome so
		// rep doctor's default repair works. Arc is unavailable.
		base := xdgConfig
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		paths = map[string]string{
			"dia":      filepath.Join(base, "google-chrome", "NativeMessagingHosts", file),
			"chrome":   filepath.Join(base, "google-chrome", "NativeMessagingHosts", file),
			"chromium": filepath.Join(base, "chromium", "NativeMessagingHosts", file),
			"edge":     filepath.Join(base, "microsoft-edge", "NativeMessagingHosts", file),
			"brave":    filepath.Join(base, "BraveSoftware", "Brave-Browser", "NativeMessagingHosts", file),
		}
		defaults = []string{"chrome", "chromium"}
	} else {
		base := filepath.Join(home, "Library", "Application Support")
		paths = map[string]string{
			// Dia keeps Chrome's branding paths and reads Chrome's user-level native
			// hosts, so it shares this manifest with Chrome.
			"dia":      filepath.Join(base, "Google", "Chrome", "NativeMessagingHosts", file),
			"arc":      filepath.Join(base, "Arc", "NativeMessagingHosts", file),
			"chrome":   filepath.Join(base, "Google", "Chrome", "NativeMessagingHosts", file),
			"chromium": filepath.Join(base, "Chromium", "NativeMessagingHosts", file),
			"edge":     filepath.Join(base, "Microsoft Edge", "NativeMessagingHosts", file),
			"brave":    filepath.Join(base, "BraveSoftware", "Brave-Browser", "NativeMessagingHosts", file),
		}
	}
	selector = strings.ToLower(strings.TrimSpace(selector))
	if selector == "" || selector == "any" || selector == "all" {
		selected := make([]string, 0, len(defaults))
		for _, name := range defaults {
			selected = append(selected, paths[name])
		}
		return selected, nil
	}
	path, ok := paths[selector]
	if !ok {
		names := make([]string, 0, len(paths))
		for _, name := range []string{"dia", "arc", "chrome", "chromium", "edge", "brave"} {
			if _, present := paths[name]; present {
				names = append(names, name)
			}
		}
		return nil, fmt.Errorf("unsupported browser %q on %s (want %s, or any)", selector, goos, strings.Join(names, ", "))
	}
	return []string{path}, nil
}

func mergeAllowedOrigins(origin, path string) []string {
	origins := []string{origin}
	content, err := os.ReadFile(path)
	if err != nil {
		return origins
	}
	var existing nativeHostManifest
	if json.Unmarshal(content, &existing) != nil || existing.Name != "com.repplus.host" {
		return origins
	}
	for _, other := range existing.AllowedOrigins {
		if other != origin {
			origins = append(origins, other)
		}
	}
	return origins
}

func deriveExtensionID(path string) (string, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(canonical, "manifest.json")); err != nil {
		return "", fmt.Errorf("extension manifest not found under %s: %w", canonical, err)
	}
	digest := sha256.Sum256([]byte(canonical))
	const alphabet = "abcdefghijklmnop"
	var result strings.Builder
	result.Grow(32)
	for _, value := range digest[:16] {
		result.WriteByte(alphabet[value>>4])
		result.WriteByte(alphabet[value&0x0f])
	}
	return result.String(), nil
}

func atomicWriteSetupFile(path string, content []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".rep-manifest-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
