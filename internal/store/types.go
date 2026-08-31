package store

import "strings"

// Request represents a captured HTTP request from the extension
// Matches the exact export format from rep+ extension
type Request struct {
	ID                      string    `json:"id"`
	OriginalID              string    `json:"original_id,omitempty"`
	Method                  string    `json:"method"`
	URL                     string    `json:"url"`
	PageURL                 string    `json:"page_url,omitempty"`
	ResourceType            string    `json:"resource_type,omitempty"`
	Initiator               string    `json:"initiator,omitempty"`
	Headers                 HeaderMap `json:"headers,omitempty"`
	Body                    string    `json:"body,omitempty"`
	Response                *Response `json:"response,omitempty"`
	ResponseEncoding        string    `json:"response_encoding,omitempty"`
	ResponseBodyTruncated   bool      `json:"response_body_truncated,omitempty"`
	ResponseBodyError       string    `json:"response_body_error,omitempty"`
	ErrorText               string    `json:"error_text,omitempty"`
	Canceled                bool      `json:"canceled,omitempty"`
	IntentionalCancellation string    `json:"intentional_cancellation,omitempty"`
	CaptureSource           string    `json:"capture_source,omitempty"`
	TabID                   int       `json:"tab_id,omitempty"`
	Timestamp               int64     `json:"timestamp"`
	StartOrdinal            int64     `json:"start_ordinal,omitempty"`
	ResponseOrdinal         int64     `json:"response_ordinal,omitempty"`
	CompletionOrdinal       int64     `json:"completion_ordinal,omitempty"`
	// Computed fields (not from export)
	Domain     string     `json:"-"`
	Path       string     `json:"-"`
	SemanticID SemanticID `json:"-"` // AI-friendly ID: hash_METHOD_status
}

// RequestMeta provides lightweight metadata for AI agent consumption
// Contains only essential info without headers/bodies to minimize token usage
type RequestMeta struct {
	ID           string     `json:"id"`
	SemanticID   SemanticID `json:"sid"`
	Method       string     `json:"method"`
	URL          string     `json:"url"`
	Domain       string     `json:"domain"`
	Path         string     `json:"path"`
	Status       int        `json:"status"`
	ResourceType string     `json:"type,omitempty"`
	HasAuth      bool       `json:"has_auth"`
	AuthType     string     `json:"auth_type,omitempty"` // bearer, cookie, api-key, basic
	BodyType     string     `json:"body_type,omitempty"` // json, form, xml, text, binary
	BodySize     int        `json:"body_size"`
	Timestamp    int64      `json:"ts"`
}

// GetMeta returns lightweight metadata (no bodies/headers) for AI consumption
func (r *Request) GetMeta() RequestMeta {
	meta := RequestMeta{
		ID:           r.ID,
		SemanticID:   r.SemanticID,
		Method:       r.Method,
		URL:          r.URL,
		Domain:       r.Domain,
		Path:         r.Path,
		ResourceType: r.ResourceType,
		Timestamp:    r.Timestamp,
	}

	if r.Response != nil {
		meta.Status = r.Response.Status
		meta.BodySize = len(r.Response.Body)
		meta.BodyType = detectBodyType(r.Response.Headers, r.Response.Body)
	}

	meta.HasAuth, meta.AuthType = detectAuth(r.Headers)

	return meta
}

// detectAuth checks request headers for authentication
func detectAuth(headers HeaderMap) (hasAuth bool, authType string) {
	if headers == nil {
		return false, ""
	}

	// Check Authorization header
	if auth := HeaderFirst(headers, "authorization"); auth != "" {
		hasAuth = true
		authLower := strings.ToLower(auth)
		switch {
		case strings.HasPrefix(authLower, "bearer "):
			authType = "bearer"
		case strings.HasPrefix(authLower, "basic "):
			authType = "basic"
		case strings.HasPrefix(authLower, "digest "):
			authType = "digest"
		default:
			authType = "other"
		}
		return
	}

	// Check for API key headers
	for key := range headers {
		keyLower := strings.ToLower(key)
		if strings.Contains(keyLower, "api-key") || strings.Contains(keyLower, "apikey") ||
			strings.Contains(keyLower, "x-api-key") {
			return true, "api-key"
		}
	}

	// Check for cookies (simple presence check)
	if cookie := HeaderFirst(headers, "cookie"); cookie != "" {
		return true, "cookie"
	}

	return false, ""
}

