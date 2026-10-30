package commands

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/bridge"

	"github.com/repplus/rep-cli/internal/store"
)

const (
	defaultBrowserDownloadChunk = 1024 * 1024
	minimumBrowserDownloadChunk = 64 * 1024
	maximumBrowserDownloadChunk = 4 * 1024 * 1024
)

type browserDownloadOptions struct {
	Overwrite       bool
	Timeout         time.Duration
	ChunkSize       int
	NoCache         bool
	OmitCredentials bool
	Validation      artifactValidationOptions
	Client          *bridge.Client
	Origin          string
	OnCreate        func(int) error
	OnClose         func(int)
}

type browserDownloadResult struct {
	Evidence            *operationEvidenceRef `json:"evidence,omitempty"`
	RequestID           string                `json:"request_id"`
	OutputPath          string                `json:"output_path"`
	Transport           string                `json:"transport"`
	Status              int                   `json:"status"`
	Bytes               int64                 `json:"bytes"`
	SHA256              string                `json:"sha256"`
	ContentType         string                `json:"content_type"`
	DetectedContentType string                `json:"detected_content_type"`
	TabID               int                   `json:"tab_id"`
	TabClosed           bool                  `json:"tab_closed"`
}

func downloadCapturedRequestInBrowser(ctx context.Context, req *store.Request, outputPath string, opts browserDownloadOptions) (_ browserDownloadResult, err error) {
	if strings.ToUpper(strings.TrimSpace(req.Method)) != "GET" {
		return browserDownloadResult{}, fmt.Errorf("captured method %s is not a GET", strings.ToUpper(strings.TrimSpace(req.Method)))
	}
	parsed, err := url.Parse(req.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return browserDownloadResult{}, errors.New("captured request URL must be absolute HTTP(S)")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Minute
	}
	if opts.Timeout > 30*time.Minute {
		return browserDownloadResult{}, errors.New("timeout must not exceed 30m")
	}
	if opts.ChunkSize == 0 {
		opts.ChunkSize = defaultBrowserDownloadChunk
	}
	if opts.ChunkSize < minimumBrowserDownloadChunk || opts.ChunkSize > maximumBrowserDownloadChunk {
		return browserDownloadResult{}, fmt.Errorf("chunk size must be between %d and %d bytes", minimumBrowserDownloadChunk, maximumBrowserDownloadChunk)
	}
	validator, err := prepareArtifactValidator(opts.Validation)
	if err != nil {
		return browserDownloadResult{}, err
	}
	absPath, parent, err := prepareBrowserDownloadDestination(outputPath, opts.Overwrite)
	if err != nil {
		return browserDownloadResult{}, err
	}

	transferCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	client := opts.Client
	if client == nil {
		client, err = bridge.Select(transferCtx, browserSelector)
		if err != nil {
			return browserDownloadResult{}, fmt.Errorf("select browser bridge: %w", err)
		}
	}

	var created struct {
		Created bool `json:"created"`
		TabID   int  `json:"tab_id"`
	}
	if err := client.Call(transferCtx, "browser.create", map[string]interface{}{
		"url": "about:blank", "active": false,
	}, &created); err != nil {
		return browserDownloadResult{}, err
	}
	if !created.Created || created.TabID < 0 {
		return browserDownloadResult{}, errors.New("browser create returned an invalid tab ID")
	}
	tabID := created.TabID
	if opts.OnCreate != nil {
		if err := opts.OnCreate(tabID); err != nil {
			if cleanupBrowserDownload(client, tabID, "", false) == nil && opts.OnClose != nil {
				opts.OnClose(tabID)
			}
			return browserDownloadResult{}, err
		}
	}
	attached := false
	streamHandle := ""
	defer func() {
		if tabID >= 0 && cleanupBrowserDownload(client, tabID, streamHandle, attached) == nil && opts.OnClose != nil {
			opts.OnClose(tabID)
		}
	}()

	if opts.Origin != "" {
		var navigated any
		if err := client.Call(transferCtx, "browser.navigate", map[string]any{"tab_id": tabID, "url": opts.Origin + "/", "wait": "load", "timeout_ms": opts.Timeout.Milliseconds(), "active": false}, &navigated); err != nil {
			return browserDownloadResult{}, err
		}
	}
	var attachResult map[string]interface{}
	if err := client.Call(transferCtx, "browser.attach", map[string]interface{}{"tab_id": tabID}, &attachResult); err != nil {
		return browserDownloadResult{}, err
	}
	attached = true

	var frameTree struct {
		FrameTree struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if err := callBrowserCDPResult(transferCtx, client, tabID, "Page.getFrameTree", nil, &frameTree); err != nil {
		return browserDownloadResult{}, err
	}
	if frameTree.FrameTree.Frame.ID == "" {
		return browserDownloadResult{}, errors.New("browser page returned no main frame")
	}

	var loaded struct {
		Resource struct {
			Success        bool                   `json:"success"`
			HTTPStatusCode int                    `json:"httpStatusCode"`
			Stream         string                 `json:"stream"`
			Headers        map[string]interface{} `json:"headers"`
			NetError       float64                `json:"netError"`
			NetErrorName   string                 `json:"netErrorName"`
		} `json:"resource"`
	}
	if err := callBrowserCDPResult(transferCtx, client, tabID, "Network.loadNetworkResource", map[string]interface{}{
		"frameId": frameTree.FrameTree.Frame.ID,
		"url":     req.URL,
		"options": map[string]interface{}{
			"disableCache":       opts.NoCache,
			"includeCredentials": !opts.OmitCredentials,
		},
	}, &loaded); err != nil {
		return browserDownloadResult{}, err
	}
	streamHandle = loaded.Resource.Stream
	if !loaded.Resource.Success {
		detail := strings.TrimSpace(loaded.Resource.NetErrorName)
		if detail == "" {
			detail = fmt.Sprintf("network error %.0f", loaded.Resource.NetError)
		}
		return browserDownloadResult{}, fmt.Errorf("browser resource load failed: %s", detail)
	}
	if loaded.Resource.HTTPStatusCode < 200 || loaded.Resource.HTTPStatusCode >= 300 {
		return browserDownloadResult{}, fmt.Errorf("browser resource returned HTTP %d", loaded.Resource.HTTPStatusCode)
	}
	if streamHandle == "" {
		return browserDownloadResult{}, errors.New("browser resource load returned no IO stream")
	}
	contentType := browserHeaderValue(loaded.Resource.Headers, "content-type")

	part, err := os.CreateTemp(parent, "."+filepath.Base(absPath)+".*.part")
	if err != nil {
		return browserDownloadResult{}, fmt.Errorf("create part file: %w", err)
	}
	partPath := part.Name()
	published := false
	defer func() {
		_ = part.Close()
		if !published {
			_ = os.Remove(partPath)
		}
	}()
	if err := part.Chmod(0600); err != nil {
		return browserDownloadResult{}, fmt.Errorf("secure part file: %w", err)
	}

	digest := sha256.New()
	var byteCount int64
	for {
		var read struct {
			Data          string `json:"data"`
			Base64Encoded bool   `json:"base64Encoded"`
			EOF           bool   `json:"eof"`
		}
		if err := callBrowserCDPResult(transferCtx, client, tabID, "IO.read", map[string]interface{}{
			"handle": streamHandle, "size": opts.ChunkSize,
		}, &read); err != nil {
			return browserDownloadResult{}, err
		}
		chunk := []byte(read.Data)
		if read.Base64Encoded {
			chunk, err = base64.StdEncoding.DecodeString(read.Data)
			if err != nil {
				return browserDownloadResult{}, errors.New("browser IO stream returned invalid base64 data")
			}
		}
		if len(chunk) > 0 {
			if _, err := part.Write(chunk); err != nil {
				return browserDownloadResult{}, fmt.Errorf("write part file: %w", err)
			}
			if _, err := digest.Write(chunk); err != nil {
				return browserDownloadResult{}, fmt.Errorf("hash part file: %w", err)
			}
			byteCount += int64(len(chunk))
		}
		if read.EOF {
			break
		}
		if len(chunk) == 0 {
			return browserDownloadResult{}, errors.New("browser IO stream stalled before EOF")
		}
	}
	if err := part.Sync(); err != nil {
		return browserDownloadResult{}, fmt.Errorf("sync part file: %w", err)
	}
	if err := part.Close(); err != nil {
		return browserDownloadResult{}, fmt.Errorf("close part file: %w", err)
	}
	downloadTabID := tabID
	if err := cleanupBrowserDownload(client, tabID, streamHandle, attached); err != nil {
		return browserDownloadResult{}, err
	}
	if opts.OnClose != nil {
		opts.OnClose(tabID)
	}
	streamHandle = ""
	attached = false
	tabID = -1
	validation, err := validator.validateDownloadedArtifact(partPath, absPath, contentType)
	if err != nil {
		return browserDownloadResult{}, err
	}
	if validation.Size != byteCount {
		return browserDownloadResult{}, fmt.Errorf("browser transfer size mismatch: stream=%d file=%d", byteCount, validation.Size)
	}
	if opts.Overwrite {
		if err := os.Rename(partPath, absPath); err != nil {
			return browserDownloadResult{}, fmt.Errorf("publish output: %w", err)
		}
	} else {
		if err := os.Link(partPath, absPath); err != nil {
			if errors.Is(err, os.ErrExist) {
				return browserDownloadResult{}, fmt.Errorf("output already exists: %s", absPath)
			}
			return browserDownloadResult{}, fmt.Errorf("publish output without overwrite: %w", err)
		}
		if err := os.Remove(partPath); err != nil {
			return browserDownloadResult{}, fmt.Errorf("remove published part link: %w", err)
		}
	}
	published = true
	_ = syncDirectory(parent)
	return browserDownloadResult{
		RequestID: req.ID, OutputPath: absPath, Transport: "browser",
		Status: loaded.Resource.HTTPStatusCode, Bytes: validation.Size,
		SHA256: hex.EncodeToString(digest.Sum(nil)), ContentType: validation.ContentType,
		DetectedContentType: validation.DetectedContentType, TabID: downloadTabID, TabClosed: true,
	}, nil
}

func callBrowserCDPResult(ctx context.Context, client *bridge.Client, tabID int, method string, params map[string]interface{}, result interface{}) error {
	if params == nil {
		params = map[string]interface{}{}
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := client.Call(ctx, "browser.cdp", map[string]interface{}{
		"tab_id": tabID, "method": method, "command_params": params,
	}, &envelope); err != nil {
		return fmt.Errorf("%s failed: %w", method, err)
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return fmt.Errorf("%s returned no result", method)
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return fmt.Errorf("decode %s result: %w", method, err)
	}
	return nil
}

func prepareBrowserDownloadDestination(outputPath string, overwrite bool) (string, string, error) {
	absPath, err := filepath.Abs(outputPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve output path: %w", err)
	}
	parent := filepath.Dir(absPath)
	info, err := os.Stat(parent)
	if err != nil {
		return "", "", fmt.Errorf("inspect output directory: %w", err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("output parent is not a directory: %s", parent)
	}
	if !overwrite {
		if _, err := os.Lstat(absPath); err == nil {
			return "", "", fmt.Errorf("output already exists: %s", absPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("inspect output: %w", err)
		}
	}
	return absPath, parent, nil
}

func browserHeaderValue(headers map[string]interface{}, wanted string) string {
	for name, value := range headers {
		if !strings.EqualFold(name, wanted) {
			continue
		}
		switch typed := value.(type) {
		case string:
			return typed
		case []interface{}:
			if len(typed) > 0 {
				return fmt.Sprint(typed[0])
			}
		default:
			return fmt.Sprint(typed)
		}
	}
	return ""
}

func cleanupBrowserDownload(client *bridge.Client, tabID int, streamHandle string, attached bool) error {
	if client == nil || tabID < 0 {
		return nil
	}
	if streamHandle != "" {
		var ignored map[string]interface{}
		_ = boundedBrowserCall(client, "browser.cdp", map[string]interface{}{
			"tab_id": tabID, "method": "IO.close",
			"command_params": map[string]interface{}{"handle": streamHandle},
		}, &ignored)
	}
	var detachErr error
	if attached {
		var ignored map[string]interface{}
		detachErr = boundedBrowserCall(client, "browser.detach", map[string]interface{}{"tab_id": tabID}, &ignored)
	}
	var ignored map[string]interface{}
	if closeErr := boundedBrowserCall(client, "browser.close", map[string]interface{}{"tab_id": tabID}, &ignored); closeErr != nil {
		if detachErr != nil {
			return fmt.Errorf("close temporary browser tab: %v (detach also failed: %v)", closeErr, detachErr)
		}
		return fmt.Errorf("close temporary browser tab: %w", closeErr)
	}
	return nil
}

func boundedBrowserCall(client *bridge.Client, method string, params map[string]interface{}, result interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return client.Call(ctx, method, params, result)
}
