package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

// Page commands give agents fast control without a network capture: navigate
// waits for a lifecycle state instead of network idle, and screenshots are
// written to private task files instead of flooding output with image data.

var navigateWaitModes = []string{"commit", "domcontentloaded", "load", "networkidle", "settled"}
var screenshotFormats = map[string]string{"png": ".png", "jpeg": ".jpg", "webp": ".webp"}

type pageNavigateFlags struct {
	Tab      int
	Wait     string
	Timeout  time.Duration
	Referrer string
	Active   bool
}

type pageScreenshotFlags struct {
	Tab      int
	Out      string
	Format   string
	Quality  int
	FullPage bool
	Selector string
}

func validNavigateWait(value string) bool {
	for _, mode := range navigateWaitModes {
		if value == mode {
			return true
		}
	}
	return false
}

// pageRPCError turns an extension that predates these RPCs into an actionable
// error instead of a generic method failure.
func pageRPCError(command string, err error, capability string) error {
	var rpcError *bridge.RPCError
	if errorsAs(err, &rpcError) && rpcError.Code == "method_not_found" {
		return output.EmitAgentError(os.Stdout, output.NewAgentError("extension_outdated", command,
			"the connected rep+ extension does not support "+capability+"; reload the extension from this checkout",
			"rep browser reload-extension --browser arc", "rep browser headless stop && rep browser headless start"), getOutputMode() == "json")
	}
	return emitBrowserCallError(command, err)
}

func navigateParams(url string, flags pageNavigateFlags) map[string]any {
	params := map[string]any{"url": url, "wait": flags.Wait, "timeout_ms": flags.Timeout.Milliseconds(), "active": flags.Active}
	if flags.Tab >= 0 {
		params["tab_id"] = flags.Tab
	}
	if flags.Referrer != "" {
		params["referrer"] = flags.Referrer
	}
	return params
}

func runBrowserNavigate(cmd *cobra.Command, url string, flags pageNavigateFlags) (returnErr error) {
	if !validNavigateWait(flags.Wait) {
		return output.EmitAgentError(os.Stdout, output.NewAgentError(output.ErrCodeInvalidArgument, "browser navigate", "--wait must be one of "+strings.Join(navigateWaitModes, ", ")), getOutputMode() == "json")
	}
	if flags.Timeout < 100*time.Millisecond || flags.Timeout > 2*time.Minute {
		return output.EmitAgentError(os.Stdout, output.NewAgentError(output.ErrCodeInvalidArgument, "browser navigate", "--timeout must be between 100ms and 2m"), getOutputMode() == "json")
	}
	record, err := beginBrowserEvidence("browser.navigate", map[string]any{"intent": "navigate the owned tab", "url": url, "stop": flags.Wait}, browserSelector, flags.Tab)
	if err != nil {
		return emitBrowserCallError("browser navigate", err)
	}
	defer record.finishOnReturn(&returnErr)
	ctx, cancel := context.WithTimeout(cmd.Context(), flags.Timeout+10*time.Second)
	defer cancel()
	client, err := selectBrowserBridge(ctx, browserSelector, "browser navigate")
	if err != nil {
		return err
	}
	var result map[string]any
	record.dispatch()
	if err := client.Call(ctx, "browser.navigate", navigateParams(url, flags), &result); err != nil {
		var rpcError *bridge.RPCError
		var observed any
		if errorsAs(err, &rpcError) {
			observed = rpcError.Data
		}
		if evidenceErr := record.finish(observed, "unknown", "unknown", "", err); evidenceErr != nil {
			return emitBrowserCallError("browser navigate", errors.Join(err, evidenceErr))
		}
		if errorsAs(err, &rpcError) && rpcError.Code == "navigation_timeout" {
			tab := ""
			if data, ok := rpcError.Data.(map[string]any); ok {
				if id, ok := data["tab_id"].(float64); ok {
					tab = strconv.FormatInt(int64(id), 10)
				}
			}
			message := rpcError.Message
			suggest := []string{"retry with --wait domcontentloaded or --wait commit", "raise --timeout"}
			if tab != "" {
				message += "; tab " + tab + " remains open and usable"
				suggest = append(suggest, "rep browser observe --tab "+tab, "rep browser screenshot --tab "+tab)
			}
			return output.EmitAgentError(os.Stdout, output.NewAgentError("navigation_timeout", "browser navigate", message, suggest...), getOutputMode() == "json")
		}
		return pageRPCError("browser navigate", err, "lifecycle navigation")
	}
	if err := record.finish(result, "completed", "satisfied", "", nil); err != nil {
		return emitBrowserCallError("browser navigate", err)
	}
	attachOperationEvidence(result, record)
	return emitBrowserResult(result, func() {
		fmt.Printf("tab %v reached %v in %vms: %v\n", result["tab_id"], result["reached"], result["duration_ms"], result["url"])
		if title, _ := result["title"].(string); title != "" {
			fmt.Printf("title: %s\n", title)
		}
	})
}

