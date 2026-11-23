package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/records"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

// Records artifacts hold third-party usernames and text, so they share the
// retention of other personal data under a task and are pruned at each run.
const recordsRetention = 7 * 24 * time.Hour

// The inline result stays small enough to read whole; everything else lives in
// the artifact. 16 KiB holds about 60-80 compact records.
const recordsInlineBudget = 16 << 10

type recordsOptions struct {
	Max     int
	Timeout time.Duration
	KeepTab bool
	Site    string
	URL     string
}

type recordsInput struct {
	body        []byte
	url, mime   string
	source      records.Source
	requestBody string
	requestURL  string
}

func newCoreRecordsCommand() *cobra.Command {
	options := recordsOptions{}
	command := &cobra.Command{Use: "records TARGET...", Short: "Decode thread records from a page, capture or file", Args: cobra.MinimumNArgs(1), Long: `TARGET: a thread URL, an owned tab, an s_ capture, h_ requests in merge order,
or saved response files. Only responses the page's own client loaded are decoded;
pending continuations are listed, never fetched. Zero model requests.`}
	command.Example = `  rep records t1
  rep records s_CAPTURE --max 100
  rep records ./response.json --site reddit --url https://www.reddit.com/r/example/comments/abc/title/`
	command.Flags().IntVar(&options.Max, "max", 50, "Records inline")
	command.Flags().DurationVar(&options.Timeout, "timeout", 60*time.Second, "Live budget (5s–5m)")
	command.Flags().BoolVar(&options.KeepTab, "keep-tab", false, "Keep the live tab open")
	command.Flags().StringVar(&options.Site, "site", "auto", "auto, reddit, or x")
	command.Flags().StringVar(&options.URL, "url", "", "Response URL for files")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := coreRecords(cmd, args, options); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	command.AddCommand(newCoreRecordsControlsCommand())
	return command
}

func coreRecords(command *cobra.Command, args []string, options recordsOptions) error {
	if options.Max < 0 {
		return coreArgument("--max must be nonnegative")
	}
	if options.Timeout < 5*time.Second || options.Timeout > 5*time.Minute {
		return coreArgument("timeout must be between 5s and 5m")
	}
	if options.Site != "auto" && options.Site != "reddit" && options.Site != "x" {
		return coreArgument("--site must be auto, reddit, or x")
	}
	kind := recordsTargetKind(args[0])
	for _, arg := range args[1:] {
		if other := recordsTargetKind(arg); other != kind || kind == "capture" || kind == "tab" || kind == "url" {
			return coreArgument("provide one URL, one tab, one capture, or several h_ requests or files")
		}
	}
	started := time.Now()
	var inputs []recordsInput
	var meta map[string]any
	switch kind {
	case "file":
		if options.KeepTab {
			return coreArgument("--keep-tab applies to a live read")
		}
		for _, path := range args {
			body, err := os.ReadFile(path)
			if err != nil {
				return agentstate.Failure("input_unreadable", "an input file cannot be read", coreNext("records", "--help"))
			}
			inputs = append(inputs, recordsInput{body: body, url: options.URL})
		}
		meta = map[string]any{"source": "file", "files": len(args)}
	case "request", "capture":
		if options.KeepTab || options.URL != "" {
			return coreArgument("--keep-tab and --url apply to live and file reads")
		}
		var err error
		inputs, meta, err = recordsArchiveInputs(kind, args, options.Site)
		if err != nil {
			return err
		}
	default:
		return coreRecordsLive(command, args[0], kind, options, started)
	}
	thread, err := recordsDecodeAll(inputs, options.Site, recordsFocal(options.URL))
	if err != nil {
		return err
	}
	return emitRecords(command, coreResult{}, thread, meta, options.Max, started)
}

func recordsTargetKind(arg string) string {
	switch {
	case agentstate.TabPattern.MatchString(arg):
		return "tab"
	case coreRequestHandle.MatchString(arg):
		return "request"
	case coreCaptureHandle.MatchString(arg):
		return "capture"
	case coreHTTPURL(arg):
		return "url"
	}
	return "file"
}

