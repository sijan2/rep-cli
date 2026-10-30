package commands

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

	"time"

	"github.com/repplus/rep-cli/internal/bridge"

	"github.com/repplus/rep-cli/internal/scope"
)

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
