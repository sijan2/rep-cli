package store

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
)

const (
	StoreFileName = "store.json"
	LiveFileName  = "live.json" // Native host export file name
)

// GetStorePath returns the path to the store directory following XDG spec
// Uses ~/.local/share/rep-cli/
func GetStorePath() (string, error) {
	// Check XDG_DATA_HOME first
	if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
		return filepath.Join(xdgData, "rep-cli"), nil
	}
	// Default to ~/.local/share/rep-cli
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "rep-cli"), nil
}

var (
	instance *Store
	once     sync.Once
	mu       sync.RWMutex
)

// GetLiveFilePath returns the path where live data is exported.
// REPLIVE_PATH overrides the default XDG/rep-cli location.
func GetLiveFilePath() (string, error) {
	if override := os.Getenv("REPLIVE_PATH"); override != "" {
		return expandHomePath(override)
	}
	storePath, err := GetStorePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(storePath, LiveFileName), nil
}

func expandHomePath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

// GetStoreFilePath returns the full path to the store file
func GetStoreFilePath() (string, error) {
	storePath, err := GetStorePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(storePath, StoreFileName), nil
}

// EnsureStoreDir creates the store directory if it doesn't exist
func EnsureStoreDir() error {
	storePath, err := GetStorePath()
	if err != nil {
		return err
	}
	return os.MkdirAll(storePath, 0755)
}

// NewStore creates a new store
func NewStore() *Store {
	return &Store{
		Sessions:       []Session{},
		IgnoredDomains: make(map[string]bool),
		PrimaryDomains: make(map[string]bool),
	}
}

// Get returns the singleton store instance
func Get() (*Store, error) {
	var loadErr error
	once.Do(func() {
		instance, loadErr = Load()
	})
	if loadErr != nil {
		return nil, loadErr
	}
	return instance, nil
}

// NewTempStore creates a temporary store from a slice of requests.
// Used for filtering live.json data without affecting the persistent store.
func NewTempStore(requests []Request) *Store {
	s := NewStore()
	s.Requests = requests
	// Compute Domain/Path for each request
	for i := range s.Requests {
		req := &s.Requests[i]
		if parsed, err := url.Parse(req.URL); err == nil {
			req.Domain = parsed.Host
			req.Path = parsed.Path
		}
	}
	return s
}

// Load loads the store from disk
func Load() (*Store, error) {
	filePath, err := GetStoreFilePath()
	if err != nil {
		return nil, err
	}

	store := NewStore()

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, fmt.Errorf("failed to read store: %w", err)
	}

	if err := sonic.Unmarshal(data, store); err != nil {
		return nil, fmt.Errorf("failed to parse store: %w", err)
	}

	// Ensure maps are initialized
	if store.IgnoredDomains == nil {
		store.IgnoredDomains = make(map[string]bool)
	}
	if store.PrimaryDomains == nil {
		store.PrimaryDomains = make(map[string]bool)
	}
	if store.Sessions == nil {
		store.Sessions = []Session{}
	}

	// Migrate old format: if we have Requests but no Sessions, create a migration session
	if len(store.Requests) > 0 && len(store.Sessions) == 0 {
		// Compute domain/path for legacy requests
		for i := range store.Requests {
			ComputeRequestFields(&store.Requests[i])
		}
		session := Session{
			ID:        "migrated-" + time.Now().Format("20060102"),
			Timestamp: time.Now().UnixMilli(),
			Note:      "Auto-migrated from old format",
			Requests:  store.Requests,
		}
		store.Sessions = append(store.Sessions, session)
		store.Requests = nil // Clear legacy field
		store.LastImport = 0 // Clear legacy field
	}

	// Compute domain/path for all session requests
	for i := range store.Sessions {
		for j := range store.Sessions[i].Requests {
			ComputeRequestFields(&store.Sessions[i].Requests[j])
		}
	}

	return store, nil
}

// ComputeRequestFields computes Domain and Path from URL.
func ComputeRequestFields(req *Request) {
	if parsedURL, err := url.Parse(req.URL); err == nil {
		req.Domain = parsedURL.Host
		req.Path = parsedURL.Path
		if parsedURL.RawQuery != "" {
			req.Path += "?" + parsedURL.RawQuery
		}
	}
}


// Save saves the store to disk
func (s *Store) Save() error {
	mu.Lock()
	defer mu.Unlock()

	if err := EnsureStoreDir(); err != nil {
		return err
	}

	filePath, err := GetStoreFilePath()
	if err != nil {
		return err
	}

	data, err := sonic.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal store: %w", err)
	}

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write store: %w", err)
	}

	return nil
}

