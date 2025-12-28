package store

// Request represents a captured HTTP request from the extension
// Matches the exact export format from rep+ extension
type Request struct {
	ID               string    `json:"id"`
	OriginalID       string    `json:"original_id,omitempty"`
	Method           string    `json:"method"`
	URL              string    `json:"url"`
	PageURL          string    `json:"page_url,omitempty"`
	ResourceType     string    `json:"resource_type,omitempty"`
	Initiator        string    `json:"initiator,omitempty"`
	Headers          HeaderMap `json:"headers,omitempty"`
	Body             string    `json:"body,omitempty"`
	Response         *Response `json:"response,omitempty"`
	ResponseEncoding string    `json:"response_encoding,omitempty"`
	Timestamp        int64     `json:"timestamp"`
	// Computed fields (not from export)
	Domain string `json:"-"`
	Path   string `json:"-"`
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
	ID        string    `json:"id"`        // Format: "YYYYMMDD-HHMMSS" or "YYYYMMDD-HHMMSS-note"
	Timestamp int64     `json:"timestamp"` // Unix millis when saved
	Note      string    `json:"note,omitempty"`
	Requests  []Request `json:"requests"`
}

// Store holds saved sessions and configuration
type Store struct {
	Sessions       []Session       `json:"sessions"`
	IgnoredDomains map[string]bool `json:"ignored_domains"`
	PrimaryDomains map[string]bool `json:"primary_domains"`
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
