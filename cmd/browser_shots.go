package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

// browser shots applies haylxon's tab-pool design to Rep's bridge: a fixed set
// of worker tabs is reused across URLs, each navigation waits only for its
// lifecycle state, and every image is written to a private file.

const maxShotURLs = 10000

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

type shotsSummary struct {
	Evidence   *operationEvidenceRef `json:"evidence,omitempty"`
	Version    int                   `json:"version"`
	OutDir     string                `json:"out_dir"`
	Report     string                `json:"report,omitempty"`
	Total      int                   `json:"total"`
	Captured   int                   `json:"captured"`
	Failed     int                   `json:"failed"`
	Parallel   int                   `json:"parallel"`
	DurationMS float64               `json:"duration_ms"`
	Results    []shotResult          `json:"results"`
}

// readShotURLs keeps input order; blank lines and #-comments are ignored.
func readShotURLs(args []string, file string, stdin io.Reader) ([]string, error) {
	urls := []string{}
	add := func(line string) error {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			return nil
		}
		if len(urls) >= maxShotURLs {
			return fmt.Errorf("at most %d URLs per run", maxShotURLs)
		}
		if len(line) > 8192 {
			return errors.New("a URL exceeds 8192 bytes")
		}
		urls = append(urls, line)
		return nil
	}
	for _, arg := range args {
		if err := add(arg); err != nil {
			return nil, err
		}
	}
	readers := []io.Reader{}
	if file != "" {
		handle, err := os.Open(file)
		if err != nil {
			return nil, fmt.Errorf("cannot open URL file: %w", err)
		}
		defer handle.Close()
		readers = append(readers, handle)
	}
	if stdin != nil {
		readers = append(readers, stdin)
	}
	for _, reader := range readers {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), 16*1024)
		for scanner.Scan() {
			if err := add(scanner.Text()); err != nil {
				return nil, err
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read URLs: %w", err)
		}
	}
	if len(urls) == 0 {
		return nil, errors.New("provide URLs as arguments, --file, or --stdin")
	}
	return urls, nil
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

