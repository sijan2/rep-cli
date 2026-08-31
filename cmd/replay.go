package cmd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	replaySet         []string
	replayMatrix      []string
	replayTimeout     time.Duration
	replayConcurrency int
	replayIncludeBody bool
)

var replayCmd = &cobra.Command{
	Use:   "replay <request-id>",
	Short: "Re-execute a captured request (optionally mutated) and diff the response",
	Long: `Re-run a captured request against its real URL, then diff the response.

Mutation path syntax (--set and --matrix):
  header.<NAME>=<value>   query.<NAME>=<value>   body.<KEY>=<value>

Examples:
  rep replay c84a                                  # plain replay
  rep replay c84a --set 'header.X-Debug=1'         # header mutation
  rep replay c84a --set 'body.user.email=a@b.c'    # JSON dot-path
  rep replay c84a --matrix 'body.price=[0,-1,1e9]' # parallel sweep

Use exactly one of --set or --matrix; they cannot be combined.`,
	Args: cobra.ExactArgs(1),
	RunE: runReplay,
}

func init() {
	rootCmd.AddCommand(replayCmd)
	replayCmd.Flags().StringArrayVar(&replaySet, "set", nil, "Mutation 'path=value' (repeatable)")
	replayCmd.Flags().StringArrayVar(&replayMatrix, "matrix", nil, "Matrix 'KEY=[v1,v2,v3]' — one replay per value")
	replayCmd.Flags().DurationVar(&replayTimeout, "timeout", 10*time.Second, "Per-replay timeout")
	replayCmd.Flags().IntVar(&replayConcurrency, "concurrency", 4, "Max parallel replays (matrix mode)")
	replayCmd.Flags().BoolVar(&replayIncludeBody, "include-body", false, "Include full response bodies in output")
}

type replaySummary struct {
	Status            int    `json:"status"`
	Size              int    `json:"size"`
	ContentType       string `json:"content_type,omitempty"`
	ResponseBodyPath  string `json:"response_body_path,omitempty"`
	InlineBodyPreview string `json:"inline_body_preview,omitempty"`
}

type diffReport struct {
	StatusDelta   int      `json:"status_delta"`
	SizeDelta     int      `json:"size_delta"`
	BodyDistinct  bool     `json:"body_distinct"`
	HeaderChanges []string `json:"header_changes"`
}

type mutantResult struct {
	Value        string            `json:"value"`
	Status       int               `json:"status,omitempty"`
	Size         int               `json:"size,omitempty"`
	BodyDistinct bool              `json:"body_distinct,omitempty"`
	SpillPath    string            `json:"spill_path,omitempty"`
	Error        *output.AgentError `json:"error,omitempty"`
}

func runReplay(cmd *cobra.Command, args []string) error {
	jsonMode := getOutputMode() == "json"

	if len(replaySet) > 0 && len(replayMatrix) > 0 {
		ae := output.NewAgentError(
			output.ErrCodeInvalidArgument,
			"replay",
			"cannot combine --set and --matrix in the same invocation",
			"use --set for a single replay with one or more mutations",
			"use --matrix for a parallel sweep over a single key's values",
		)
		return output.EmitAgentError(os.Stdout, ae, jsonMode)
	}
	if len(replayMatrix) > 1 {
		ae := output.NewAgentError(
			output.ErrCodeInvalidArgument,
			"replay",
			"only one --matrix may be provided per invocation",
		)
		return output.EmitAgentError(os.Stdout, ae, jsonMode)
	}

	req := lookupRequestForReplay(args[0])
	if req == nil {
		ae := output.NewAgentError(
			output.ErrCodeRequestNotFound,
			"replay",
			fmt.Sprintf("no request matched %q", args[0]),
			"rep list --line | head  # browse available IDs",
			"rep body "+args[0]+"  # also accepts >=4-char prefixes",
		)
		return output.EmitAgentError(os.Stdout, ae, jsonMode)
	}

	originalSummary := summarizeOriginal(req)

	// Matrix path
	if len(replayMatrix) == 1 {
		key, values, err := parseMatrixSpec(replayMatrix[0])
		if err != nil {
			ae := output.NewAgentError(output.ErrCodeInvalidArgument, "replay", err.Error(),
				"format: --matrix 'body.price=[0,-1,99]'")
			return output.EmitAgentError(os.Stdout, ae, jsonMode)
		}
		return runMatrix(req, originalSummary, key, values, jsonMode)
	}

	// Single path (0..N --set mutations)
	mutations, err := parseMutationSpecs(replaySet)
	if err != nil {
		ae := output.NewAgentError(output.ErrCodeInvalidArgument, "replay", err.Error(),
			"format: --set 'header.X-Debug=1' or --set 'body.user.email=a@b.c'")
		return output.EmitAgentError(os.Stdout, ae, jsonMode)
	}
	return runSingle(req, originalSummary, mutations, jsonMode)
}