// Clear removes all sessions from the store
func (s *Store) Clear() {
	mu.Lock()
	defer mu.Unlock()
	s.Sessions = []Session{}
}

// ClearAll clears sessions, ignore list, and primary list
func (s *Store) ClearAll() {
	mu.Lock()
	defer mu.Unlock()
	s.Sessions = []Session{}
	s.IgnoredDomains = make(map[string]bool)
	s.PrimaryDomains = make(map[string]bool)
}

// GenerateSessionID creates an agent-friendly session ID
func GenerateSessionID(note string) string {
	base := time.Now().Format("20060102-150405")
	if note == "" {
		return base
	}
	// Sanitize note: lowercase, replace spaces with hyphens, max 30 chars
	sanitized := strings.ToLower(note)
	sanitized = strings.ReplaceAll(sanitized, " ", "-")
	// Remove non-alphanumeric characters except hyphens
	var result strings.Builder
	for _, r := range sanitized {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		}
	}
	sanitized = result.String()
	if len(sanitized) > 30 {
		sanitized = sanitized[:30]
	}
	// Trim trailing hyphens
	sanitized = strings.TrimRight(sanitized, "-")
	if sanitized == "" {
		return base
	}
	return base + "-" + sanitized
}

// AddSession saves a new session to the store
func (s *Store) AddSession(id string, note string, requests []Request) *Session {
	mu.Lock()
	defer mu.Unlock()

	// Compute domain/path for all requests
	for i := range requests {
		ComputeRequestFields(&requests[i])
	}

	session := Session{
		ID:        id,
		Timestamp: time.Now().UnixMilli(),
		Note:      note,
		Requests:  requests,
	}
	s.Sessions = append(s.Sessions, session)
	return &s.Sessions[len(s.Sessions)-1]
}

// GetSession returns a session by ID (exact or prefix match)
func (s *Store) GetSession(id string) *Session {
	mu.RLock()
	defer mu.RUnlock()

	// Try exact match first
	for i := range s.Sessions {
		if s.Sessions[i].ID == id {
			return &s.Sessions[i]
		}
	}
	// Try prefix match
	for i := range s.Sessions {
		if strings.HasPrefix(s.Sessions[i].ID, id) {
			return &s.Sessions[i]
		}
	}
	return nil
}

