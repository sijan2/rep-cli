package commands

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"

	"path/filepath"

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
	base := filepath.Join(home, "Library", "Application Support")
	paths := map[string]string{
		// Dia keeps Chrome's branding paths and reads Chrome's user-level native
		// hosts, so it shares this manifest with Chrome.
		"dia":      filepath.Join(base, "Google", "Chrome", "NativeMessagingHosts", "com.repplus.host.json"),
		"arc":      filepath.Join(base, "Arc", "NativeMessagingHosts", "com.repplus.host.json"),
		"chrome":   filepath.Join(base, "Google", "Chrome", "NativeMessagingHosts", "com.repplus.host.json"),
		"chromium": filepath.Join(base, "Chromium", "NativeMessagingHosts", "com.repplus.host.json"),
		"edge":     filepath.Join(base, "Microsoft Edge", "NativeMessagingHosts", "com.repplus.host.json"),
		"brave":    filepath.Join(base, "BraveSoftware", "Brave-Browser", "NativeMessagingHosts", "com.repplus.host.json"),
	}
	selector = strings.ToLower(strings.TrimSpace(selector))
	if selector == "" || selector == "any" || selector == "all" {
		return []string{paths["dia"], paths["arc"], paths["chromium"]}, nil
	}
	path, ok := paths[selector]
	if !ok {
		return nil, fmt.Errorf("unsupported browser %q (want dia, arc, chrome, chromium, edge, brave, or any)", selector)
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