// recordsFocal derives the X focal status id from a status URL so a page-1
// response names its thread even when the file carries no request descriptor.
func recordsFocal(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "status" {
			return parts[i+1]
		}
	}
	return ""
}

func recordsArchiveInputs(kind string, handles []string, site string) ([]recordsInput, map[string]any, error) {
	var requests []*store.Request
	var capture agentstate.Capture
	if kind == "request" {
		for _, handle := range handles {
			found, request, err := coreDataRequest(handle)
			if err != nil {
				return nil, nil, err
			}
			if capture.ID != "" && found.ID != capture.ID {
				return nil, nil, coreArgument("requests must come from one capture")
			}
			capture = found
			requests = append(requests, request)
		}
	} else {
		found, snapshot, err := loadCoreDataCapture(handles[0])
		if err != nil {
			return nil, nil, err
		}
		capture = found
		ordered := make([]*store.Request, 0, len(snapshot.Requests))
		for i := range snapshot.Requests {
			ordered = append(ordered, &snapshot.Requests[i])
		}
		sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].StartOrdinal < ordered[j].StartOrdinal })
		// The primary response leads the merge; later fragments resolve its
		// continuations. Responses from other sites or threads are left alone.
		primary := -1
		for i, request := range ordered {
			input, err := recordsCapturedInput(request)
			if err != nil {
				continue
			}
			detected, op := records.Detect(input.url, input.mime, input.body)
			// A URL alone proves nothing about the bytes behind it: the body's
			// own shape must agree before a response leads the merge.
			shape, shapeOp := records.Detect("", input.mime, input.body)
			if detected == "" || detected != shape || (site != "auto" && detected != site) || op == "more_comments" || shapeOp == "more_comments" {
				continue
			}
			primary = i
			break
		}
		if primary < 0 {
			return nil, nil, agentstate.Failure("records_not_found", "the capture holds no complete supported thread response", coreNext("net", capture.ID))
		}
		requests = append(requests, ordered[primary])
		detectedSite, _ := records.Detect(ordered[primary].URL, "", nil)
		for i, request := range ordered {
			if i == primary {
				continue
			}
			input, err := recordsCapturedInput(request)
			if err != nil {
				continue
			}
			detected, _ := records.Detect(input.url, input.mime, input.body)
			if shape, _ := records.Detect("", input.mime, input.body); detected == detectedSite && detected != "" && shape == detected {
				requests = append(requests, request)
			}
		}
	}
	inputs := make([]recordsInput, 0, len(requests))
	ids := make([]string, 0, len(requests))
	for _, request := range requests {
		input, err := recordsCapturedInput(request)
		if err != nil {
			return nil, nil, agentstate.Failure("body_unavailable", "a selected response has no complete captured body", coreNext("body", request.ID))
		}
		inputs = append(inputs, input)
		ids = append(ids, request.ID)
	}
	return inputs, map[string]any{"source": "capture", "capture": capture.ID, "requests": ids}, nil
}

func recordsCapturedInput(request *store.Request) (recordsInput, error) {
	if request.RecordKind != "" && request.RecordKind != "http" {
		return recordsInput{}, fmt.Errorf("stream records carry no thread body")
	}
	body, mime, evidence, err := bodyEvidence(request, false)
	if err != nil || evidence.State != "complete" {
		return recordsInput{}, fmt.Errorf("body is not verified complete")
	}
	return recordsInput{body: body, url: request.URL, mime: mime, source: records.Source{RequestID: request.ID, SHA256: evidence.SHA256}, requestBody: request.Body, requestURL: request.URL}, nil
}

