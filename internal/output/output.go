package output

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/store"
)

const (
	OverflowThreshold   = 96 * 1024
	OverflowPreviewSize = 8 * 1024
)

type TruncationInfo struct {
	Reason       string `json:"reason"`
	Returned     int    `json:"returned"`
	Total        int    `json:"total"`
	OverflowPath string `json:"overflow_path,omitempty"`
}

var binaryContentTypes = []string{
	"image/", "video/", "audio/", "font/", "application/octet-stream",
	"application/pdf", "application/zip", "application/gzip", "application/x-tar",
	"application/x-rar", "application/wasm",
}

func IsBinaryContentType(contentType string) bool {
	ct := strings.ToLower(contentType)
	for _, prefix := range binaryContentTypes {
		if strings.HasPrefix(ct, prefix) || strings.Contains(ct, prefix) {
			return true
		}
	}
	return false
}

func SanitizeText(input string) string {
	return strings.ReplaceAll(input, "\x00", "\\x00")
}

func FormatBodySize(size int) string {
	switch {
	case size < 1024:
		return fmt.Sprintf("%dB", size)
	case size < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(size)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(size)/(1024*1024))
	}
}

func TruncateBodyInfo(body, contentType string, cfg store.TruncateConfig) (string, TruncationInfo) {
	info := TruncationInfo{Reason: "none", Returned: len(body), Total: len(body)}
	if cfg.BinaryAsLabel && IsBinaryContentType(contentType) {
		label := fmt.Sprintf("[BINARY: %s %s]", FormatBodySize(len(body)), contentType)
		info.Reason = "binary-label"
		info.Returned = len(label)
		return label, info
	}
	if cfg.MaxBodySize <= 0 || len(body) <= cfg.MaxBodySize {
		return body, info
	}
	truncated := body[:cfg.MaxBodySize]
	if cfg.ShowFullSize {
		truncated += fmt.Sprintf("\n[...truncated, %s total]", FormatBodySize(len(body)))
	} else {
		truncated += "\n[...truncated]"
	}
	info.Reason = "size-cap"
	info.Returned = cfg.MaxBodySize
	return truncated, info
}

func TruncateBody(body, contentType string, cfg store.TruncateConfig) (string, bool) {
	out, info := TruncateBodyInfo(body, contentType, cfg)
	return out, info.Reason != "none"
}

func SpillBodyToDisk(body, requestID, contentType string) (string, TruncationInfo, error) {
	ext := ".txt"
	switch {
	case strings.Contains(contentType, "json"):
		ext = ".json"
	case strings.Contains(contentType, "html"):
		ext = ".html"
	case strings.Contains(contentType, "javascript"):
		ext = ".js"
	}
	dir := os.Getenv("REP_SPILL_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", TruncationInfo{}, err
	}
	safeID := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, requestID)
	path := filepath.Join(dir, "rep-body-"+safeID+ext)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		return "", TruncationInfo{}, err
	}
	previewLen := min(len(body), OverflowPreviewSize)
	preview := body[:previewLen] + fmt.Sprintf("\n\n[...%d bytes total; full body spilled]\nREP_SPILL=%s", len(body), path)
	return preview, TruncationInfo{
		Reason:       "overflow-to-disk",
		Returned:     previewLen,
		Total:        len(body),
		OverflowPath: path,
	}, nil
}

type RequestOutput struct {
	ID                    string             `json:"id"`
	OriginalID            string             `json:"original_id,omitempty"`
	Method                string             `json:"method"`
	URL                   string             `json:"url"`
	PageURL               string             `json:"page_url,omitempty"`
	ResourceType          string             `json:"resource_type,omitempty"`
	Initiator             string             `json:"initiator,omitempty"`
	ResponseEncoding      string             `json:"response_encoding,omitempty"`
	ResponseBodyTruncated bool               `json:"response_body_truncated,omitempty"`
	ResponseBodyError     string             `json:"response_body_error,omitempty"`
	ResponseBodyCapture   *store.BodyCapture `json:"response_body_capture,omitempty"`
	RequestBodyCapture    *store.BodyCapture `json:"request_body_capture,omitempty"`
	NetworkState          string             `json:"network_state,omitempty"`
	ErrorText             string             `json:"error_text,omitempty"`
	CaptureSource         string             `json:"capture_source,omitempty"`
	TabID                 int                `json:"tab_id,omitempty"`
	Timestamp             int64              `json:"timestamp,omitempty"`
	Domain                string             `json:"domain"`
	Path                  string             `json:"path"`
	Headers               store.HeaderMap    `json:"headers,omitempty"`
	Body                  string             `json:"body,omitempty"`
	Response              *ResponseOutput    `json:"response,omitempty"`
}