// lookupRequestForReplay mirrors body.go's live -> sessions fallback chain.
func lookupRequestForReplay(id string) *store.Request {
	if livePath, err := store.GetLiveFilePath(); err == nil {
		if export, err := loadLiveExport(livePath); err == nil {
			idx := store.BuildIndex(export.Requests)
			if req := idx.GetByAny(id); req != nil {
				return req
			}
		}
	}
	s, err := store.Get()
	if err != nil {
		return nil
	}
	if req := s.GetRequestFromSessions(id); req != nil {
		return req
	}
	return findRequestByAnyID(s, id)
}

type mutation struct {
	kind  string // "header" | "query" | "body"
	key   string // header name, query key, or body-path
	value string
}

func parseMutationSpecs(specs []string) ([]mutation, error) {
	out := make([]mutation, 0, len(specs))
	for _, s := range specs {
		m, err := parseOneMutation(s)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func parseOneMutation(spec string) (mutation, error) {
	eq := strings.Index(spec, "=")
	if eq <= 0 {
		return mutation{}, fmt.Errorf("mutation %q must be path=value", spec)
	}
	path := spec[:eq]
	value := spec[eq+1:]
	dot := strings.Index(path, ".")
	if dot <= 0 {
		return mutation{}, fmt.Errorf("mutation path %q must be header.NAME / query.NAME / body.KEY", path)
	}
	kind := path[:dot]
	key := path[dot+1:]
	if key == "" {
		return mutation{}, fmt.Errorf("mutation path %q has empty key", path)
	}
	switch kind {
	case "header", "query", "body":
		return mutation{kind: kind, key: key, value: value}, nil
	default:
		return mutation{}, fmt.Errorf("unknown mutation kind %q (want header|query|body)", kind)
	}
}

func parseMatrixSpec(spec string) (string, []string, error) {
	eq := strings.Index(spec, "=")
	if eq <= 0 {
		return "", nil, fmt.Errorf("matrix %q must be KEY=[v1,v2,...]", spec)
	}
	key := spec[:eq]
	rest := strings.TrimSpace(spec[eq+1:])
	if !strings.HasPrefix(rest, "[") || !strings.HasSuffix(rest, "]") {
		return "", nil, fmt.Errorf("matrix values %q must be wrapped in [ ]", rest)
	}
	inner := strings.TrimSpace(rest[1 : len(rest)-1])
	if inner == "" {
		return "", nil, fmt.Errorf("matrix value list is empty")
	}
	raw := strings.Split(inner, ",")
	values := make([]string, 0, len(raw))
	for _, v := range raw {
		values = append(values, strings.TrimSpace(v))
	}
	// Validate key shape via parseOneMutation (with a dummy value).
	if _, err := parseOneMutation(key + "=x"); err != nil {
		return "", nil, err
	}
	return key, values, nil
}

// applyMutations returns a mutated copy; never touches the store's request.
func applyMutations(src *store.Request, muts []mutation) (*store.Request, error) {
	out := &store.Request{
		ID:      src.ID,
		Method:  src.Method,
		URL:     src.URL,
		Body:    src.Body,
		Headers: cloneHeaders(src.Headers),
	}
	if src.Response != nil {
		out.Response = src.Response
	}
	for _, m := range muts {
		switch m.kind {
		case "header":
			out.Headers[m.key] = []string{m.value}
		case "query":
			newURL, err := setQueryParam(out.URL, m.key, m.value)
			if err != nil {
				return nil, fmt.Errorf("query.%s: %w", m.key, err)
			}
			out.URL = newURL
		case "body":
			newBody, err := setBodyField(out.Body, store.HeaderFirst(out.Headers, "content-type"), m.key, m.value)
			if err != nil {
				return nil, fmt.Errorf("body.%s: %w", m.key, err)
			}
			out.Body = newBody
		}
	}
	return out, nil
}

func cloneHeaders(h store.HeaderMap) store.HeaderMap {
	out := make(store.HeaderMap, len(h))
	for k, v := range h {
		dup := make([]string, len(v))
		copy(dup, v)
		out[k] = dup
	}
	return out
}

func setQueryParam(raw, key, value string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func setBodyField(body, contentType, key, value string) (string, error) {
	ct := strings.ToLower(contentType)
	// urlencoded
	if strings.Contains(ct, "application/x-www-form-urlencoded") {
		vals, err := url.ParseQuery(body)
		if err != nil {
			return "", fmt.Errorf("parse urlencoded body: %w", err)
		}
		vals.Set(key, value)
		return vals.Encode(), nil
	}
	// JSON (default when ambiguous, per spec)
	if body == "" || strings.Contains(ct, "json") || looksLikeJSON(body) {
		var root interface{}
		if body == "" {
			root = map[string]interface{}{}
		} else if err := sonic.Unmarshal([]byte(body), &root); err != nil {
			return "", fmt.Errorf("parse JSON body: %w", err)
		}
		obj, ok := root.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("JSON body is not an object; cannot set %q", key)
		}
		if err := setDotPath(obj, key, value); err != nil {
			return "", err
		}
		buf, err := sonic.Marshal(obj)
		if err != nil {
			return "", err
		}
		return string(buf), nil
	}
	return "", fmt.Errorf("unsupported body content-type %q for body mutation", contentType)
}

func looksLikeJSON(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")
}

func setDotPath(root map[string]interface{}, path, value string) error {
	parts := strings.Split(path, ".")
	cur := root
	for i, p := range parts {
		if p == "" {
			return fmt.Errorf("empty segment in path %q", path)
		}
		if i == len(parts)-1 {
			cur[p] = value
			return nil
		}
		next, ok := cur[p]
		if !ok {
			created := map[string]interface{}{}
			cur[p] = created
			cur = created
			continue
		}
		m, ok := next.(map[string]interface{})
		if !ok {
			return fmt.Errorf("path %q: segment %q is not an object", path, p)
		}
		cur = m
	}
	return nil
}

type replayResult struct {
	Status      int
	Body        []byte
	ContentType string
	Headers     http.Header
}

func executeReplay(ctx context.Context, req *store.Request) (*replayResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, strings.NewReader(req.Body))
	if err != nil {
		return nil, err
	}
	copyHeadersToRequest(req.Headers, httpReq)

	client := &http.Client{Timeout: replayTimeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Cap read at 16MB to prevent a pathological response from blowing up the agent.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if err != nil {
		return nil, err
	}
	return &replayResult{
		Status:      resp.StatusCode,
		Body:        body,
		ContentType: resp.Header.Get("Content-Type"),
		Headers:     resp.Header,
	}, nil
}

// copyHeadersToRequest writes stored headers onto the outbound request while
// dropping ones the net/http client must recompute or that were never meant
// to cross the wire (HTTP/2 pseudo-headers, for instance).
func copyHeadersToRequest(src store.HeaderMap, httpReq *http.Request) {
	skip := map[string]bool{
		"host":           true,
		"content-length": true,
		"cookie":         true, // rebuilt below
	}
	for key, values := range src {
		lower := strings.ToLower(key)
		if strings.HasPrefix(key, ":") { // HTTP/2 pseudo-headers
			continue
		}
		if skip[lower] {
			continue
		}
		for _, v := range values {
			httpReq.Header.Add(key, v)
		}
	}
	// Rebuild Cookie from the original header verbatim — preserves all cookies
	// without parsing into http.Cookie structs (which would drop unknown attrs).
	if ck := store.HeaderFirst(src, "cookie"); ck != "" {
		httpReq.Header.Set("Cookie", ck)
	}
}

func runSingle(origReq *store.Request, orig replaySummary, muts []mutation, jsonMode bool) error {
	mutated, err := applyMutations(origReq, muts)
	if err != nil {
		ae := output.NewAgentError(output.ErrCodeInvalidArgument, "replay", err.Error())
		return output.EmitAgentError(os.Stdout, ae, jsonMode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), replayTimeout)
	defer cancel()

	res, err := executeReplay(ctx, mutated)
	if err != nil {
		ae := output.WrapError(err, output.ErrCodeInternal, "replay",
			"check target reachability / TLS / timeout")
		return output.EmitAgentError(os.Stdout, ae, jsonMode)
	}

	replay, spillPath := buildReplaySummary(origReq.ID, "base", res)
	diff := computeReplayDiff(origReq, res)

	data := map[string]interface{}{
		"request_id": origReq.ID,
		"original":   orig,
		"replay":     replay,
		"diff":       diff,
	}
	if replayIncludeBody {
		data["replay_body"] = string(res.Body)
	}
	if spillPath != "" {
		data["spill_path"] = spillPath
	}
	env := output.WrapData("replay", "live.json", data)
	return emitReplayEnvelope(env, jsonMode)
}

func runMatrix(origReq *store.Request, orig replaySummary, key string, values []string, jsonMode bool) error {
	concurrency := replayConcurrency
	if concurrency < 1 {
		concurrency = 1
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	results := make([]mutantResult, len(values))

	for i, v := range values {
		i, v := i, v
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = runOneMutant(origReq, key, v)
		}()
	}
	wg.Wait()

	data := map[string]interface{}{
		"request_id": origReq.ID,
		"matrix_key": key,
		"original":   orig,
		"mutants":    results,
	}
	env := output.WrapData("replay", "live.json", data)
	return emitReplayEnvelope(env, jsonMode)
}

func runOneMutant(origReq *store.Request, key, value string) mutantResult {
	mr := mutantResult{Value: value}

	mut, err := parseOneMutation(key + "=" + value)
	if err != nil {
		ae := output.NewAgentError(output.ErrCodeInvalidArgument, "replay", err.Error())
		mr.Error = &ae
		return mr
	}

	mutated, err := applyMutations(origReq, []mutation{mut})
	if err != nil {
		ae := output.NewAgentError(output.ErrCodeInvalidArgument, "replay", err.Error())
		mr.Error = &ae
		return mr
	}

	ctx, cancel := context.WithTimeout(context.Background(), replayTimeout)
	defer cancel()

	res, err := executeReplay(ctx, mutated)
	if err != nil {
		ae := output.WrapError(err, output.ErrCodeInternal, "replay")
		mr.Error = &ae
		return mr
	}

	_, spillPath := buildReplaySummary(origReq.ID, value, res)
	mr.Status = res.Status
	mr.Size = len(res.Body)
	mr.BodyDistinct = bodyDistinct(origRespBody(origReq), res.Body)
	mr.SpillPath = spillPath
	return mr
}

func summarizeOriginal(req *store.Request) replaySummary {
	s := replaySummary{}
	if req.Response != nil {
		s.Status = req.Response.Status
		s.Size = len(req.Response.Body)
		s.ContentType = store.HeaderFirst(req.Response.Headers, "content-type")
	}
	return s
}

func origRespBody(req *store.Request) []byte {
	if req.Response == nil {
		return nil
	}
	return []byte(req.Response.Body)
}

// buildReplaySummary fills the inline preview + spill path given a response.
// Always spills when body > OverflowPreviewSize so the envelope stays small.
func buildReplaySummary(reqID, tag string, res *replayResult) (replaySummary, string) {
	s := replaySummary{
		Status:      res.Status,
		Size:        len(res.Body),
		ContentType: res.ContentType,
	}
	if len(res.Body) == 0 {
		return s, ""
	}
	if len(res.Body) > output.OverflowPreviewSize {
		spillPath := writeSpill(reqID, tag, res.ContentType, res.Body)
		s.ResponseBodyPath = spillPath
		previewLen := output.OverflowPreviewSize
		if previewLen > len(res.Body) {
			previewLen = len(res.Body)
		}
		s.InlineBodyPreview = string(res.Body[:previewLen])
		return s, spillPath
	}
	s.InlineBodyPreview = string(res.Body)
	return s, ""
}

func writeSpill(reqID, tag, contentType string, body []byte) string {
	dir := os.Getenv("REP_SPILL_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	ext := replayExtFor(contentType)
	safeTag := sanitizeTag(tag)
	name := fmt.Sprintf("rep-replay-%s-%s%s", sanitizeTag(reqID), safeTag, ext)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0600); err != nil {
		return ""
	}
	return path
}

func sanitizeTag(s string) string {
	if s == "" {
		return "x"
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.'
		if ok {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "x"
	}
	return string(out)
}

func replayExtFor(contentType string) string {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "json"):
		return ".json"
	case strings.Contains(ct, "html"):
		return ".html"
	case strings.Contains(ct, "xml"):
		return ".xml"
	case strings.Contains(ct, "javascript"), strings.Contains(ct, "ecmascript"):
		return ".js"
	case strings.Contains(ct, "css"):
		return ".css"
	case strings.Contains(ct, "text/"):
		return ".txt"
	default:
		return ".bin"
	}
}

// computeReplayDiff produces the shape documented in the replay envelope spec.
func computeReplayDiff(origReq *store.Request, res *replayResult) diffReport {
	var origStatus, origSize int
	var origHeaders store.HeaderMap
	if origReq.Response != nil {
		origStatus = origReq.Response.Status
		origSize = len(origReq.Response.Body)
		origHeaders = origReq.Response.Headers
	}
	return diffReport{
		StatusDelta:   res.Status - origStatus,
		SizeDelta:     len(res.Body) - origSize,
		BodyDistinct:  bodyDistinct(origRespBody(origReq), res.Body),
		HeaderChanges: headerChanges(origHeaders, res.Headers),
	}
}

// bodyDistinct compares the first 4KB of two bodies after whitespace
// normalization. Small perf cost, big signal-to-noise win.
func bodyDistinct(a, b []byte) bool {
	const window = 4096
	if len(a) > window {
		a = a[:window]
	}
	if len(b) > window {
		b = b[:window]
	}
	return normalizeWS(string(a)) != normalizeWS(string(b))
}

func normalizeWS(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// headerChanges returns sorted "+name" for headers only in the replay and
// "-name" for headers only in the original. Case-insensitive match, canonical
// lowercase output.
func headerChanges(orig store.HeaderMap, replay http.Header) []string {
	origSet := map[string]bool{}
	for k := range orig {
		origSet[strings.ToLower(k)] = true
	}
	newSet := map[string]bool{}
	for k := range replay {
		newSet[strings.ToLower(k)] = true
	}
	var changes []string
	for k := range newSet {
		if !origSet[k] {
			changes = append(changes, "+"+k)
		}
	}
	for k := range origSet {
		if !newSet[k] {
			changes = append(changes, "-"+k)
		}
	}
	sort.Strings(changes)
	return changes
}

func emitReplayEnvelope(env output.AgentEnvelope, jsonMode bool) error {
	// Keep inline output under OverflowThreshold. If the envelope blows past
	// that (big matrix with inline previews), drop previews and keep spill
	// paths — the agent can still read the spill files.
	buf, err := sonic.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	if len(buf) > output.OverflowThreshold {
		buf = trimEnvelopePreviews(env, jsonMode)
	}

	if jsonMode {
		fmt.Println(string(buf))
		return nil
	}
	// Plain-text: a compact human rendering on top of the JSON body.
	printReplayText(env)
	return nil
}

// trimEnvelopePreviews re-marshals the envelope with inline previews stripped.
// Best-effort; returns the shrunken JSON bytes either way.
func trimEnvelopePreviews(env output.AgentEnvelope, jsonMode bool) []byte {
	_ = jsonMode
	if data, ok := env.Data.(map[string]interface{}); ok {
		if r, ok := data["replay"].(replaySummary); ok {
			r.InlineBodyPreview = ""
			data["replay"] = r
		}
	}
	buf, _ := sonic.MarshalIndent(env, "", "  ")
	return buf
}

func printReplayText(env output.AgentEnvelope) {
	data, ok := env.Data.(map[string]interface{})
	if !ok {
		buf, _ := sonic.MarshalIndent(env, "", "  ")
		fmt.Println(string(buf))
		return
	}
	fmt.Printf("replay %v\n", data["request_id"])
	if orig, ok := data["original"].(replaySummary); ok {
		fmt.Printf("  original: %d %s (%d bytes)\n", orig.Status, orig.ContentType, orig.Size)
	}
	if r, ok := data["replay"].(replaySummary); ok {
		fmt.Printf("  replay:   %d %s (%d bytes)\n", r.Status, r.ContentType, r.Size)
		if r.ResponseBodyPath != "" {
			fmt.Printf("  body:     %s\n", r.ResponseBodyPath)
		}
	}
	if d, ok := data["diff"].(diffReport); ok {
		fmt.Printf("  diff:     status=%+d size=%+d body_distinct=%v headers=%v\n",
			d.StatusDelta, d.SizeDelta, d.BodyDistinct, d.HeaderChanges)
	}
	if muts, ok := data["mutants"].([]mutantResult); ok {
		fmt.Printf("  mutants (%d):\n", len(muts))
		for _, m := range muts {
			if m.Error != nil {
				fmt.Printf("    %q: ERROR %s: %s\n", m.Value, m.Error.Code, m.Error.Message)
				continue
			}
			fmt.Printf("    %q: %d (%d bytes, distinct=%v) %s\n",
				m.Value, m.Status, m.Size, m.BodyDistinct, m.SpillPath)
		}
	}
}