func recordsDecodeAll(inputs []recordsInput, site, focal string) (*records.Thread, error) {
	var thread *records.Thread
	for _, input := range inputs {
		detected, op := records.Detect(input.url, input.mime, input.body)
		selected := site
		if selected == "auto" {
			selected = detected
		}
		if selected == "" || (thread != nil && thread.Site != selected) || (detected != "" && detected != selected) {
			return nil, agentstate.Failure("unsupported_site", "input is not a supported thread response for this site", coreNext("records", "--help"))
		}
		if input.source.SHA256 == "" {
			digest := sha256.Sum256(input.body)
			input.source.SHA256 = hex.EncodeToString(digest[:])
		}
		input.source.Op = op
		var page records.Page
		var err error
		switch selected {
		case "reddit":
			page, err = records.DecodeReddit(input.body, input.source)
		case "x":
			page, err = records.DecodeXTweetDetail(input.body, input.source, focal)
		}
		if err != nil {
			return nil, agentstate.Failure("decode_failed", "a response is malformed or incomplete", coreNext("records", "--help"))
		}
		if page.Thread == nil && len(page.Records) == 0 && len(page.Continuations) == 0 && page.Class == "ok" {
			return nil, agentstate.Failure("unsupported_site", "input contains no supported thread data", coreNext("records", "--help"))
		}
		if thread == nil {
			thread = records.NewThread(page)
			if focal == "" && thread.Site == "x" && thread.Thread != nil {
				focal = thread.Thread.ID
			}
			continue
		}
		if page.Thread != nil && thread.Thread != nil && page.Thread.ID != thread.Thread.ID {
			return nil, agentstate.Failure("thread_mismatch", "a response belongs to another thread", coreNext("records", "--help"))
		}
		thread.Merge(page, recordsResponseContinuation(thread, input))
	}
	return thread, nil
}

// A response resolves a frontier item only when its own request names that
// item: Reddit by decoded path plus posted cursor, X by variables.cursor.
// Files carry no request, so they merge without resolving anything.
func recordsResponseContinuation(t *records.Thread, input recordsInput) *records.Continuation {
	if input.requestURL == "" {
		return nil
	}
	u, err := url.Parse(input.requestURL)
	if err != nil {
		return nil
	}
	var key records.ContinuationKey
	switch t.Site {
	case "reddit":
		form, err := url.ParseQuery(input.requestBody)
		if err != nil || form.Get("cursor") == "" {
			return nil
		}
		key = records.ContinuationKey{Path: u.Path, Cursor: form.Get("cursor")}
	case "x":
		var variables struct {
			Cursor string `json:"cursor"`
		}
		if json.Unmarshal([]byte(u.Query().Get("variables")), &variables) != nil || variables.Cursor == "" {
			return nil
		}
		key = records.ContinuationKey{Path: "TweetDetail", Cursor: variables.Cursor}
	default:
		return nil
	}
	for i := range t.Continuations {
		c := &t.Continuations[i]
		if c.Key == key && (c.State == "pending" || c.State == "requested") {
			return c
		}
	}
	return nil
}

type recordsInline struct {
	R       string           `json:"r"`
	ID      string           `json:"id"`
	Parent  string           `json:"parent"`
	Depth   int              `json:"depth"`
	Author  string           `json:"author"`
	Time    string           `json:"time"`
	Metrics map[string]int64 `json:"metrics"`
	Text    string           `json:"text"`
}

