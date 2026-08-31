package cmd

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/spf13/cobra"
)

type nativeHostManifest struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Path           string   `json:"path"`
	Type           string   `json:"type"`
	AllowedOrigins []string `json:"allowed_origins"`
}

type browserManifestCheck struct {
	Browser       string   `json:"browser"`
	ManifestPath  string   `json:"manifest_path"`
	Exists        bool     `json:"exists"`
	Valid         bool     `json:"valid"`
	HostPath      string   `json:"host_path,omitempty"`
	HostExists    bool     `json:"host_exists"`
	AllowedOrigin []string `json:"allowed_origins,omitempty"`
	Error         string   `json:"error,omitempty"`
}

var (
	installExtensionID   string
	installExtensionPath string
	installHostPath      string
)

var browserDoctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Inspect native-host installation and live browser bridges",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		checks, err := inspectNativeManifests(browserSelector)
		if err != nil {
			return output.EmitAgentError(os.Stdout, output.WrapError(err, output.ErrCodeInternal, "browser doctor"), getOutputMode() == "json")
		}
		registries, discoverErr := bridge.Discover()
		result := map[string]interface{}{
			"platform":  runtime.GOOS,
			"manifests": checks,
			"bridges":   registries,
			"healthy":   discoverErr == nil && len(registries) > 0,
		}
		if version := arcVersion(); version != "" {
			result["arc_version"] = version
		}
		if discoverErr != nil {
			result["bridge_error"] = discoverErr.Error()
		}
		return emitBrowserResult(result, func() {
			if version := result["arc_version"]; version != nil {
				fmt.Printf("Arc: %v\n", version)
			}
			for _, check := range checks {
				state := "missing"
				if check.Valid && check.HostExists {
					state = "ok"
				} else if check.Exists {
					state = "invalid"
				}
				fmt.Printf("%s native host: %s (%s)\n", check.Browser, state, check.ManifestPath)
				if check.Error != "" {
					fmt.Printf("  error: %s\n", check.Error)
				}
			}
			fmt.Printf("live bridges: %d\n", len(registries))
			if len(registries) == 0 {
				fmt.Println("next: rep browser install --extension-path /path/to/rep && reload rep+ in arc://extensions")
			}
		})
	},
}

var browserInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install the rep-host manifest for Arc/Chrome",
	Long: `Write a native-messaging manifest for the selected browser.

For an unpacked extension, --extension-path derives the stable Chrome
extension ID from its canonical path. --extension-id accepts an explicit
32-character ID instead. The host defaults to rep-host next to this rep binary.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		extensionID := strings.TrimSpace(installExtensionID)
		if extensionID == "" && installExtensionPath != "" {
			var err error
			extensionID, err = deriveExtensionID(installExtensionPath)
			if err != nil {
				return output.EmitAgentError(os.Stdout, output.WrapError(err, output.ErrCodeInvalidArgument, "browser install"), getOutputMode() == "json")
			}
		}
		if extensionID == "" {
			extensionID = extensionIDFromExistingManifest(browserSelector)
		}
		if !regexp.MustCompile(`^[a-p]{32}$`).MatchString(extensionID) {
			return output.EmitAgentError(os.Stdout, output.NewAgentError(
				output.ErrCodeInvalidArgument,
				"browser install",
				"a valid extension ID is required",
				"rep browser install --extension-path /absolute/path/to/rep",
				"rep browser install --extension-id <32-character-id>",
			), getOutputMode() == "json")
		}
		hostPath := installHostPath
		if hostPath == "" {
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			hostPath = filepath.Join(filepath.Dir(executable), "rep-host")
		}
		hostPath, _ = filepath.Abs(hostPath)
		info, err := os.Stat(hostPath)
		if err != nil || info.IsDir() || info.Mode()&0111 == 0 {
			return output.EmitAgentError(os.Stdout, output.NewAgentError(
				output.ErrCodeInvalidArgument,
				"browser install",
				fmt.Sprintf("rep-host is missing or not executable: %s", hostPath),
				"scripts/build_install.sh --host --install-dir ~/.local/bin",
			), getOutputMode() == "json")
		}
		targets, err := nativeManifestPaths(browserSelector)
		if err != nil {
			return output.EmitAgentError(os.Stdout, output.NewAgentError(output.ErrCodeInvalidArgument, "browser install", err.Error()), getOutputMode() == "json")
		}
		manifest := nativeHostManifest{
			Name: "com.repplus.host", Description: "rep+ Native Messaging Host for CLI browser control",
			Path: hostPath, Type: "stdio", AllowedOrigins: []string{"chrome-extension://" + extensionID + "/"},
		}
		content, _ := json.MarshalIndent(manifest, "", "  ")
		installed := make([]string, 0, len(targets))
		for _, path := range targets {
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return err
			}
			if err := atomicWriteSetupFile(path, append(content, '\n'), 0644); err != nil {
				return err
			}
			installed = append(installed, path)
		}
		result := map[string]interface{}{
			"extension_id": extensionID,
			"host_path":    hostPath,
			"manifests":    installed,
			"next":         "reload rep+ in the browser extension manager, then run rep browser status",
		}
		return emitBrowserResult(result, func() {
			fmt.Printf("extension: %s\n", extensionID)
			fmt.Printf("host: %s\n", hostPath)
			for _, path := range installed {
				fmt.Printf("installed: %s\n", path)
			}
			fmt.Println("next: reload rep+ in arc://extensions, then run rep browser status")
		})
	},
}

func inspectNativeManifests(selector string) ([]browserManifestCheck, error) {
	paths, err := nativeManifestPaths(selector)
	if err != nil {
		return nil, err
	}
	checks := make([]browserManifestCheck, 0, len(paths))
	for _, path := range paths {
		browser := browserForManifestPath(path)
		check := browserManifestCheck{Browser: browser, ManifestPath: path}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			if !os.IsNotExist(readErr) {
				check.Error = readErr.Error()
			}
			checks = append(checks, check)
			continue
		}
		check.Exists = true
		var manifest nativeHostManifest
		if err := sonic.Unmarshal(content, &manifest); err != nil {
			check.Error = err.Error()
			checks = append(checks, check)
			continue
		}
		check.HostPath = manifest.Path
		check.AllowedOrigin = manifest.AllowedOrigins
		check.Valid = manifest.Name == "com.repplus.host" && manifest.Type == "stdio" && len(manifest.AllowedOrigins) > 0
		if info, err := os.Stat(manifest.Path); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			check.HostExists = true
		} else if err != nil {
			check.Error = err.Error()
		}
		checks = append(checks, check)
	}
	return checks, nil
}

func nativeManifestPaths(selector string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	base := filepath.Join(home, "Library", "Application Support")
	paths := map[string]string{
		"arc":      filepath.Join(base, "Arc", "NativeMessagingHosts", "com.repplus.host.json"),
		"chrome":   filepath.Join(base, "Google", "Chrome", "NativeMessagingHosts", "com.repplus.host.json"),
		"chromium": filepath.Join(base, "Chromium", "NativeMessagingHosts", "com.repplus.host.json"),
		"edge":     filepath.Join(base, "Microsoft Edge", "NativeMessagingHosts", "com.repplus.host.json"),
		"brave":    filepath.Join(base, "BraveSoftware", "Brave-Browser", "NativeMessagingHosts", "com.repplus.host.json"),
	}
	selector = strings.ToLower(strings.TrimSpace(selector))
	if selector == "" || selector == "any" || selector == "all" {
		return []string{paths["arc"], paths["chrome"], paths["chromium"]}, nil
	}
	path, ok := paths[selector]
	if !ok {
		return nil, fmt.Errorf("unsupported browser %q (want arc, chrome, chromium, edge, brave, or any)", selector)
	}
	return []string{path}, nil
}

func browserForManifestPath(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.Contains(lower, "/arc/"):
		return "arc"
	case strings.Contains(lower, "/google/chrome/"):
		return "chrome"
	case strings.Contains(lower, "/chromium/"):
		return "chromium"
	case strings.Contains(lower, "/microsoft edge/"):
		return "edge"
	case strings.Contains(lower, "/bravesoftware/"):
		return "brave"
	default:
		return "unknown"
	}
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

func extensionIDFromExistingManifest(selector string) string {
	paths, err := nativeManifestPaths(selector)
	if err != nil {
		return ""
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var manifest nativeHostManifest
		if json.Unmarshal(content, &manifest) != nil {
			continue
		}
		for _, origin := range manifest.AllowedOrigins {
			id := strings.TrimSuffix(strings.TrimPrefix(origin, "chrome-extension://"), "/")
			if regexp.MustCompile(`^[a-p]{32}$`).MatchString(id) {
				return id
			}
		}
	}
	return ""
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

func arcVersion() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	output, err := exec.Command("/usr/bin/plutil", "-extract", "CFBundleShortVersionString", "raw", "-o", "-", "/Applications/Arc.app/Contents/Info.plist").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func init() {
	browserCmd.AddCommand(browserDoctorCmd, browserInstallCmd)
	browserInstallCmd.Flags().StringVar(&installExtensionID, "extension-id", "", "Explicit unpacked/store extension ID")
	browserInstallCmd.Flags().StringVar(&installExtensionPath, "extension-path", "", "Path to unpacked extension (derives its stable ID)")
	browserInstallCmd.Flags().StringVar(&installHostPath, "host-path", "", "Path to rep-host (default: next to rep)")
}