type ResponseOutput struct {
	Status  int             `json:"status"`
	Headers store.HeaderMap `json:"headers,omitempty"`
	Body    string          `json:"body,omitempty"`
}

func FormatRequest(req *store.Request, mode store.OutputMode) RequestOutput {
	requestHeaders := req.Headers
	if mode == store.OutputMeta {
		requestHeaders = filterMetaHeaders(req.Headers, false)
	}
	out := RequestOutput{
		ID: req.ID, OriginalID: req.OriginalID, Method: req.Method, URL: req.URL,
		PageURL: req.PageURL, ResourceType: req.ResourceType, Initiator: req.Initiator,
		ResponseEncoding: req.ResponseEncoding, ResponseBodyTruncated: req.ResponseBodyTruncated,
		ResponseBodyError: req.ResponseBodyError, ErrorText: req.ErrorText,
		ResponseBodyCapture: req.ResponseBodyCapture, RequestBodyCapture: req.RequestBodyCapture, NetworkState: req.NetworkState,
		CaptureSource: req.CaptureSource, TabID: req.TabID, Timestamp: req.Timestamp,
		Domain: req.Domain, Path: req.Path,
		Headers: requestHeaders, Body: req.Body,
	}
	if mode == store.OutputMeta {
		out.Body = ""
	}
	if req.Response != nil {
		responseHeaders := req.Response.Headers
		if mode == store.OutputMeta {
			responseHeaders = filterMetaHeaders(req.Response.Headers, true)
		}
		resp := &ResponseOutput{Status: req.Response.Status, Headers: responseHeaders}
		switch mode {
		case store.OutputMeta:
		case store.OutputCompact:
			resp.Body, _ = TruncateBody(req.Response.Body, store.HeaderFirst(req.Response.Headers, "content-type"), store.DefaultTruncateConfig())
		default:
			resp.Body = req.Response.Body
		}
		out.Response = resp
	}
	return out
}

func filterMetaHeaders(headers store.HeaderMap, response bool) store.HeaderMap {
	if len(headers) == 0 {
		return headers
	}
	filtered := make(store.HeaderMap)
	for name, values := range headers {
		lower := strings.ToLower(name)
		if !keepMetaHeader(lower, response) {
			continue
		}
		copyValues := append([]string(nil), values...)
		if sensitiveHeader(lower) {
			for i, value := range copyValues {
				digest := sha256.Sum256([]byte(value))
				copyValues[i] = fmt.Sprintf("[REDACTED sha256:%x bytes:%d]", digest[:4], len(value))
			}
		}
		filtered[name] = copyValues
	}
	return filtered
}

// MetaHeaders returns a bounded copy suitable for agent-facing terminal output.
// Authentication material is fingerprinted rather than partially disclosed.
func MetaHeaders(headers store.HeaderMap, response bool) store.HeaderMap {
	return filterMetaHeaders(headers, response)
}

func keepMetaHeader(name string, response bool) bool {
	if sensitiveHeader(name) {
		return true
	}
	if response {
		if name == "content-security-policy" || name == "content-security-policy-report-only" {
			return false
		}
		return strings.HasPrefix(name, "content-") || strings.HasPrefix(name, "x-") ||
			strings.HasPrefix(name, "access-control-") || strings.Contains(name, "ratelimit") ||
			name == "location" || name == "cache-control" || name == "etag" ||
			name == "last-modified" || name == "server" || name == "retry-after"
	}
	return strings.HasPrefix(name, "x-") || name == "accept" || name == "content-type" ||
		name == "content-length" || name == "origin" || name == "referer" ||
		name == "user-agent" || name == "sec-fetch-site" || name == "sec-fetch-mode" ||
		name == "sec-fetch-dest"
}

func sensitiveHeader(name string) bool {
	return name == "authorization" || name == "proxy-authorization" || name == "cookie" ||
		name == "set-cookie" || strings.Contains(name, "api-key") || strings.Contains(name, "apikey") ||
		strings.Contains(name, "csrf") || strings.Contains(name, "xsrf") || strings.Contains(name, "token") ||
		strings.Contains(name, "secret") || strings.Contains(name, "nonce")
}

func FormatRequests(reqs []store.Request, mode store.OutputMode) []RequestOutput {
	result := make([]RequestOutput, len(reqs))
	for i := range reqs {
		result[i] = FormatRequest(&reqs[i], mode)
	}
	return result
}