type recordsFrontierItem struct {
	Kind      string `json:"kind"`
	CountHint int    `json:"count_hint"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
}

type recordsFrontierText struct {
	Parent string `json:"parent"`
	Label  string `json:"label,omitempty"`
}

type recordsUntrusted struct {
	Thread   *recordsInline        `json:"thread"`
	Records  []recordsInline       `json:"records"`
	Frontier []recordsFrontierText `json:"frontier"`
}

func recordsPrefix(text string, limit int) string {
	text = strings.ToValidUTF8(text, "�")
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

func recordsInlineRecord(r records.Record, truncated *bool) recordsInline {
	v := recordsInline{R: recordsPrefix(r.R, 256), ID: recordsPrefix(r.ID, 256), Parent: recordsPrefix(r.Parent, 256), Depth: r.Depth, Author: recordsPrefix(r.Author, 256), Time: r.Time, Metrics: r.Metrics, Text: recordsPrefix(r.Text, 200)}
	if v.R != r.R || v.ID != r.ID || v.Parent != r.Parent || v.Author != r.Author || v.Text != r.Text {
		*truncated = true
	}
	return v
}

// emitRecords writes the full thread to a private artifact and prints a bounded
// summary. Coverage is never truncated; records and frontier previews shrink.
func emitRecords(command *cobra.Command, result coreResult, thread *records.Thread, meta map[string]any, maximum int, started time.Time) error {
	coverage := records.Check(thread, records.DefaultContracts())
	ordered := thread.Ordered()
	artifact, err := writeRecordsArtifact(thread, ordered, coverage, meta)
	if err != nil {
		return err
	}
	truncated := false
	untrusted := recordsUntrusted{Records: []recordsInline{}, Frontier: []recordsFrontierText{}}
	if thread.Thread != nil {
		v := recordsInlineRecord(*thread.Thread, &truncated)
		untrusted.Thread = &v
	}
	for _, r := range ordered[:min(maximum, len(ordered))] {
		untrusted.Records = append(untrusted.Records, recordsInlineRecord(r, &truncated))
	}
	frontier := []recordsFrontierItem{}
	for _, f := range thread.Continuations[:min(20, len(thread.Continuations))] {
		frontier = append(frontier, recordsFrontierItem{Kind: f.Kind, CountHint: f.CountHint, State: f.State, Reason: f.Reason})
		text := recordsFrontierText{Parent: recordsPrefix(f.Parent, 256), Label: recordsPrefix(f.Label, 200)}
		truncated = truncated || text.Parent != f.Parent || text.Label != f.Label
		untrusted.Frontier = append(untrusted.Frontier, text)
	}
	truncated = truncated || len(ordered) > maximum || len(thread.Continuations) > 20
	if meta == nil {
		meta = map[string]any{}
	}
	meta["site"], meta["op"], meta["coverage"] = thread.Site, thread.Op, coverage
	meta["frontier"] = map[string]any{"total": len(thread.Continuations), "items": frontier}
	meta["dropped"], meta["model_requests"], meta["artifact"] = coverage.Dropped, 0, artifact
	timing, _ := meta["timing_ms"].(map[string]int64)
	if timing == nil {
		timing = map[string]int64{}
	}
	timing["total"] = time.Since(started).Milliseconds()
	meta["timing_ms"] = timing
	result.Status, result.Complete, result.Meta = coverage.Status, boolPointer(coverage.Status == "complete"), meta
	// Shrink the record preview until the whole result fits the inline budget.
	for {
		result.Untrusted = untrusted
		meta["truncated"] = truncated
		encoded, err := output.Marshal(result)
		if err != nil {
			return err
		}
		if len(encoded) <= recordsInlineBudget || len(untrusted.Records) == 0 {
			break
		}
		untrusted.Records = untrusted.Records[:len(untrusted.Records)/2]
		truncated = true
	}
	return emitCore(command, result)
}

func writeRecordsArtifact(thread *records.Thread, ordered []records.Record, coverage records.Coverage, meta map[string]any) (map[string]any, error) {
	selected, err := scope.Current()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(selected.DataDir, "records")
	pruneRecordsArtifacts(root, time.Now())
	dir := filepath.Join(root, agentstate.Token("rd_"))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	var lines strings.Builder
	encoder := json.NewEncoder(&lines)
	for _, r := range ordered {
		if err := encoder.Encode(r); err != nil {
			return nil, err
		}
	}
	written, err := coreArtifact(filepath.Join(dir, "records.ndjson"), ".ndjson", []byte(lines.String()))
	if err != nil {
		return nil, err
	}
	manifest := map[string]any{"version": 1, "created": time.Now().UTC().Format(time.RFC3339), "site": thread.Site, "op": thread.Op, "thread": thread.Thread, "coverage": coverage, "frontier": thread.Continuations, "dropped": thread.Dropped, "expected": thread.Expected, "class": thread.Class, "source": meta, "records": written}
	encoded, err := json.MarshalIndent(manifest, "", " ")
	if err != nil {
		return nil, err
	}
	if _, err := coreArtifact(filepath.Join(dir, "manifest.json"), ".json", encoded); err != nil {
		return nil, err
	}
	written["dir"] = dir
	written["records"] = len(ordered)
	return written, nil
}

// pruneRecordsArtifacts removes this task's expired rd_ directories. Other
// tasks' directories are never visited.
func pruneRecordsArtifacts(root string, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "rd_") {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < recordsRetention {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, entry.Name()))
	}
}

func init() { rootCmd.AddCommand(newCoreRecordsCommand()) }
