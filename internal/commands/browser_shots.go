package commands

import (
	"context"
	"errors"
	"fmt"

	"path/filepath"
	"regexp"
	"strings"

	"time"

	"github.com/repplus/rep-cli/internal/bridge"
)

type shotsFlags struct {
	File     string
	Stdin    bool
	Parallel int
	OutDir   string
	Format   string
	Quality  int
	FullPage bool
	Wait     string
	Timeout  time.Duration
	Delay    time.Duration
	Report   bool
}

type shotResult struct {
	Evidence   *operationEvidenceRef `json:"evidence,omitempty"`
	Index      int                   `json:"index"`
	URL        string                `json:"url"`
	FinalURL   string                `json:"final_url,omitempty"`
	Title      string                `json:"title,omitempty"`
	HTTPStatus int                   `json:"http_status,omitempty"`
	Reached    string                `json:"reached,omitempty"`
	TimedOut   bool                  `json:"timed_out,omitempty"`
	File       string                `json:"file,omitempty"`
	Bytes      int                   `json:"bytes,omitempty"`
	Width      int                   `json:"width,omitempty"`
	Height     int                   `json:"height,omitempty"`
	SHA256     string                `json:"sha256,omitempty"`
	ElapsedMS  float64               `json:"elapsed_ms"`
	Error      string                `json:"error,omitempty"`
	ErrorCode  string                `json:"error_code,omitempty"`
	TabID      int                   `json:"tab_id,omitempty"`
	reportFile string
}

var unsafeShotName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func shotFileName(index int, rawURL, extension string) string {
	name := rawURL
	if cut := strings.Index(name, "://"); cut >= 0 {
		name = name[cut+3:]
	}
	name = strings.Trim(unsafeShotName.ReplaceAllString(name, "_"), "_.")
	if len(name) > 80 {
		name = name[:80]
	}
	if name == "" {
		name = "page"
	}
	return fmt.Sprintf("%04d-%s%s", index+1, name, extension)
}

// shotWorker owns one tab for the whole run and reuses it for every URL it takes.
type shotWorker struct {
	client *bridge.Client
	flags  shotsFlags
	outDir string
	tab    int
	parent *operationEvidenceRef
}

func (worker *shotWorker) ensureTab(ctx context.Context) error {
	if worker.tab >= 0 {
		return nil
	}
	var created struct {
		TabID int `json:"tab_id"`
	}
	if err := worker.client.Call(ctx, "browser.create", map[string]any{"url": "about:blank", "active": false}, &created); err != nil {
		return err
	}
	worker.tab = created.TabID
	return nil
}

func rpcCode(err error) string {
	var rpcError *bridge.RPCError
	if errorsAs(err, &rpcError) {
		return rpcError.Code
	}
	return ""
}

func (worker *shotWorker) capture(ctx context.Context, index int, rawURL string) (result shotResult) {
	started := time.Now()
	result = shotResult{Index: index, URL: rawURL}
	record, err := beginBrowserEvidence("browser.shot", map[string]any{"intent": "navigate and save a screenshot", "url": rawURL, "stop": worker.flags.Wait}, worker.client.Registry.Browser, worker.tab)
	if err != nil {
		result.Error, result.ErrorCode = err.Error(), "evidence_write_failed"
		return result
	}
	result.Evidence = record.ref()
	record.relatedOperation(worker.parent, "scheduled_by")
	defer func() {
		result.ElapsedMS = float64(time.Since(started).Microseconds()) / 1000
		status, verification := "completed", "unverified"
		var cause error
		if result.Error != "" {
			status, verification, cause = "unknown", "unknown", errors.New(result.Error)
		}
		if err := record.finish(result, status, verification, "", cause); err != nil {
			result.Error, result.ErrorCode = err.Error(), "evidence_write_failed"
		}
	}()
	record.dispatch()
	for attempt := 0; ; attempt++ {
		if err := worker.ensureTab(ctx); err != nil {
			result.Error, result.ErrorCode = err.Error(), rpcCode(err)
			return result
		}
		result.TabID = worker.tab
		var page struct {
			URL        string `json:"url"`
			Title      string `json:"title"`
			HTTPStatus int    `json:"http_status"`
			Reached    string `json:"reached"`
		}
		navigation := pageNavigateFlags{Tab: worker.tab, Wait: worker.flags.Wait, Timeout: worker.flags.Timeout}
		callCtx, cancel := context.WithTimeout(ctx, worker.flags.Timeout+10*time.Second)
		err := worker.client.Call(callCtx, "browser.navigate", navigateParams(rawURL, navigation), &page)
		cancel()
		if err != nil {
			code := rpcCode(err)
			var rpcError *bridge.RPCError
			if code == "navigation_timeout" && errorsAs(err, &rpcError) {
				// The page is loaded as far as it got; capture it like haylxon does.
				result.TimedOut = true
				if data, ok := rpcError.Data.(map[string]any); ok {
					page.URL, _ = data["url"].(string)
					page.Title, _ = data["title"].(string)
					page.Reached, _ = data["reached"].(string)
					if status, ok := data["http_status"].(float64); ok {
						page.HTTPStatus = int(status)
					}
				}
			} else if attempt == 0 && code == "chrome_api_error" {
				// The worker's tab was closed or crashed; replace it once.
				worker.tab = -1
				continue
			} else {
				result.Error, result.ErrorCode = err.Error(), code
				return result
			}
		}
		result.FinalURL, result.Title, result.HTTPStatus, result.Reached = page.URL, page.Title, page.HTTPStatus, page.Reached
		break
	}
	if worker.flags.Delay > 0 {
		select {
		case <-ctx.Done():
			result.Error = ctx.Err().Error()
			return result
		case <-time.After(worker.flags.Delay):
		}
	}
	name := shotFileName(index, rawURL, screenshotFormats[worker.flags.Format])
	shot := pageScreenshotFlags{Tab: worker.tab, Format: worker.flags.Format, Quality: worker.flags.Quality, FullPage: worker.flags.FullPage}
	callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	image, err := captureScreenshot(callCtx, worker.client, worker.tab, shot, filepath.Join(worker.outDir, name))
	record.relatedOperation(image.Evidence, "screenshot_result")
	if err != nil {
		result.Error, result.ErrorCode = err.Error(), rpcCode(err)
		return result
	}
	result.File, result.Bytes, result.Width, result.Height, result.SHA256 = image.Path, image.Bytes, image.Width, image.Height, image.SHA256
	result.reportFile = name
	return result
}