// detectBodyType determines the type of body content
func detectBodyType(headers HeaderMap, body string) string {
	if body == "" {
		return "none"
	}

	contentType := ""
	if headers != nil {
		contentType = strings.ToLower(HeaderFirst(headers, "content-type"))
	}

	switch {
	case strings.Contains(contentType, "application/json"):
		return "json"
	case strings.Contains(contentType, "application/xml"), strings.Contains(contentType, "text/xml"):
		return "xml"
	case strings.Contains(contentType, "application/x-www-form-urlencoded"):
		return "form"
	case strings.Contains(contentType, "multipart/form-data"):
		return "multipart"
	case strings.Contains(contentType, "text/html"):
		return "html"
	case strings.Contains(contentType, "text/"):
		return "text"
	case strings.HasPrefix(contentType, "image/"), strings.HasPrefix(contentType, "audio/"),
		strings.HasPrefix(contentType, "video/"), strings.Contains(contentType, "octet-stream"):
		return "binary"
	default:
		// Try to detect from content
		trimmed := strings.TrimSpace(body)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			return "json"
		}
		if strings.HasPrefix(trimmed, "<") {
			if strings.Contains(trimmed[:min(100, len(trimmed))], "<?xml") {
				return "xml"
			}
			return "html"
		}
		return "text"
	}
}

// Response represents an HTTP response
type Response struct {
	Status  int       `json:"status"`
	Headers HeaderMap `json:"headers,omitempty"`
	Body    string    `json:"body,omitempty"`
}

// Export represents the JSON export format from rep+ extension
type Export struct {
	Version    string    `json:"version"`
	ExportedAt string    `json:"exported_at"`
	Requests   []Request `json:"requests"`
}

// Session represents a saved capture session
type Session struct {
	ID        string    `json:"id"` // Format: "YYYYMMDD-HHMMSS" or "YYYYMMDD-HHMMSS-note"
	HashID    string    `json:"hash_id,omitempty"`
	Timestamp int64     `json:"timestamp"` // Unix millis when saved
	Note      string    `json:"note,omitempty"`
	Requests  []Request `json:"requests"`
}

// MutedPath represents a path pattern to mute (fine-grained noise filtering)
type MutedPath struct {
	Domain  string `json:"domain"`  // Domain to match, or "*" for all domains
	Pattern string `json:"pattern"` // Path pattern (prefix or regex if starts with ^)
}

// Store holds saved sessions and configuration
type Store struct {
	Sessions       []Session       `json:"sessions"`
	IgnoredDomains map[string]bool `json:"ignored_domains"`
	PrimaryDomains map[string]bool `json:"primary_domains"`
	MutedPaths     []MutedPath     `json:"muted_paths,omitempty"`
	// Legacy fields for migration (will be removed after migration)
	Requests   []Request `json:"requests,omitempty"`
	LastImport int64     `json:"last_import,omitempty"`
}

// OutputMode controls how much detail to show
type OutputMode string

const (
	OutputCompact OutputMode = "compact" // Truncated bodies (default)
	OutputMeta    OutputMode = "meta"    // Headers only, no body
	OutputFull    OutputMode = "full"    // Complete bodies
	OutputJSON    OutputMode = "json"    // Raw JSON for piping
)

// FilterOptions for filtering requests
type FilterOptions struct {
	Domain         string
	Domains        []string
	Method         string
	Methods        []string
	Status         int
	StatusRange    string   // e.g., "4xx", "5xx"
	StatusRanges   []string // Multiple ranges like ["4xx", "5xx"]
	ResourceTypes  []string // Filter by resource type (script, xhr, fetch, etc.)
	Pattern        string   // regex pattern for URL
	ExcludeIgnored bool
	PrimaryOnly    bool
	Limit          int
	Offset         int
	Since          int64 // Timestamp cutoff in milliseconds
}

// PageFlowInfo represents requests grouped by PageURL for cross-domain analysis
type PageFlowInfo struct {
	PageURL          string   `json:"page_url"`
	PageDomain       string   `json:"page_domain"`
	RequestedDomains []string `json:"requested_domains"`
	RequestCount     int      `json:"request_count"`
}

// DomainInfo holds computed stats for a domain (not stored, computed on demand)
type DomainInfo struct {
	Domain       string
	RequestCount int
	Methods      map[string]int
	Endpoints    []string
	IsIgnored    bool
	IsPrimary    bool
}

// TruncateConfig controls body truncation
type TruncateConfig struct {
	MaxBodySize   int  // Max chars to show (default 500)
	ShowFullSize  bool // Show total size in truncation message
	BinaryAsLabel bool // Show "[BINARY: 12KB image/png]" for binary
}

// DefaultTruncateConfig returns sensible defaults for agent consumption
func DefaultTruncateConfig() TruncateConfig {
	return TruncateConfig{
		MaxBodySize:   500,
		ShowFullSize:  true,
		BinaryAsLabel: true,
	}
}
