package store

import (
	"strings"
	"sync"
)

// RequestIndex provides O(1) lookups for requests by ID or SemanticID
type RequestIndex struct {
	mu            sync.RWMutex
	byID          map[string]*Request     // Original ID -> Request
	bySemantic    map[SemanticID]*Request // SemanticID -> Request
	byShortID     map[string]*Request     // Short ID prefix -> Request (for convenience)
	requestSlice  []*Request              // Original order for iteration
}

// NewRequestIndex creates a new request index
func NewRequestIndex() *RequestIndex {
	return &RequestIndex{
		byID:       make(map[string]*Request),
		bySemantic: make(map[SemanticID]*Request),
		byShortID:  make(map[string]*Request),
	}
}

// BuildIndex creates an index from a slice of requests
func BuildIndex(requests []Request) *RequestIndex {
	idx := NewRequestIndex()
	for i := range requests {
		idx.Add(&requests[i])
	}
	return idx
}

// Add adds a request to the index
func (idx *RequestIndex) Add(req *Request) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Ensure computed fields are set
	if req.SemanticID == "" {
		ComputeRequestFields(req)
	}

	idx.byID[req.ID] = req
	idx.bySemantic[req.SemanticID] = req
	idx.requestSlice = append(idx.requestSlice, req)

	// Add short ID lookup (first 6 chars)
	shortID := req.ID
	if len(shortID) > 6 {
		shortID = shortID[:6]
	}
	shortID = strings.TrimPrefix(shortID, "h_")
	idx.byShortID[shortID] = req
}

// GetByID returns a request by its original ID (O(1))
func (idx *RequestIndex) GetByID(id string) *Request {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	// Try exact match
	if req := idx.byID[id]; req != nil {
		return req
	}

	// Try without h_ prefix
	id = strings.TrimPrefix(id, "h_")

	// Try short ID
	if req := idx.byShortID[id]; req != nil {
		return req
	}

	// Try prefix match on short ID
	if len(id) >= 4 {
		for shortID, req := range idx.byShortID {
			if strings.HasPrefix(shortID, id) || strings.HasPrefix(id, shortID) {
				return req
			}
		}
	}

	return nil
}

// GetBySemantic returns a request by its semantic ID (O(1))
func (idx *RequestIndex) GetBySemantic(sid SemanticID) *Request {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.bySemantic[sid]
}

// GetByAny tries to find a request by any ID format
// Accepts: original ID, semantic ID, or short hash
func (idx *RequestIndex) GetByAny(id string) *Request {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	// Try as original ID
	if req := idx.byID[id]; req != nil {
		return req
	}

	// Try as semantic ID
	if req := idx.bySemantic[SemanticID(id)]; req != nil {
		return req
	}

	// Try as short ID
	id = strings.TrimPrefix(id, "h_")
	if req := idx.byShortID[id]; req != nil {
		return req
	}

	// Try to extract hash from semantic ID format (hash_METHOD_status)
	parts := strings.Split(id, "_")
	if len(parts) >= 1 {
		if req := idx.byShortID[parts[0]]; req != nil {
			return req
		}
	}

	// Fuzzy match on short ID
	for shortID, req := range idx.byShortID {
		if strings.HasPrefix(shortID, id) || strings.HasPrefix(id, shortID) {
			return req
		}
	}

	return nil
}

// Count returns the number of indexed requests
func (idx *RequestIndex) Count() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.requestSlice)
}

// All returns all requests in original order
func (idx *RequestIndex) All() []*Request {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	result := make([]*Request, len(idx.requestSlice))
	copy(result, idx.requestSlice)
	return result
}

// FilterByMethod returns requests with the given method
func (idx *RequestIndex) FilterByMethod(method string) []*Request {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []*Request
	for _, req := range idx.requestSlice {
		if strings.EqualFold(req.Method, method) {
			result = append(result, req)
		}
	}
	return result
}

// FilterByStatus returns requests with the given status
func (idx *RequestIndex) FilterByStatus(status int) []*Request {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []*Request
	for _, req := range idx.requestSlice {
		if req.Response != nil && req.Response.Status == status {
			result = append(result, req)
		}
	}
	return result
}

// FilterByStatusRange returns requests with status in the given range (e.g., "4xx")
func (idx *RequestIndex) FilterByStatusRange(statusRange string) []*Request {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []*Request
	for _, req := range idx.requestSlice {
		if req.SemanticID.MatchesStatusRange(statusRange) {
			result = append(result, req)
		}
	}
	return result
}

// FilterByDomain returns requests with the given domain
func (idx *RequestIndex) FilterByDomain(domain string) []*Request {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []*Request
	for _, req := range idx.requestSlice {
		if strings.EqualFold(req.Domain, domain) {
			result = append(result, req)
		}
	}
	return result
}

// Clear removes all entries from the index
func (idx *RequestIndex) Clear() {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	idx.byID = make(map[string]*Request)
	idx.bySemantic = make(map[SemanticID]*Request)
	idx.byShortID = make(map[string]*Request)
	idx.requestSlice = nil
}