type screenshotResult struct {
	Evidence   *operationEvidenceRef `json:"evidence,omitempty"`
	TabID      int                   `json:"tab_id"`
	Path       string                `json:"path"`
	Format     string                `json:"format"`
	Bytes      int                   `json:"bytes"`
	SHA256     string                `json:"sha256"`
	Width      int                   `json:"width,omitempty"`
	Height     int                   `json:"height,omitempty"`
	FullPage   bool                  `json:"full_page"`
	Selector   string                `json:"selector,omitempty"`
	Clip       map[string]any        `json:"clip,omitempty"`
	DurationMS float64               `json:"duration_ms"`
}

func screenshotParams(tab int, flags pageScreenshotFlags) map[string]any {
	params := map[string]any{"tab_id": tab, "format": flags.Format, "full_page": flags.FullPage}
	if flags.Format != "png" && flags.Quality > 0 {
		params["quality"] = flags.Quality
	}
	if flags.Selector != "" {
		params["selector"] = flags.Selector
	}
	return params
}

// captureScreenshot requests one image and writes it without echoing its data.
func captureScreenshot(ctx context.Context, client *bridge.Client, tab int, flags pageScreenshotFlags, path string) (_ screenshotResult, returnErr error) {
	record, err := beginBrowserEvidence("browser.screenshot", map[string]any{"intent": "save the rendered image", "format": flags.Format, "full_page": flags.FullPage, "selector": flags.Selector}, client.Registry.Browser, tab)
	if err != nil {
		return screenshotResult{}, err
	}
	defer record.finishOnReturn(&returnErr)
	var wire struct {
		TabID      int            `json:"tab_id"`
		Format     string         `json:"format"`
		FullPage   bool           `json:"full_page"`
		Selector   string         `json:"selector"`
		Clip       map[string]any `json:"clip"`
		Data       string         `json:"data"`
		DurationMS float64        `json:"duration_ms"`
	}
	record.dispatch()
	if err := client.Call(ctx, "browser.screenshot", screenshotParams(tab, flags), &wire); err != nil {
		return screenshotResult{}, err
	}
	image, err := base64.StdEncoding.DecodeString(wire.Data)
	if err != nil || len(image) == 0 {
		return screenshotResult{}, errors.New("browser returned invalid screenshot data")
	}
	if path == "" {
		path, err = saveTaskArtifact("screenshots", "shot-*"+screenshotFormats[flags.Format], image)
	} else {
		err = writePrivateFile(path, image)
	}
	if err != nil {
		return screenshotResult{}, err
	}
	sum := sha256.Sum256(image)
	width, height := imageDimensions(flags.Format, image)
	result := screenshotResult{Evidence: record.ref(), TabID: tab, Path: path, Format: flags.Format, Bytes: len(image), SHA256: hex.EncodeToString(sum[:]), Width: width, Height: height,
		FullPage: wire.FullPage, Selector: wire.Selector, Clip: wire.Clip, DurationMS: wire.DurationMS}
	if err := record.artifact(path, "screenshot", "unknown", "image bytes retained; page coverage depends on the viewport or clip", map[string]any{"tab_id": tab, "full_page": wire.FullPage, "clip": wire.Clip}); err != nil {
		return result, err
	}
	if err := record.finish(result, "completed", "unverified", "", nil); err != nil {
		return result, err
	}
	return result, nil
}

func validateScreenshotFlags(flags *pageScreenshotFlags) error {
	flags.Format = strings.ToLower(strings.TrimSpace(flags.Format))
	if flags.Format == "jpg" {
		flags.Format = "jpeg"
	}
	if _, ok := screenshotFormats[flags.Format]; !ok {
		return errors.New("--format must be png, jpeg, or webp")
	}
	if flags.Quality < 0 || flags.Quality > 100 {
		return errors.New("--quality must be between 1 and 100")
	}
	if flags.FullPage && flags.Selector != "" {
		return errors.New("choose either --full-page or --selector")
	}
	return nil
}

