package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "embed"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

// readabilityJS extracts the main readable content of a rendered page as
// markdown. It runs in the page renderer, so it sees post-hydration DOM that
// a static HTML fetch never would.
//
//go:embed assets/readability.js
var readabilityJS string

type readFlags struct {
	Format   string
	MaxBytes int
	Offset   int
	Section  string
	TabID    int
	KeepTab  bool
	Refresh  bool
	Timeout  time.Duration
}

var readOpts readFlags

var readCmd = &cobra.Command{
	Use:   "read <url-or-@file>",
	Short: "Read a page as bounded markdown through the browser session",
	Long: `Render a URL in the task's browser and return its readable content as
markdown, without flooding agent context with markup.

Unlike 'rep browse', read does not seal a full network capture: a heavy page
stays readable even when byte-budget limits would abort an archive. Use
'rep browse' when you need the network evidence, 'rep read' when you need the text.

Fidelity rules that matter for research:
  - <pre>/<code> are preserved VERBATIM inside fences (payload bytes survive)
  - tables become markdown tables; image alt/src and SVG label text are kept
  - headings are indexed, so --section can return one part of a long document

Examples:
  rep read https://example.com/post              Readable markdown (bounded)
  rep read <url> --format index                  Heading outline only
  rep read <url> --section "Key Takeaways"       One section
  rep read <url> --format links                  Outbound links
  rep read <url> --offset 20000                  Next page of a long document
  rep read <url> --tab 123                       Reuse an already-open owned tab`,
	Args: cobra.ExactArgs(1),
	RunE: runRead,
}

func init() {
	rootCmd.AddCommand(readCmd)
	readCmd.Flags().StringVar(&readOpts.Format, "format", "md", "Output: md, links, index, json")
	readCmd.Flags().IntVar(&readOpts.MaxBytes, "max-bytes", 20000, "Maximum content bytes returned")
	readCmd.Flags().IntVar(&readOpts.Offset, "offset", 0, "Byte offset into the extracted content")
	readCmd.Flags().StringVar(&readOpts.Section, "section", "", "Return only the section under this heading")
	readCmd.Flags().IntVar(&readOpts.TabID, "tab", -1, "Reuse an open owned tab instead of navigating")
	readCmd.Flags().BoolVar(&readOpts.KeepTab, "keep-tab", false, "Keep the tab open after reading")
	readCmd.Flags().BoolVar(&readOpts.Refresh, "refresh", false, "Bypass the cached extraction")
	advancedFlags(readCmd, "refresh")
	readCmd.Flags().DurationVar(&readOpts.Timeout, "timeout", 45*time.Second, "Maximum navigation and extraction duration")
}

// readDocument is the cached, provenance-bearing extraction record.
type readDocument struct {
	URL       string        `json:"url"`
	Title     string        `json:"title"`
	Chars     int           `json:"chars"`
	FetchedAt string        `json:"fetched_at"`
	Headings  []readHeading `json:"headings"`
	Text      string        `json:"text"`
	Links     []string      `json:"links"`
	Stats     readStats     `json:"stats"`
	// Provenance recorded by rep, not by the page.
	RequestedURL string `json:"requested_url,omitempty"`
	Redirected   bool   `json:"redirected,omitempty"`
	ContentSHA   string `json:"content_sha256,omitempty"`
}

type readHeading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
}

type readStats struct {
	CodeBlocks float64 `json:"code_blocks"`
	Tables     float64 `json:"tables"`
	Images     float64 `json:"images"`
}

func runRead(cmd *cobra.Command, args []string) error {
	jsonMode := getOutputMode() == "json"

	rawURL, err := readBrowserURLArgument(args[0])
	if err != nil {
		return emitBrowserArgumentError("read", err)
	}
	switch readOpts.Format {
	case "md", "links", "index", "json":
	default:
		ae := output.NewAgentError(output.ErrCodeInvalidArgument, "read",
			fmt.Sprintf("unknown --format %q", readOpts.Format),
			"use one of: md, links, index, json")
		return output.EmitAgentError(os.Stdout, ae, jsonMode)
	}

	doc, cached, err := loadOrExtract(cmd, rawURL)
	if err != nil {
		return err
	}
	return emitRead(doc, cached, jsonMode)
}

func loadOrExtract(cmd *cobra.Command, rawURL string) (*readDocument, bool, error) {
	cachePath, cacheErr := readCachePath(rawURL)
	if cacheErr == nil && !readOpts.Refresh && readOpts.TabID < 0 {
		if buf, err := os.ReadFile(cachePath); err == nil {
			var doc readDocument
			if err := sonic.Unmarshal(buf, &doc); err == nil && doc.Chars > 0 {
				return &doc, true, nil
			}
		}
	}

	doc, err := extractViaBrowser(cmd, rawURL)
	if err != nil {
		return nil, false, err
	}
	// A zero-text extraction is never cached: caching it would poison every
	// later read of the same URL with a permanent empty success.
	if cacheErr == nil && doc.Chars > 0 {
		if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err == nil {
			if buf, err := sonic.Marshal(doc); err == nil {
				tmp := cachePath + ".tmp"
				if os.WriteFile(tmp, buf, 0o600) == nil {
					_ = os.Rename(tmp, cachePath)
				}
			}
		}
	}
	return doc, false, nil
}

