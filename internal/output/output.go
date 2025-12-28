package output

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/store"
)

// Binary content types that should show as label instead of content
var binaryContentTypes = []string{
	"image/", "video/", "audio/", "font/",
	"application/octet-stream",
	"application/pdf",
	"application/zip",
	"application/gzip",
	"application/x-tar",
	"application/x-rar",
	"application/wasm",
}

// IsBinaryContentType checks if content type is binary
func IsBinaryContentType(contentType string) bool {
	ct := strings.ToLower(contentType)
	for _, prefix := range binaryContentTypes {
		if strings.HasPrefix(ct, prefix) || strings.Contains(ct, prefix) {
			return true
		}
	}
	return false
}

// SanitizeText replaces NUL bytes so text output stays grep-friendly.
func SanitizeText(input string) string {
	if !strings.ContainsRune(input, '\x00') {
		return input
	}
	return strings.ReplaceAll(input, "\x00", "\\x00")
}

// FormatBodySize formats byte size to human readable
func FormatBodySize(size int) string {
	if size < 1024 {
		return fmt.Sprintf("%dB", size)
	} else if size < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(size)/1024)
	} else {
		return fmt.Sprintf("%.1fMB", float64(size)/(1024*1024))
	}
}

// TruncateBody truncates response body for compact output
// Returns the truncated body and whether it was truncated
func TruncateBody(body string, contentType string, cfg store.TruncateConfig) (string, bool) {
	bodyLen := len(body)

	// Handle binary content
	if cfg.BinaryAsLabel && IsBinaryContentType(contentType) {
		return fmt.Sprintf("[BINARY: %s %s]", FormatBodySize(bodyLen), contentType), true
	}

	// No truncation needed
	if bodyLen <= cfg.MaxBodySize {
		return body, false
	}

	// Truncate with size info
	truncated := body[:cfg.MaxBodySize]
	if cfg.ShowFullSize {
		return truncated + fmt.Sprintf("\n[...truncated, %s total]", FormatBodySize(bodyLen)), true
	}
	return truncated + "\n[...truncated]", true
}

// RequestOutput represents a request formatted for output
type RequestOutput struct {
	ID               string          `json:"id"`
	OriginalID       string          `json:"original_id,omitempty"`
	Method           string          `json:"method"`
	URL              string          `json:"url"`
	PageURL          string          `json:"page_url,omitempty"`
	ResourceType     string          `json:"resource_type,omitempty"`
	Initiator        string          `json:"initiator,omitempty"`
	ResponseEncoding string          `json:"response_encoding,omitempty"`
	Domain           string          `json:"domain"`
	Path             string          `json:"path"`
	Headers          store.HeaderMap `json:"headers,omitempty"`
	Body             string          `json:"body,omitempty"`
	Response         *ResponseOutput `json:"response,omitempty"`
}

// ResponseOutput represents a response formatted for output
type ResponseOutput struct {
	Status  int             `json:"status"`
	Headers store.HeaderMap `json:"headers,omitempty"`
	Body    string          `json:"body,omitempty"`
}

// FormatRequest formats a request for the specified output mode
func FormatRequest(req *store.Request, mode store.OutputMode) RequestOutput {
	out := RequestOutput{
		ID:               req.ID,
		OriginalID:       req.OriginalID,
		Method:           req.Method,
		URL:              req.URL,
		PageURL:          req.PageURL,
		ResourceType:     req.ResourceType,
		Initiator:        req.Initiator,
		ResponseEncoding: req.ResponseEncoding,
		Domain:           req.Domain,
		Path:             req.Path,
		Headers:          req.Headers,
		Body:             req.Body,
	}

	if req.Response != nil {
		respOut := &ResponseOutput{
			Status:  req.Response.Status,
			Headers: req.Response.Headers,
		}

		switch mode {
		case store.OutputMeta:
			// No body
			respOut.Body = ""

		case store.OutputFull:
			// Full body
			respOut.Body = req.Response.Body

		case store.OutputCompact:
			// Truncated body
			contentType := store.HeaderFirst(req.Response.Headers, "content-type")
			respOut.Body, _ = TruncateBody(req.Response.Body, contentType, store.DefaultTruncateConfig())

		default:
			respOut.Body = req.Response.Body
		}

		out.Response = respOut
	}

	return out
}

// FormatRequests formats multiple requests
func FormatRequests(reqs []store.Request, mode store.OutputMode) []RequestOutput {
	result := make([]RequestOutput, len(reqs))
	for i, req := range reqs {
		result[i] = FormatRequest(&req, mode)
	}
	return result
}

// ToJSON converts output to JSON string
func ToJSON(v interface{}) (string, error) {
	data, err := sonic.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ToCompactJSON converts output to compact JSON (no indentation)
func ToCompactJSON(v interface{}) (string, error) {
	data, err := sonic.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// FormatRequestCompact returns a single-line summary of a request
func FormatRequestCompact(req *store.Request) string {
	status := 0
	if req.Response != nil {
		status = req.Response.Status
	}
	return fmt.Sprintf("[%s] %s %s → %d", req.ID, req.Method, req.URL, status)
}

// FormatDomainInfo formats domain info for display
func FormatDomainInfo(info store.DomainInfo) string {
	methods := make([]string, 0, len(info.Methods))
	for m, count := range info.Methods {
		methods = append(methods, fmt.Sprintf("%s:%d", m, count))
	}

	flags := ""
	if info.IsPrimary {
		flags += " [PRIMARY]"
	}
	if info.IsIgnored {
		flags += " [IGNORED]"
	}

	return fmt.Sprintf("%s (%d reqs, %d endpoints)%s [%s]",
		info.Domain,
		info.RequestCount,
		len(info.Endpoints),
		flags,
		strings.Join(methods, ", "))
}
