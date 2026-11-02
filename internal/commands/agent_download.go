package commands

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

func newCoreDownloadCommand() *cobra.Command {
	var overwrite bool
	var timeout time.Duration
	command := &cobra.Command{Use: "download h_ID|URL PATH", Short: "Save a GET privately with browser credentials and validation", Args: cobra.ExactArgs(2)}
	command.Flags().BoolVar(&overwrite, "overwrite", false, "Atomically replace an existing destination")
	command.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "Total transfer budget (1s–30m)")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := coreDownload(cmd, args[0], args[1], overwrite, timeout); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}
func coreDownloadValidation(path string) artifactValidationOptions {
	magic := map[string]string{".pdf": "pdf", ".zip": "zip", ".ipa": "ipa", ".apk": "apk", ".jar": "zip", ".png": "png", ".jpg": "jpeg", ".jpeg": "jpeg", ".gz": "gzip", ".tgz": "gzip", ".xz": "xz", ".bz2": "bzip2", ".7z": "7z", ".rar": "rar", ".wasm": "wasm", ".sqlite": "sqlite", ".zst": "zstd"}[strings.ToLower(filepath.Ext(path))]
	return artifactValidationOptions{ExpectedMagic: magic}
}
func coreDownload(command *cobra.Command, source, path string, overwrite bool, timeout time.Duration) error {
	if timeout < time.Second || timeout > 30*time.Minute {
		return coreArgument("timeout must be between 1s and 30m")
	}
	if _, _, err := prepareBrowserDownloadDestination(path, overwrite); err != nil {
		return agentstate.Failure("destination_unavailable", "destination must be writable and absent unless --overwrite is given", coreNext("download", "--help"))
	}
	request := &store.Request{Method: "GET", URL: source}
	var addressed *agentstate.Tab
	captured := strings.HasPrefix(source, "h_")
	if captured {
		capture, req, err := coreCapturedRequest(source)
		if err != nil {
			return err
		}
		selected, _ := scope.Current()
		if selected.Identity() != capture.Owner {
			return agentstate.Failure("owned_by_other_task", "explicit task does not own this capture", coreNext("scope"))
		}
		request = req
		addressed = &agentstate.Tab{Browser: capture.Browser}
	}
	if request.Method != "GET" {
		return coreArgument("download accepts GET requests only")
	}
	origin, urlErr := coreRequestURL(request.URL)
	if urlErr != nil {
		return urlErr
	}
	index, err := coreStore()
	if err != nil {
		return err
	}
	selected, _ := scope.Current()
	ctx, cancel := context.WithTimeout(command.Context(), timeout)
	defer cancel()
	client, browser, browserErr := resolveCoreBrowser(ctx, index, addressed)
	var tab *agentstate.Tab
	var lease string
	var result browserDownloadResult
	validation := coreDownloadValidation(path)
	if browserErr == nil {
		result, browserErr = downloadCapturedRequestInBrowser(ctx, request, path, browserDownloadOptions{Client: client, Origin: origin, Overwrite: overwrite, Timeout: timeout, NoCache: true, Validation: validation,
			OnCreate: func(id int) error {
				var e error
				tab, e = index.Register(browser, id, selected.Identity(), selected.SessionKey, true)
				if e != nil {
					return e
				}
				tab, lease, e = index.Reserve(browser, selected.Identity(), selected.SessionKey, tab.Handle, true, false, timeout+time.Second)
				return e
			},
			OnClose: func(id int) {
				if tab != nil {
					_ = index.Change(tab.Handle, lease, func(t *agentstate.Tab) error { t.Closed = true; t.Lease = nil; return nil })
				}
			},
		})
	}
	if browserErr != nil {
		if !captured || ctx.Err() != nil {
			return agentstate.Failure("download_failed", "browser GET failed; URL downloads have no captured headers for fallback", coreNext("doctor"))
		}
		remaining := timeout
		if deadline, ok := ctx.Deadline(); ok {
			remaining = time.Until(deadline)
		}
		fallback, e := downloadCapturedRequest(ctx, request, path, capturedDownloadOptions{Overwrite: overwrite, Retries: 2, RetryDelay: time.Second, Timeout: remaining, Validation: validation})
		if e != nil {
			return agentstate.Failure("download_failed", "browser and captured-header GET transports did not produce a valid file", coreNext("body", request.ID))
		}
		result = browserDownloadResult{OutputPath: fallback.OutputPath, Transport: "curl", Status: fallback.Status, Bytes: fallback.Bytes, SHA256: fallback.SHA256, ContentType: fallback.ContentType, DetectedContentType: fallback.DetectedContentType}
	}
	return emitCore(command, coreResult{Browser: browser.Name, Profile: browser.Profile, Status: "complete", Complete: boolPointer(true), Meta: map[string]any{"path": result.OutputPath, "bytes": result.Bytes, "sha256": result.SHA256, "transport": result.Transport, "status": result.Status}, Untrusted: map[string]any{"content_type": result.ContentType, "detected_content_type": result.DetectedContentType}})
}

func init() { rootCmd.AddCommand(newCoreDownloadCommand()) }