func runBrowserScreenshot(cmd *cobra.Command, flags pageScreenshotFlags) error {
	if err := validateScreenshotFlags(&flags); err != nil {
		return output.EmitAgentError(os.Stdout, output.NewAgentError(output.ErrCodeInvalidArgument, "browser screenshot", err.Error()), getOutputMode() == "json")
	}
	if flags.Tab < 0 {
		return output.EmitAgentError(os.Stdout, output.NewAgentError(output.ErrCodeInvalidArgument, "browser screenshot", "--tab is required", "rep browser tabs"), getOutputMode() == "json")
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
	defer cancel()
	client, err := selectBrowserBridge(ctx, browserSelector, "browser screenshot")
	if err != nil {
		return err
	}
	result, err := captureScreenshot(ctx, client, flags.Tab, flags, flags.Out)
	if err != nil {
		var rpcError *bridge.RPCError
		if errorsAs(err, &rpcError) {
			return pageRPCError("browser screenshot", err, "screenshots")
		}
		return output.EmitAgentError(os.Stdout, output.WrapError(err, output.ErrCodeStoreWrite, "browser screenshot"), getOutputMode() == "json")
	}
	return emitBrowserResult(result, func() {
		fmt.Printf("%s (%dx%d %s, %d bytes, %.0fms)\n", result.Path, result.Width, result.Height, result.Format, result.Bytes, result.DurationMS)
	})
}

// saveTaskArtifact writes a unique private file under the task namespace, so
// concurrent agents and repeated captures can never replace one another.
func saveTaskArtifact(subdir, pattern string, data []byte) (string, error) {
	selected, err := scope.Current()
	if err != nil {
		return "", err
	}
	base := filepath.Join(os.TempDir(), "rep-artifacts")
	if selected.Scoped {
		base = selected.DataDir
	}
	dir := filepath.Join(base, subdir)
	if err := privateDirectory(dir); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			os.Remove(file.Name())
		}
	}()
	if _, err := file.Write(data); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	complete = true
	return file.Name(), nil
}

func privateDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("artifact directory must be a regular directory: %s", dir)
	}
	return os.Chmod(dir, 0700)
}

// writePrivateFile replaces path atomically with owner-only permissions.
func writePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".rep-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// imageDimensions reads pixel dimensions from PNG, JPEG, or WebP headers.
func imageDimensions(format string, data []byte) (int, int) {
	if format == "webp" {
		return webpDimensions(data)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return config.Width, config.Height
}

func webpDimensions(data []byte) (int, int) {
	if len(data) < 30 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0
	}
	switch string(data[12:16]) {
	case "VP8X":
		width := int(data[24]) | int(data[25])<<8 | int(data[26])<<16
		height := int(data[27]) | int(data[28])<<8 | int(data[29])<<16
		return width + 1, height + 1
	case "VP8L":
		if data[20] != 0x2f {
			return 0, 0
		}
		bits := binary.LittleEndian.Uint32(data[21:25])
		return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1
	case "VP8 ":
		if data[23] != 0x9d || data[24] != 0x01 || data[25] != 0x2a {
			return 0, 0
		}
		return int(binary.LittleEndian.Uint16(data[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(data[28:30]) & 0x3fff)
	}
	return 0, 0
}

func init() {
	navigate := pageNavigateFlags{Tab: -1, Wait: "load", Timeout: 30 * time.Second}
	navigateCmd := &cobra.Command{
		Use:   "navigate <url>",
		Short: "Navigate a tab and wait for a lifecycle state, without a network capture",
		Long: `Navigate an owned tab (or a new inactive tab when --tab is omitted) and wait
for a Chromium lifecycle state: commit, domcontentloaded, load (default),
networkidle, or settled (load plus 200 ms without DOM mutations, at most 3 s).
Unlike 'browser open', no capture session or network-idle floor is added, so
this is the fast path for interaction workflows. Use 'browser open' when the
page's network traffic is the evidence you need.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runBrowserNavigate(cmd, args[0], navigate) },
	}
	navigateCmd.Flags().IntVar(&navigate.Tab, "tab", -1, "Existing tab to navigate (default: create an inactive tab)")
	navigateCmd.Flags().StringVar(&navigate.Wait, "wait", "load", "commit, domcontentloaded, load, networkidle, or settled")
	navigateCmd.Flags().DurationVar(&navigate.Timeout, "timeout", 30*time.Second, "Maximum time to reach --wait")
	navigateCmd.Flags().StringVar(&navigate.Referrer, "referrer", "", "Navigation referrer URL")
	navigateCmd.Flags().BoolVar(&navigate.Active, "active", false, "Activate a newly created tab")

	shot := pageScreenshotFlags{Tab: -1, Format: "png"}
	screenshotCmd := &cobra.Command{
		Use:   "screenshot --tab ID",
		Short: "Capture a tab screenshot into a private task file",
		Long: `Capture the viewport, the full page (--full-page), or one top-frame element
(--selector CSS) as png, jpeg, or webp. The image is written to --out or a
unique private file under the task's screenshots directory; output contains
only its path, size, dimensions, and SHA-256, never the image data.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return runBrowserScreenshot(cmd, shot) },
	}
	screenshotCmd.Flags().IntVar(&shot.Tab, "tab", -1, "Tab to capture")
	screenshotCmd.Flags().StringVar(&shot.Out, "out", "", "Output file (default: private task artifact)")
	screenshotCmd.Flags().StringVar(&shot.Format, "format", "png", "png, jpeg, or webp")
	screenshotCmd.Flags().IntVar(&shot.Quality, "quality", 0, "jpeg/webp quality 1-100 (default 80)")
	screenshotCmd.Flags().BoolVar(&shot.FullPage, "full-page", false, "Capture the full scrollable page")
	screenshotCmd.Flags().StringVar(&shot.Selector, "selector", "", "Capture only the first element matching this CSS selector")
	browserCmd.AddCommand(navigateCmd, screenshotCmd)
}