func readCachePath(rawURL string) (string, error) {
	dir, err := store.GetStorePath()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(rawURL))
	return filepath.Join(dir, "read", hex.EncodeToString(sum[:8])+".json"), nil
}

func extractViaBrowser(cmd *cobra.Command, rawURL string) (*readDocument, error) {
	ctx, cancel := context.WithTimeout(cmd.Context(), readOpts.Timeout+10*time.Second)
	defer cancel()

	client, err := selectBrowserBridge(ctx, browserSelector, "read")
	if err != nil {
		return nil, err
	}

	tabID := readOpts.TabID
	ownTab := false
	if tabID < 0 {
		// browser.create navigates WITHOUT sealing a network capture, so a
		// heavy page stays readable when a capture byte budget would abort.
		var created map[string]interface{}
		if err := client.Call(ctx, "browser.create", map[string]interface{}{
			"url": rawURL, "active": false,
		}, &created); err != nil {
			return nil, emitBrowserCallError("read", err)
		}
		id, ok := browserTabIDOf(created)
		if !ok {
			ae := output.NewAgentError(output.ErrCodeInternal, "read",
				"browser returned no tab id for the new tab",
				"rep browser status --browser arc -j  # confirm the bridge is connected")
			return nil, output.EmitAgentError(os.Stdout, ae, getOutputMode() == "json")
		}
		tabID, ownTab = id, true
	}
	if ownTab && !readOpts.KeepTab {
		defer func() {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer closeCancel()
			var ignored map[string]interface{}
			_ = client.Call(closeCtx, "browser.close", map[string]interface{}{"tab_id": tabID}, &ignored)
		}()
	}

	if err := waitForReadable(ctx, client, tabID); err != nil {
		return nil, err
	}

	value, err := evalForString(ctx, client, tabID, readabilityJS)
	if err != nil {
		return nil, err
	}
	var doc readDocument
	if err := sonic.Unmarshal([]byte(value), &doc); err != nil {
		ae := output.WrapError(err, output.ErrCodeInternal, "read",
			"the page returned an unparsable extraction result")
		return nil, output.EmitAgentError(os.Stdout, ae, getOutputMode() == "json")
	}
	doc.RequestedURL = rawURL
	doc.Redirected = doc.URL != "" && doc.URL != rawURL
	sum := sha256.Sum256([]byte(doc.Text))
	doc.ContentSHA = hex.EncodeToString(sum[:])
	return &doc, nil
}

// waitForReadable polls until the document reports complete and its rendered
// text length stops growing. Client-rendered pages finish loading well after
// readyState flips, so a readiness wait beats a fixed sleep.
func waitForReadable(ctx context.Context, client *bridge.Client, tabID int) error {
	const probe = `JSON.stringify({rs:document.readyState,len:(document.body&&document.body.innerText||"").length})`
	stable, last := 0, -1
	deadline := time.Now().Add(readOpts.Timeout)
	for time.Now().Before(deadline) {
		value, err := evalForString(ctx, client, tabID, probe)
		if err != nil {
			return err
		}
		var state struct {
			RS  string `json:"rs"`
			Len int    `json:"len"`
		}
		if err := sonic.Unmarshal([]byte(value), &state); err == nil {
			if state.RS == "complete" && state.Len > 0 && state.Len == last {
				stable++
				if stable >= 2 {
					return nil
				}
			} else {
				stable = 0
			}
			last = state.Len
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(400 * time.Millisecond):
		}
	}
	return nil // soft timeout: extract whatever rendered
}

func evalForString(ctx context.Context, client *bridge.Client, tabID int, expression string) (string, error) {
	params := map[string]interface{}{
		"tab_id": tabID, "expression": expression,
		"await_promise": true, "return_by_value": true,
		"user_gesture": false, "repl_mode": false, "keep_attached": true,
	}
	var result map[string]interface{}
	if err := client.Call(ctx, "browser.eval", params, &result); err != nil {
		return "", emitBrowserCallError("read", err)
	}
	value, ok := evalResultString(result)
	if !ok {
		return "", fmt.Errorf("evaluation returned no string value")
	}
	return value, nil
}

func evalResultString(result map[string]interface{}) (string, bool) {
	outer, ok := result["result"].(map[string]interface{})
	if !ok {
		return "", false
	}
	inner, ok := outer["result"].(map[string]interface{})
	if !ok {
		return "", false
	}
	value, ok := inner["value"].(string)
	return value, ok
}

func browserTabIDOf(result map[string]interface{}) (int, bool) {
	switch v := result["tab_id"].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	}
	return 0, false
}