func ToJSON(v interface{}) (string, error) {
	b, err := sonic.MarshalIndent(v, "", "  ")
	return string(b), err
}

func ToCompactJSON(v interface{}) (string, error) {
	b, err := sonic.Marshal(v)
	return string(b), err
}

type BudgetMode int

const (
	BudgetMinimal BudgetMode = iota
	BudgetCompact
	BudgetStandard
	BudgetFull
)

func ParseBudgetMode(value string) BudgetMode {
	switch strings.ToLower(value) {
	case "minimal", "min":
		return BudgetMinimal
	case "standard", "std":
		return BudgetStandard
	case "full":
		return BudgetFull
	default:
		return BudgetCompact
	}
}

func FormatRequestLineBudget(req *store.Request, budget BudgetMode) string {
	status := 0
	if req.Response != nil {
		status = req.Response.Status
	}
	id := req.ID
	if req.SemanticID != "" {
		id = string(req.SemanticID)
	}
	base := fmt.Sprintf("[%s] %s %s → %d", id, req.Method, req.URL, status)
	if budget == BudgetMinimal {
		return fmt.Sprintf("[%s] %s %s → %d", id, req.Method, req.Path, status)
	}
	if budget >= BudgetStandard {
		return fmt.Sprintf("%s type=%s auth=%t", base, req.ResourceType, req.GetMeta().HasAuth)
	}
	return base
}

func FormatRequestCompact(req *store.Request) string {
	return FormatRequestLineBudget(req, BudgetCompact)
}

func FormatDomainInfo(info store.DomainInfo) string {
	methods := make([]string, 0, len(info.Methods))
	for method, count := range info.Methods {
		methods = append(methods, fmt.Sprintf("%s:%d", method, count))
	}
	sort.Strings(methods)
	flags := ""
	if info.IsPrimary {
		flags += " [PRIMARY]"
	}
	if info.IsIgnored {
		flags += " [IGNORED]"
	}
	return fmt.Sprintf("%s (%d reqs, %d endpoints)%s [%s]", info.Domain, info.RequestCount, len(info.Endpoints), flags, strings.Join(methods, ", "))
}

type EmptyResultContext struct {
	Command               string
	Source                string
	TotalCandidates       int
	DistinctDomains       int
	Filters               store.FilterOptions
	PrimaryCount          int
	PrimariesInCandidates int
	SampleDomains         []string
}

func FormatSourceLine(source string) string {
	if source == "" {
		source = "live.json"
	}
	return "source: " + source
}

func FormatEmptyReason(ctx EmptyResultContext) string {
	if ctx.Command == "" {
		ctx.Command = "list"
	}
	if ctx.Source == "" {
		ctx.Source = "live.json"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "source: %s (%d requests, %d domains)\n", ctx.Source, ctx.TotalCandidates, ctx.DistinctDomains)
	filters := make([]string, 0, 4)
	if ctx.Filters.PrimaryOnly {
		filters = append(filters, fmt.Sprintf("primary=%d (%d match candidates)", ctx.PrimaryCount, ctx.PrimariesInCandidates))
	}
	if ctx.Filters.Domain != "" {
		filters = append(filters, "domain="+ctx.Filters.Domain)
	}
	if ctx.Filters.Method != "" {
		filters = append(filters, "method="+ctx.Filters.Method)
	}
	if len(filters) == 0 {
		filters = append(filters, "none")
	}
	fmt.Fprintf(&b, "filters: %s\nresult: 0 requests matched\nsuggest:\n", strings.Join(filters, ", "))
	if ctx.TotalCandidates == 0 {
		b.WriteString("  rep browser status\n  rep browse <url>  # enable auto-export and capture traffic\n  rep summary\n")
		return b.String()
	}
	b.WriteString("  rep list --primary=false\n")
	if len(ctx.SampleDomains) > 0 {
		fmt.Fprintf(&b, "  rep primary --clear && rep primary %s\n", ctx.SampleDomains[0])
	}
	return b.String()
}

func FormatPaginationFooter(returned, total, offset, limit int) string {
	if returned == 0 {
		return ""
	}
	if limit <= 0 || total <= 0 {
		return fmt.Sprintf("[Showing %d requests]", returned)
	}
	start := offset + 1
	end := offset + returned
	if end < total {
		return fmt.Sprintf("[Showing %d-%d of %d requests. Next: --offset=%d --limit=%d]", start, end, total, end, limit)
	}
	return fmt.Sprintf("[Showing %d-%d of %d requests]", start, end, total)
}