func defaultShotsDir() (string, error) {
	selected, err := scope.Current()
	if err != nil {
		return "", err
	}
	base := filepath.Join(os.TempDir(), "rep-artifacts")
	if selected.Scoped {
		base = selected.DataDir
	}
	return filepath.Join(base, "shots", time.Now().UTC().Format("20060102T150405.000Z")), nil
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

func runBrowserShots(cmd *cobra.Command, args []string, flags shotsFlags) (returnErr error) {
	fail := func(code string, err error) error {
		return output.EmitAgentError(os.Stdout, output.NewAgentError(code, "browser shots", err.Error()), getOutputMode() == "json")
	}
	shot := pageScreenshotFlags{Format: flags.Format, Quality: flags.Quality, FullPage: flags.FullPage}
	if err := validateScreenshotFlags(&shot); err != nil {
		return fail(output.ErrCodeInvalidArgument, err)
	}
	flags.Format = shot.Format
	if !validNavigateWait(flags.Wait) {
		return fail(output.ErrCodeInvalidArgument, errors.New("--wait must be one of "+strings.Join(navigateWaitModes, ", ")))
	}
	if flags.Parallel < 1 || flags.Parallel > 32 {
		return fail(output.ErrCodeInvalidArgument, errors.New("--parallel must be between 1 and 32"))
	}
	if flags.Timeout < 100*time.Millisecond || flags.Timeout > 2*time.Minute || flags.Delay < 0 || flags.Delay > time.Minute {
		return fail(output.ErrCodeInvalidArgument, errors.New("--timeout must be 100ms-2m and --delay 0-1m"))
	}
	var stdin io.Reader
	if flags.Stdin {
		stdin = cmd.InOrStdin()
	}
	urls, err := readShotURLs(args, flags.File, stdin)
	if err != nil {
		return fail(output.ErrCodeInvalidArgument, err)
	}
	outDir := flags.OutDir
	if outDir == "" {
		if outDir, err = defaultShotsDir(); err != nil {
			return fail(output.ErrCodeStoreWrite, err)
		}
	}
	if err := privateDirectory(outDir); err != nil {
		return fail(output.ErrCodeStoreWrite, err)
	}
	record, err := beginBrowserEvidence("browser.shots", map[string]any{"intent": "save screenshots for the supplied URL list", "urls": len(urls), "parallel": flags.Parallel, "stop": flags.Wait}, browserSelector, -1)
	if err != nil {
		return fail(output.ErrCodeStoreWrite, err)
	}
	defer record.finishOnReturn(&returnErr)
	ctx := cmd.Context()
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	client, err := selectBrowserBridge(connectCtx, browserSelector, "browser shots")
	cancel()
	if err != nil {
		return err
	}
	var status struct {
		Capabilities []string `json:"capabilities"`
	}
	statusCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = client.Call(statusCtx, "browser.status", nil, &status)
	cancel()
	if err != nil {
		return emitBrowserCallError("browser shots", err)
	}
	if !hasCapability(status.Capabilities, "lifecycle_navigation") || !hasCapability(status.Capabilities, "page_screenshot") {
		return pageRPCError("browser shots", &bridge.RPCError{Code: "method_not_found"}, "lifecycle navigation and screenshots")
	}
	started := time.Now()
	workers := min(flags.Parallel, len(urls))
	jobs := make(chan int)
	results := make([]shotResult, len(urls))
	pool := make([]*shotWorker, workers)
	var wait sync.WaitGroup
	record.dispatch()
	for i := range pool {
		pool[i] = &shotWorker{client: client, flags: flags, outDir: outDir, tab: -1, parent: record.ref()}
		wait.Add(1)
		go func(worker *shotWorker) {
			defer wait.Done()
			for index := range jobs {
				results[index] = worker.capture(ctx, index, urls[index])
				if getOutputMode() != "json" {
					printShotLine(results[index])
				}
			}
		}(pool[i])
	}
	for index := range urls {
		select {
		case jobs <- index:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wait.Wait()
	for index := range results {
		if results[index].URL == "" {
			results[index] = shotResult{Index: index, URL: urls[index], Error: "canceled before capture", ErrorCode: "canceled"}
		}
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	for _, worker := range pool {
		if worker.tab >= 0 {
			_ = client.Call(cleanup, "browser.close", map[string]any{"tab_id": worker.tab}, nil)
		}
	}
	cancel()
	summary := shotsSummary{Evidence: record.ref(), Version: 1, OutDir: outDir, Total: len(urls), Parallel: workers, Results: results}
	for _, result := range results {
		record.relatedOperation(result.Evidence, "scheduled_operation")
		if result.File != "" && result.Error == "" {
			summary.Captured++
		} else {
			summary.Failed++
		}
	}
	summary.DurationMS = float64(time.Since(started).Microseconds()) / 1000
	if flags.Report {
		report := filepath.Join(outDir, "index.html")
		if err := writeShotsReport(report, summary); err != nil {
			return fail(output.ErrCodeStoreWrite, err)
		}
		summary.Report = report
	}
	operationStatus := "completed"
	if summary.Failed > 0 {
		operationStatus = "failed"
	}
	if err := record.finish(map[string]any{"total": summary.Total, "captured": summary.Captured, "failed": summary.Failed,
		"parallel": summary.Parallel, "duration_ms": summary.DurationMS, "report": summary.Report}, operationStatus, "unverified", "", nil); err != nil {
		return fail(output.ErrCodeStoreWrite, err)
	}
	return emitBrowserResult(summary, func() {
		fmt.Printf("%d/%d captured in %.1fs with %d tabs: %s\n", summary.Captured, summary.Total, summary.DurationMS/1000, workers, outDir)
		if summary.Report != "" {
			fmt.Printf("report: %s\n", summary.Report)
		}
	})
}

func hasCapability(capabilities []string, wanted string) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}

var shotLineMu sync.Mutex

func printShotLine(result shotResult) {
	shotLineMu.Lock()
	defer shotLineMu.Unlock()
	if result.Error != "" {
		fmt.Printf("✗ %s: %s\n", result.URL, result.Error)
		return
	}
	suffix := ""
	if result.TimedOut {
		suffix = " (readiness timeout)"
	}
	fmt.Printf("✓ %s [%d] %.0fms %s%s\n", result.URL, result.HTTPStatus, result.ElapsedMS, filepath.Base(result.File), suffix)
}

var shotsReportTemplate = template.Must(template.New("report").Parse(`<!doctype html>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Rep screenshots</title>
<style>
:root{color-scheme:light dark;--bg:#f6f6f4;--card:#fff;--text:#1d1d1b;--muted:#6b6b66;--bad:#b3261e;--line:#deded8}
@media (prefers-color-scheme:dark){:root{--bg:#161615;--card:#20201e;--text:#ecece8;--muted:#9b9b95;--bad:#f2b8b5;--line:#33332f}}
body{margin:0;padding:16px;font:14px/1.4 system-ui,sans-serif;background:var(--bg);color:var(--text)}
header{margin:0 0 16px}h1{font-size:18px;margin:0 0 4px}p{margin:0;color:var(--muted)}
main{display:grid;gap:12px;grid-template-columns:repeat(auto-fill,minmax(280px,1fr))}
article{background:var(--card);border:1px solid var(--line);border-radius:8px;overflow:hidden;min-width:0}
img{display:block;width:100%;height:180px;object-fit:cover;object-position:top;border-bottom:1px solid var(--line)}
.body{padding:10px}.url{font-weight:600;overflow-wrap:anywhere}.meta{color:var(--muted);font-size:12px;margin-top:4px}.error{color:var(--bad);overflow-wrap:anywhere}
</style>
<header><h1>{{.Captured}} of {{.Total}} pages captured</h1><p>{{.Parallel}} tabs · {{printf "%.1f" .Seconds}} s</p></header>
<main>{{range .Results}}<article>{{if .File}}<a href="{{.File}}"><img loading="lazy" src="{{.File}}" alt="Screenshot of {{.URL}}"></a>{{end}}
<div class="body"><div class="url">{{.URL}}</div>{{if .Title}}<div class="meta">{{.Title}}</div>{{end}}
{{if .Error}}<div class="error">{{.Error}}</div>{{else}}<div class="meta">HTTP {{.Status}} · {{printf "%.0f" .Elapsed}} ms{{if .TimedOut}} · readiness timeout{{end}}</div>{{end}}</div></article>{{end}}</main>
`))

func writeShotsReport(path string, summary shotsSummary) error {
	type row struct {
		URL, Title, Error, File string
		Status                  int
		Elapsed                 float64
		TimedOut                bool
	}
	rows := make([]row, len(summary.Results))
	for i, result := range summary.Results {
		rows[i] = row{URL: result.URL, Title: result.Title, Error: result.Error, File: result.reportFile, Status: result.HTTPStatus, Elapsed: result.ElapsedMS, TimedOut: result.TimedOut}
	}
	var buffer bytes.Buffer
	if err := shotsReportTemplate.Execute(&buffer, map[string]any{"Captured": summary.Captured, "Total": summary.Total, "Parallel": summary.Parallel, "Seconds": summary.DurationMS / 1000, "Results": rows}); err != nil {
		return err
	}
	return writePrivateFile(path, buffer.Bytes())
}

func init() {
	flags := shotsFlags{Parallel: 4, Format: "png", Wait: "load", Timeout: 30 * time.Second}
	command := &cobra.Command{
		Use:   "shots [url...]",
		Short: "Screenshot many URLs in parallel through a pool of reused tabs",
		Long: `Capture many pages quickly: --parallel worker tabs are created once and
reused for every URL (tab pooling), each navigation waits only for its
lifecycle state (--wait, default load), and each image is written to a private
file in --out-dir (default: the task's shots directory). Readiness timeouts are
still captured and marked. Output is one JSON summary; --report also writes an
HTML gallery. Prefer --browser headless so bulk navigation stays in the task's
private profile instead of your everyday browser.`,
		RunE: func(cmd *cobra.Command, args []string) error { return runBrowserShots(cmd, args, flags) },
	}
	command.Flags().StringVar(&flags.File, "file", "", "Read URLs from a file, one per line")
	command.Flags().BoolVar(&flags.Stdin, "stdin", false, "Read URLs from standard input")
	command.Flags().IntVar(&flags.Parallel, "parallel", 4, "Worker tabs (1-32)")
	command.Flags().StringVar(&flags.OutDir, "out-dir", "", "Output directory (default: private task artifact directory)")
	command.Flags().StringVar(&flags.Format, "format", "png", "png, jpeg, or webp")
	command.Flags().IntVar(&flags.Quality, "quality", 0, "jpeg/webp quality 1-100 (default 80)")
	command.Flags().BoolVar(&flags.FullPage, "full-page", false, "Capture full scrollable pages")
	command.Flags().StringVar(&flags.Wait, "wait", "load", "commit, domcontentloaded, load, networkidle, or settled")
	command.Flags().DurationVar(&flags.Timeout, "timeout", 30*time.Second, "Per-URL readiness timeout")
	command.Flags().DurationVar(&flags.Delay, "delay", 0, "Extra wait after readiness, before each screenshot")
	command.Flags().BoolVar(&flags.Report, "report", false, "Also write an HTML gallery (index.html) in the output directory")
	browserCmd.AddCommand(command)
}