func emitRead(doc *readDocument, cached bool, jsonMode bool) error {
	switch readOpts.Format {
	case "index":
		return emitReadIndex(doc, cached, jsonMode)
	case "links":
		return emitReadLinks(doc, cached, jsonMode)
	}

	body := doc.Text
	sectionApplied := false
	if strings.TrimSpace(readOpts.Section) != "" {
		if part, ok := sliceSection(doc.Text, readOpts.Section); ok {
			body, sectionApplied = part, true
		} else {
			ae := output.NewAgentError(output.ErrCodeInvalidArgument, "read",
				fmt.Sprintf("no heading matched %q", readOpts.Section),
				"rep read <url> --format index  # list the available headings")
			return output.EmitAgentError(os.Stdout, ae, jsonMode)
		}
	}

	total := len(body)
	start := readOpts.Offset
	if start > total {
		start = total
	}
	end := start + readOpts.MaxBytes
	if end > total {
		end = total
	}
	chunk := body[start:end]

	if readOpts.Format == "json" || jsonMode {
		env := output.WrapData("read", doc.URL, map[string]interface{}{
			"url": doc.URL, "requested_url": doc.RequestedURL, "redirected": doc.Redirected,
			"title": doc.Title, "fetched_at": doc.FetchedAt, "cached": cached,
			"content_sha256": doc.ContentSHA, "stats": doc.Stats,
			"section": readOpts.Section, "text": chunk,
			"range": map[string]int{"offset": start, "returned": len(chunk), "total": total},
		})
		if end < total {
			env.Truncation = &output.TruncationInfo{
				Reason: "byte-bounded", Returned: len(chunk), Total: total,
			}
			env.Suggest = []string{
				fmt.Sprintf("rep read %s --offset %d  # next page", doc.RequestedURL, end),
				fmt.Sprintf("rep read %s --format index  # jump by section", doc.RequestedURL),
			}
		}
		buf, err := sonic.MarshalIndent(env, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(buf))
		return nil
	}

	fmt.Printf("# %s\n<%s>\n", doc.Title, doc.URL)
	if doc.Redirected {
		fmt.Printf("redirected from: %s\n", doc.RequestedURL)
	}
	scope := "document"
	if sectionApplied {
		scope = "section " + readOpts.Section
	}
	fmt.Printf("[%s %d..%d of %d chars | code=%g tables=%g images=%g | fetched %s%s]\n\n",
		scope, start, end, total, doc.Stats.CodeBlocks, doc.Stats.Tables, doc.Stats.Images,
		doc.FetchedAt, map[bool]string{true: " | cached", false: ""}[cached])
	fmt.Println(chunk)
	if end < total {
		fmt.Printf("\n[TRUNCATED — rep read %s --offset %d]\n", doc.RequestedURL, end)
	}
	return nil
}

func emitReadIndex(doc *readDocument, cached bool, jsonMode bool) error {
	if jsonMode {
		env := output.WrapData("read", doc.URL, map[string]interface{}{
			"url": doc.URL, "title": doc.Title, "chars": doc.Chars,
			"cached": cached, "headings": doc.Headings, "stats": doc.Stats,
		})
		buf, err := sonic.MarshalIndent(env, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(buf))
		return nil
	}
	fmt.Printf("# %s\n<%s>\n[%d chars, %d headings]\n\n", doc.Title, doc.URL, doc.Chars, len(doc.Headings))
	for _, h := range doc.Headings {
		fmt.Printf("%s%s\n", strings.Repeat("  ", max(0, h.Level-1)), h.Text)
	}
	return nil
}

func emitReadLinks(doc *readDocument, cached bool, jsonMode bool) error {
	if jsonMode {
		env := output.WrapData("read", doc.URL, map[string]interface{}{
			"url": doc.URL, "title": doc.Title, "cached": cached, "links": doc.Links,
		})
		buf, err := sonic.MarshalIndent(env, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(buf))
		return nil
	}
	for _, l := range doc.Links {
		fmt.Println(l)
	}
	return nil
}

// sliceSection returns the body under the first heading whose text contains
// name, stopping at the next heading of the same or higher rank.
func sliceSection(text, name string) (string, bool) {
	lines := strings.Split(text, "\n")
	want := strings.ToLower(strings.TrimSpace(name))
	startIdx, startLevel := -1, 0
	for i, line := range lines {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		level := len(line) - len(strings.TrimLeft(line, "#"))
		title := strings.ToLower(strings.TrimSpace(strings.TrimLeft(line, "# ")))
		if strings.Contains(title, want) {
			startIdx, startLevel = i, level
			break
		}
	}
	if startIdx < 0 {
		return "", false
	}
	end := len(lines)
	for i := startIdx + 1; i < len(lines); i++ {
		line := lines[i]
		if !strings.HasPrefix(line, "#") {
			continue
		}
		if level := len(line) - len(strings.TrimLeft(line, "#")); level <= startLevel {
			end = i
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines[startIdx:end], "\n")), true
}
