package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
	replayAs          []string
	replayBaseline    int
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

Use exactly one of --set or --matrix; they cannot be combined.

Replay uses Go's HTTP client, not a browser session. Body comparisons use all
retained bytes exactly; incomplete or unknown evidence is inconclusive.
Use rep describe replay for the comparison and transport fidelity contract.`,
	Args: cobra.ExactArgs(1),
	RunE: runReplay,
}

func init() {
	rootCmd.AddCommand(replayCmd)
	replayCmd.Flags().StringArrayVar(&replaySet, "set", nil, "Mutation 'path=value' (repeatable)")
	replayCmd.Flags().StringArrayVar(&replayMatrix, "matrix", nil, "Matrix 'KEY=[v1,v2,v3]' — one replay per value")
	replayCmd.Flags().DurationVar(&replayTimeout, "timeout", 10*time.Second, "Per-replay timeout")
	replayCmd.Flags().IntVar(&replayConcurrency, "concurrency", 4, "Max parallel replays (matrix mode)")
	replayCmd.Flags().BoolVar(&replayIncludeBody, "include-body", false, "Include retained response bytes in output (up to 16 MiB per response)")
	advancedFlags(replayCmd, "include-body")
	replayCmd.Flags().StringSliceVar(&replayAs, "as", nil, "Replay as named principals, e.g. --as alice,bob,anon (see rep identity)")
	replayCmd.Flags().IntVar(&replayBaseline, "baseline", 0, "Replay the unmutated request N times first to learn which fields vary on their own")
}

const maxReplayBodyBytes = 16 * 1024 * 1024

type replaySummary struct {
	Status                    int               `json:"status"`
	Size                      int               `json:"size"`
	ContentType               string            `json:"content_type,omitempty"`
	BodyCapture               store.BodyCapture `json:"body_capture"`
	BodyError                 string            `json:"body_error,omitempty"`
	BodyTruncated             bool              `json:"body_truncated,omitempty"`
	HTTPProtocol              string            `json:"http_protocol,omitempty"`
	FinalURL                  string            `json:"final_url,omitempty"`
	AutomaticallyDecompressed bool              `json:"automatically_decompressed,omitempty"`
	ResponseBodyPath          string            `json:"response_body_path,omitempty"`
	ArtifactError             string            `json:"artifact_error,omitempty"`
	InlineBodyPreview         string            `json:"inline_body_preview,omitempty"`
	InlineBodyEncoding        string            `json:"inline_body_encoding,omitempty"`
}

type replayBodyComparison struct {
	State         string `json:"state"` // equal, different, or inconclusive
	Method        string `json:"method"`
	OriginalState string `json:"original_state"`
	ReplayState   string `json:"replay_state"`
	Reason        string `json:"reason,omitempty"`
}

type diffReport struct {
	StatusDelta    int                  `json:"status_delta"`
	SizeDelta      *int                 `json:"size_delta"`
	BodyDistinct   *bool                `json:"body_distinct"`
	BodyComparison replayBodyComparison `json:"body_comparison"`
	HeaderChanges  []string             `json:"header_changes"`
}

type mutantResult struct {
	Value                     string                `json:"value"`
	Status                    int                   `json:"status,omitempty"`
	Size                      int                   `json:"size,omitempty"`
	BodyDistinct              *bool                 `json:"body_distinct"`
	BodyComparison            *replayBodyComparison `json:"body_comparison,omitempty"`
	BodyCapture               *store.BodyCapture    `json:"body_capture,omitempty"`
	BodyError                 string                `json:"body_error,omitempty"`
	BodyTruncated             bool                  `json:"body_truncated,omitempty"`
	HTTPProtocol              string                `json:"http_protocol,omitempty"`
	FinalURL                  string                `json:"final_url,omitempty"`
	AutomaticallyDecompressed bool                  `json:"automatically_decompressed,omitempty"`
	SpillPath                 string                `json:"spill_path,omitempty"`
	ArtifactError             string                `json:"artifact_error,omitempty"`
	Error                     *output.AgentError    `json:"error,omitempty"`
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

	// Differential path: identity comparison and/or baseline-masked judging.
	if len(replayAs) > 0 || replayBaseline > 0 {
		if len(replayMatrix) > 0 {
			ae := output.NewAgentError(output.ErrCodeInvalidArgument, "replay",
				"--matrix cannot be combined with --as or --baseline",
				"run the matrix sweep separately, then re-test the interesting value with --baseline")
			return output.EmitAgentError(os.Stdout, ae, jsonMode)
		}
		mutations, err := parseMutationSpecs(replaySet)
		if err != nil {
			ae := output.NewAgentError(output.ErrCodeInvalidArgument, "replay", err.Error())
			return output.EmitAgentError(os.Stdout, ae, jsonMode)
		}
		return runDifferential(req, mutations, jsonMode)
	}

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
	Status                    int
	Body                      []byte
	ContentType               string
	Headers                   http.Header
	BodyCapture               store.BodyCapture
	BodyError                 string
	BodyTruncated             bool
	HTTPProtocol              string
	FinalURL                  string
	AutomaticallyDecompressed bool
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

	// A sentinel byte distinguishes an exact-limit response from a truncated
	// one. Keep bytes and headers even when the body ends with a read error.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxReplayBodyBytes+1))
	observedBytes := int64(len(body))
	res := &replayResult{
		Status:                    resp.StatusCode,
		ContentType:               resp.Header.Get("Content-Type"),
		Headers:                   resp.Header,
		HTTPProtocol:              resp.Proto,
		FinalURL:                  resp.Request.URL.String(),
		AutomaticallyDecompressed: resp.Uncompressed,
		BodyCapture: store.BodyCapture{
			State:         "complete",
			Source:        "go-http-client",
			ObservedBytes: &observedBytes,
		},
	}
	if len(body) > maxReplayBodyBytes {
		body = body[:maxReplayBodyBytes]
		res.BodyTruncated = true
		res.BodyCapture.State = "partial"
		res.BodyCapture.Reason = "replay_body_limit"
	}
	if readErr != nil {
		res.BodyCapture.State = "partial"
		if res.BodyCapture.Reason == "" {
			res.BodyCapture.Reason = "response_body_read_failed"
		}
		res.BodyError = readErr.Error()
	}
	res.Body = body
	res.BodyCapture.CapturedBytes = int64(len(body))
	digest := sha256.Sum256(body)
	res.BodyCapture.SHA256 = hex.EncodeToString(digest[:])
	return res, nil
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

	replay, spillPath := buildReplaySummary(res)
	diff := computeReplayDiff(origReq, res)

	data := map[string]interface{}{
		"request_id": origReq.ID,
		"original":   orig,
		"replay":     replay,
		"diff":       diff,
		"fidelity":   replayFidelity(),
	}
	if replayIncludeBody {
		data["replay_body"], data["replay_body_encoding"] = encodeReplayBody(res.Body)
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
		"fidelity":   replayFidelity(),
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

	summary, spillPath := buildReplaySummary(res)
	mr.Status = res.Status
	mr.Size = len(res.Body)
	diff := computeReplayDiff(origReq, res)
	mr.BodyDistinct = diff.BodyDistinct
	mr.BodyComparison = &diff.BodyComparison
	mr.BodyCapture = &res.BodyCapture
	mr.BodyError = res.BodyError
	mr.BodyTruncated = res.BodyTruncated
	mr.HTTPProtocol = res.HTTPProtocol
	mr.FinalURL = res.FinalURL
	mr.AutomaticallyDecompressed = res.AutomaticallyDecompressed
	mr.SpillPath = spillPath
	mr.ArtifactError = summary.ArtifactError
	return mr
}

func summarizeOriginal(req *store.Request) replaySummary {
	s := replaySummary{}
	if req.Response != nil {
		s.Status = req.Response.Status
		s.ContentType = store.HeaderFirst(req.Response.Headers, "content-type")
	}
	body, evidence, err := originalReplayEvidence(req)
	s.BodyCapture = evidence
	s.Size = len(body)
	s.BodyTruncated = req.ResponseBodyTruncated
	if err != nil {
		s.BodyError = err.Error()
	}
	return s
}

func originalReplayEvidence(req *store.Request) ([]byte, store.BodyCapture, error) {
	if req.Response == nil {
		return nil, store.BodyCapture{State: "unavailable", Reason: "response_not_captured"}, nil
	}
	var metadataCheck captureBodyCounts
	captureAccumulateBody(&metadataCheck, req, false)
	if metadataCheck.MetadataInvalid > 0 {
		return nil, store.BodyCapture{State: "unknown", Reason: "invalid_body_evidence"}, fmt.Errorf("captured body metadata is inconsistent")
	}
	body, _, evidence, err := bodyEvidence(req, false)
	if err != nil {
		// Corrupt encodings or metadata must not become a verified empty body.
		return nil, store.BodyCapture{State: "unknown", Reason: "invalid_body_evidence"}, err
	}
	return body, evidence, nil
}

func replayFidelity() map[string]interface{} {
	return map[string]interface{}{
		"client":                       "go-net-http",
		"browser_equivalent":           false,
		"body_read_limit_bytes":        maxReplayBodyBytes,
		"response_body_representation": "go_http_response_body",
	}
}

func encodeReplayBody(body []byte) (string, string) {
	if utf8.Valid(body) {
		return string(body), "utf8"
	}
	return base64.StdEncoding.EncodeToString(body), "base64"
}

// buildReplaySummary fills the inline preview + spill path given a response.
// Always spills when body > OverflowPreviewSize so the envelope stays small.
func buildReplaySummary(res *replayResult) (replaySummary, string) {
	s := replaySummary{
		Status:                    res.Status,
		Size:                      len(res.Body),
		ContentType:               res.ContentType,
		BodyCapture:               res.BodyCapture,
		BodyError:                 res.BodyError,
		BodyTruncated:             res.BodyTruncated,
		HTTPProtocol:              res.HTTPProtocol,
		FinalURL:                  res.FinalURL,
		AutomaticallyDecompressed: res.AutomaticallyDecompressed,
	}
	if len(res.Body) == 0 {
		return s, ""
	}
	if len(res.Body) > output.OverflowPreviewSize {
		spillPath, err := writeSpill(res.ContentType, res.Body)
		if err != nil {
			s.ArtifactError = fmt.Sprintf("save replay body: %v", err)
		}
		s.ResponseBodyPath = spillPath
		previewLen := output.OverflowPreviewSize
		if previewLen > len(res.Body) {
			previewLen = len(res.Body)
		}
		s.InlineBodyPreview, s.InlineBodyEncoding = encodeReplayBody(res.Body[:previewLen])
		return s, spillPath
	}
	s.InlineBodyPreview, s.InlineBodyEncoding = encodeReplayBody(res.Body)
	return s, ""
}

func writeSpill(contentType string, body []byte) (string, error) {
	return saveTaskArtifact("replay-exports", "replay-*"+replayExtFor(contentType), body)
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
	var origStatus int
	var origHeaders store.HeaderMap
	if origReq.Response != nil {
		origStatus = origReq.Response.Status
		origHeaders = origReq.Response.Headers
	}
	origBody, origEvidence, evidenceErr := originalReplayEvidence(origReq)
	replayState := res.BodyCapture.State
	if replayState == "" {
		replayState = "unknown"
	}
	diff := diffReport{
		StatusDelta:   res.Status - origStatus,
		HeaderChanges: headerChanges(origHeaders, res.Headers),
		BodyComparison: replayBodyComparison{
			State:         "inconclusive",
			Method:        "exact_bytes",
			OriginalState: origEvidence.State,
			ReplayState:   replayState,
		},
	}
	if evidenceErr == nil && origReq.Response != nil {
		delta := len(res.Body) - len(origBody)
		diff.SizeDelta = &delta
	}
	switch {
	case evidenceErr != nil:
		diff.BodyComparison.Reason = "invalid_original_body_evidence"
	case origEvidence.State != "complete":
		diff.BodyComparison.Reason = "original_body_not_complete"
	case replayState != "complete" || res.BodyError != "" || res.BodyTruncated:
		diff.BodyComparison.Reason = "replay_body_not_complete"
	case res.BodyCapture.CapturedBytes != int64(len(res.Body)):
		diff.BodyComparison.Reason = "invalid_replay_body_evidence"
	default:
		distinct := bodyDistinct(origBody, res.Body)
		diff.BodyDistinct = &distinct
		diff.BodyComparison.State = "equal"
		if distinct {
			diff.BodyComparison.State = "different"
		}
	}
	return diff
}

// bodyDistinct preserves every retained byte, including whitespace and data
// beyond the preview window. Completeness is checked by computeReplayDiff.
func bodyDistinct(a, b []byte) bool {
	return !bytes.Equal(a, b)
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
			r.InlineBodyEncoding = ""
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
	fmt.Printf("  client:   Go net/http (browser_equivalent=false; body limit=%d bytes)\n", maxReplayBodyBytes)
	if orig, ok := data["original"].(replaySummary); ok {
		fmt.Printf("  original: %d %s (%d retained bytes; %s)\n", orig.Status, orig.ContentType, orig.Size, orig.BodyCapture.State)
		if orig.BodyError != "" {
			fmt.Printf("  original body error: %s\n", orig.BodyError)
		}
	}
	if r, ok := data["replay"].(replaySummary); ok {
		fmt.Printf("  replay:   %d %s (%d retained bytes; %s)\n", r.Status, r.ContentType, r.Size, r.BodyCapture.State)
		if r.BodyCapture.Reason != "" {
			fmt.Printf("  body capture: %s\n", r.BodyCapture.Reason)
		}
		if r.BodyError != "" {
			fmt.Printf("  replay body error: %s\n", r.BodyError)
		}
		if r.ArtifactError != "" {
			fmt.Printf("  artifact error: %s\n", r.ArtifactError)
		}
		if r.ResponseBodyPath != "" {
			fmt.Printf("  body:     %s\n", r.ResponseBodyPath)
		}
	}
	if d, ok := data["diff"].(diffReport); ok {
		sizeDelta := "unknown"
		if d.SizeDelta != nil {
			sizeDelta = fmt.Sprintf("%+d", *d.SizeDelta)
		}
		fmt.Printf("  diff:     status=%+d retained_size=%s body=%s headers=%v\n",
			d.StatusDelta, sizeDelta, d.BodyComparison.State, d.HeaderChanges)
		if d.BodyComparison.Reason != "" {
			fmt.Printf("  comparison: %s\n", d.BodyComparison.Reason)
		}
	}
	if muts, ok := data["mutants"].([]mutantResult); ok {
		fmt.Printf("  mutants (%d):\n", len(muts))
		for _, m := range muts {
			if m.Error != nil {
				fmt.Printf("    %q: ERROR %s: %s\n", m.Value, m.Error.Code, m.Error.Message)
				continue
			}
			comparison := "inconclusive"
			if m.BodyComparison != nil {
				comparison = m.BodyComparison.State
			}
			fmt.Printf("    %q: %d (%d retained bytes, body=%s) %s\n",
				m.Value, m.Status, m.Size, comparison, m.SpillPath)
			if m.BodyError != "" {
				fmt.Printf("      body error: %s\n", m.BodyError)
			}
			if m.ArtifactError != "" {
				fmt.Printf("      artifact error: %s\n", m.ArtifactError)
			}
		}
	}
}