// ListSessions returns all sessions (newest first)
func (s *Store) ListSessions() []Session {
	mu.RLock()
	defer mu.RUnlock()

	result := make([]Session, len(s.Sessions))
	copy(result, s.Sessions)
	// Reverse to show newest first
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// GetLatestSession returns the most recent session
func (s *Store) GetLatestSession() *Session {
	mu.RLock()
	defer mu.RUnlock()

	if len(s.Sessions) == 0 {
		return nil
	}
	return &s.Sessions[len(s.Sessions)-1]
}

// SessionCount returns the number of saved sessions
func (s *Store) SessionCount() int {
	mu.RLock()
	defer mu.RUnlock()
	return len(s.Sessions)
}

// ClearIgnoreList clears the ignore list
func (s *Store) ClearIgnoreList() {
	mu.Lock()
	defer mu.Unlock()
	s.IgnoredDomains = make(map[string]bool)
}


// IsIgnored checks if a domain is in the ignore list
func (s *Store) IsIgnored(domain string) bool {
	mu.RLock()
	defer mu.RUnlock()
	return s.IgnoredDomains[domain]
}

// Ignore adds domains to the ignore list
func (s *Store) Ignore(domains ...string) int {
	mu.Lock()
	defer mu.Unlock()
	count := 0
	for _, domain := range domains {
		if !s.IgnoredDomains[domain] {
			s.IgnoredDomains[domain] = true
			count++
		}
	}
	return count
}

// Unignore removes domains from the ignore list
func (s *Store) Unignore(domains ...string) int {
	mu.Lock()
	defer mu.Unlock()
	count := 0
	for _, domain := range domains {
		if s.IgnoredDomains[domain] {
			delete(s.IgnoredDomains, domain)
			count++
		}
	}
	return count
}

// SetPrimary marks domains as primary targets
func (s *Store) SetPrimary(domains ...string) int {
	mu.Lock()
	defer mu.Unlock()
	count := 0
	for _, domain := range domains {
		if !s.PrimaryDomains[domain] {
			s.PrimaryDomains[domain] = true
			count++
		}
	}
	return count
}

// UnsetPrimary removes domains from primary list
func (s *Store) UnsetPrimary(domains ...string) int {
	mu.Lock()
	defer mu.Unlock()
	count := 0
	for _, domain := range domains {
		if s.PrimaryDomains[domain] {
			delete(s.PrimaryDomains, domain)
			count++
		}
	}
	return count
}

// IsPrimary checks if a domain is marked as primary
func (s *Store) IsPrimary(domain string) bool {
	mu.RLock()
	defer mu.RUnlock()
	return s.PrimaryDomains[domain]
}

// Count returns the number of requests in the store (for temp stores)
func (s *Store) Count() int {
	mu.RLock()
	defer mu.RUnlock()
	return len(s.Requests)
}

// GetRequest returns a request by ID (searches temp store requests)
func (s *Store) GetRequest(id string) *Request {
	mu.RLock()
	defer mu.RUnlock()
	for i := range s.Requests {
		if s.Requests[i].ID == id {
			return &s.Requests[i]
		}
	}
	return nil
}

// GetRequestFromSessions searches all saved sessions for a request by ID
func (s *Store) GetRequestFromSessions(id string) *Request {
	mu.RLock()
	defer mu.RUnlock()
	for i := range s.Sessions {
		for j := range s.Sessions[i].Requests {
			if s.Sessions[i].Requests[j].ID == id {
				return &s.Sessions[i].Requests[j]
			}
		}
	}
	return nil
}

// Filter returns requests matching the filter options
func (s *Store) Filter(opts FilterOptions) []Request {
	mu.RLock()
	defer mu.RUnlock()

	var result []Request
	pattern := strings.TrimSpace(opts.Pattern)
	var patternRE *regexp.Regexp
	var patternLower string
	if pattern != "" {
		if re, err := regexp.Compile(pattern); err == nil {
			patternRE = re
		} else {
			patternLower = strings.ToLower(pattern)
		}
	}

	for _, req := range s.Requests {
		// Skip ignored domains
		if opts.ExcludeIgnored && s.IgnoredDomains[req.Domain] {
			continue
		}

		// Primary only filter
		if opts.PrimaryOnly && !s.PrimaryDomains[req.Domain] {
			continue
		}

		// Filter by domain
		if opts.Domain != "" && !strings.EqualFold(req.Domain, opts.Domain) {
			continue
		}

		// Filter by domains list
		if len(opts.Domains) > 0 {
			found := false
			for _, d := range opts.Domains {
				if strings.EqualFold(req.Domain, d) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Filter by method
		if opts.Method != "" && !strings.EqualFold(req.Method, opts.Method) {
			continue
		}

		// Filter by methods list
		if len(opts.Methods) > 0 {
			found := false
			for _, m := range opts.Methods {
				if strings.EqualFold(req.Method, m) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Filter by status
		if opts.Status != 0 && (req.Response == nil || req.Response.Status != opts.Status) {
			continue
		}

		// Filter by status range (e.g., "4xx", "5xx")
		if opts.StatusRange != "" && req.Response != nil {
			status := req.Response.Status
			switch opts.StatusRange {
			case "2xx":
				if status < 200 || status >= 300 {
					continue
				}
			case "3xx":
				if status < 300 || status >= 400 {
					continue
				}
			case "4xx":
				if status < 400 || status >= 500 {
					continue
				}
			case "5xx":
				if status < 500 || status >= 600 {
					continue
				}
			}
		}

		// Filter by multiple status ranges (e.g., ["4xx", "5xx"])
		if len(opts.StatusRanges) > 0 && req.Response != nil {
			status := req.Response.Status
			matched := false
			for _, sr := range opts.StatusRanges {
				switch sr {
				case "2xx":
					if status >= 200 && status < 300 {
						matched = true
					}
				case "3xx":
					if status >= 300 && status < 400 {
						matched = true
					}
				case "4xx":
					if status >= 400 && status < 500 {
						matched = true
					}
				case "5xx":
					if status >= 500 && status < 600 {
						matched = true
					}
				}
				if matched {
					break
				}
			}
			if !matched {
				continue
			}
		}

		// Filter by resource types (e.g., ["script", "xhr", "fetch"])
		if len(opts.ResourceTypes) > 0 {
			found := false
			for _, rt := range opts.ResourceTypes {
				if strings.EqualFold(req.ResourceType, rt) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// URL pattern filter (regex, fallback to substring)
		if pattern != "" {
			if patternRE != nil {
				if !patternRE.MatchString(req.URL) {
					continue
				}
			} else if !strings.Contains(strings.ToLower(req.URL), patternLower) {
				continue
			}
		}

		// Apply offset
		if opts.Offset > 0 {
			opts.Offset--
			continue
		}

		result = append(result, req)

		// Apply limit
		if opts.Limit > 0 && len(result) >= opts.Limit {
			break
		}
	}

	return result
}

// GetDomains returns all unique domains with their info
func (s *Store) GetDomains() []DomainInfo {
	mu.RLock()
	defer mu.RUnlock()

	domainMap := make(map[string]*DomainInfo)

	for _, req := range s.Requests {
		if req.Domain == "" {
			continue
		}

		info, exists := domainMap[req.Domain]
		if !exists {
			info = &DomainInfo{
				Domain:    req.Domain,
				Methods:   make(map[string]int),
				Endpoints: []string{},
				IsIgnored: s.IgnoredDomains[req.Domain],
				IsPrimary: s.PrimaryDomains[req.Domain],
			}
			domainMap[req.Domain] = info
		}

		info.RequestCount++
		info.Methods[req.Method]++

		// Track unique endpoints (method + path, without query)
		pathOnly := req.Path
		if idx := strings.Index(pathOnly, "?"); idx > 0 {
			pathOnly = pathOnly[:idx]
		}
		endpoint := fmt.Sprintf("%s %s", req.Method, pathOnly)

		found := false
		for _, e := range info.Endpoints {
			if e == endpoint {
				found = true
				break
			}
		}
		if !found && len(info.Endpoints) < 100 { // Cap at 100 endpoints
			info.Endpoints = append(info.Endpoints, endpoint)
		}
	}

	// Convert to slice and sort by request count
	result := make([]DomainInfo, 0, len(domainMap))
	for _, info := range domainMap {
		result = append(result, *info)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].RequestCount > result[j].RequestCount
	})

	return result
}

// GetIgnoredDomains returns all ignored domains
func (s *Store) GetIgnoredDomains() []string {
	mu.RLock()
	defer mu.RUnlock()

	result := make([]string, 0, len(s.IgnoredDomains))
	for domain := range s.IgnoredDomains {
		result = append(result, domain)
	}
	sort.Strings(result)
	return result
}

// GetPrimaryDomains returns all primary domains
func (s *Store) GetPrimaryDomains() []string {
	mu.RLock()
	defer mu.RUnlock()

	result := make([]string, 0, len(s.PrimaryDomains))
	for domain := range s.PrimaryDomains {
		result = append(result, domain)
	}
	sort.Strings(result)
	return result
}

// GetPageFlows groups requests by PageURL for cross-domain analysis
func (s *Store) GetPageFlows() []PageFlowInfo {
	mu.RLock()
	defer mu.RUnlock()

	pageMap := make(map[string]*PageFlowInfo)

	for _, req := range s.Requests {
		if req.PageURL == "" {
			continue
		}

		info, exists := pageMap[req.PageURL]
		if !exists {
			parsedPage, _ := url.Parse(req.PageURL)
			pageDomain := ""
			if parsedPage != nil {
				pageDomain = parsedPage.Host
			}
			info = &PageFlowInfo{
				PageURL:          req.PageURL,
				PageDomain:       pageDomain,
				RequestedDomains: []string{},
				RequestCount:     0,
			}
			pageMap[req.PageURL] = info
		}

		info.RequestCount++

		// Track unique domains
		found := false
		for _, d := range info.RequestedDomains {
			if d == req.Domain {
				found = true
				break
			}
		}
		if !found && req.Domain != "" {
			info.RequestedDomains = append(info.RequestedDomains, req.Domain)
		}
	}

	result := make([]PageFlowInfo, 0, len(pageMap))
	for _, info := range pageMap {
		sort.Strings(info.RequestedDomains)
		result = append(result, *info)
	}

	// Sort by request count descending
	sort.Slice(result, func(i, j int) bool {
		return result[i].RequestCount > result[j].RequestCount
	})

	return result
}

// GetBaseDomain extracts the base domain (e.g., "api.example.com" -> "example.com")
func GetBaseDomain(domain string) string {
	parts := strings.Split(domain, ".")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], ".")
	}
	return domain
}

// IsFirstParty checks if requestDomain is first-party relative to pageDomain
func IsFirstParty(requestDomain, pageDomain string) bool {
	return GetBaseDomain(requestDomain) == GetBaseDomain(pageDomain)
}
