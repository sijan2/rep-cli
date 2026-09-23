package store

import (
	"strings"
	"sync"
)

// RequestIndex provides O(1) lookups for requests by ID or SemanticID
type RequestIndex struct {
	mu            sync.RWMutex
	byID          map[string]*Request
	bySemantic    map[SemanticID]map[string]struct{}
	semanticForID map[string]SemanticID
	requestSlice  []*Request // Original order for iteration
}

// NewRequestIndex creates a new request index
func NewRequestIndex() *RequestIndex {
	return &RequestIndex{
		byID:          make(map[string]*Request),
		bySemantic:    make(map[SemanticID]map[string]struct{}),
		semanticForID: make(map[string]SemanticID),
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
	if req == nil || req.ID == "" {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Ensure computed fields are set
	if req.SemanticID == "" {
		ComputeRequestFields(req)
	}

	if previous, exists := idx.semanticForID[req.ID]; exists {
		delete(idx.bySemantic[previous], req.ID)
		if len(idx.bySemantic[previous]) == 0 {
			delete(idx.bySemantic, previous)
		}
	}
	idx.byID[req.ID] = req
	if idx.bySemantic[req.SemanticID] == nil {
		idx.bySemantic[req.SemanticID] = make(map[string]struct{})
	}
	idx.bySemantic[req.SemanticID][req.ID] = struct{}{}
	idx.semanticForID[req.ID] = req.SemanticID
	idx.requestSlice = append(idx.requestSlice, req)
}

// GetByID resolves an exact ID first, then a unique forward prefix of the full
// stored ID. Prefix ambiguity never selects an arbitrary request.
func (idx *RequestIndex) GetByID(id string) *Request {
	return idx.GetByAny(id)
}

// GetBySemantic returns a request by its semantic ID (O(1))
func (idx *RequestIndex) GetBySemantic(sid SemanticID) *Request {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.semanticLocked(sid)
}

// GetExact accepts an original ID (with or without h_) or a unique exact
// semantic alias. It never performs prefix matching.
func (idx *RequestIndex) GetExact(id string) *Request {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.exactLocked(id)
}

func (idx *RequestIndex) exactLocked(id string) *Request {
	if id == "" {
		return nil
	}
	if req := idx.byID[id]; req != nil {
		return req
	}
	if strings.HasPrefix(id, "h_") {
		if req := idx.byID[strings.TrimPrefix(id, "h_")]; req != nil {
			return req
		}
	} else if req := idx.byID["h_"+id]; req != nil {
		return req
	}
	return idx.semanticLocked(SemanticID(id))
}

func (idx *RequestIndex) semanticLocked(id SemanticID) *Request {
	ids := idx.bySemantic[id]
	if len(ids) != 1 {
		return nil
	}
	for requestID := range ids {
		return idx.byID[requestID]
	}
	return nil
}

// GetByAny accepts an exact original or semantic ID, or a unique forward prefix
// of at least four characters after the optional h_ prefix. Unknown semantic
// labels do not degrade into hash-only matches.
func (idx *RequestIndex) GetByAny(id string) *Request {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if req := idx.exactLocked(id); req != nil {
		return req
	}
	if _, exists := idx.bySemantic[SemanticID(id)]; exists {
		// An ambiguous semantic alias must not become a prefix of another ID.
		return nil
	}
	id = strings.TrimPrefix(id, "h_")
	if len(id) < 4 {
		return nil
	}
	var match *Request
	for storedID, request := range idx.byID {
		if strings.HasPrefix(strings.TrimPrefix(storedID, "h_"), id) {
			if match != nil && match.ID != storedID {
				return nil
			}
			match = request
		}
	}
	return match
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
	idx.bySemantic = make(map[SemanticID]map[string]struct{})
	idx.semanticForID = make(map[string]SemanticID)
	idx.requestSlice = nil
}
