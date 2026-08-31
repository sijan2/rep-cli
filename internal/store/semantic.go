package store

import (
	"fmt"
	"strings"
)

// SemanticID encodes meaning for AI reasoning
// Format: hash_METHOD_status
// Example: h6f2d0_POST_201
type SemanticID string

// GenerateSemanticID creates a semantic ID from a request
// Format: shortHash_METHOD_status (e.g., h6f2d0_POST_201)
func GenerateSemanticID(r *Request) SemanticID {
	status := 0
	if r.Response != nil {
		status = r.Response.Status
	}

	// Get short hash from ID (first 6 chars, or full ID if shorter)
	shortHash := r.ID
	if len(shortHash) > 6 {
		shortHash = shortHash[:6]
	}
	// Remove any prefix like "h_" for cleaner IDs
	shortHash = strings.TrimPrefix(shortHash, "h_")
	if len(shortHash) > 6 {
		shortHash = shortHash[:6]
	}

	return SemanticID(fmt.Sprintf("%s_%s_%d", shortHash, r.Method, status))
}

// ParseSemanticID extracts components from a semantic ID
// Returns hash, method, status, ok
func ParseSemanticID(sid SemanticID) (hash string, method string, status int, ok bool) {
	parts := strings.Split(string(sid), "_")
	if len(parts) < 3 {
		return "", "", 0, false
	}

	hash = parts[0]
	method = parts[1]

	// Parse status from last part
	_, err := fmt.Sscanf(parts[2], "%d", &status)
	if err != nil {
		return "", "", 0, false
	}

	return hash, method, status, true
}

// String returns the string representation
func (sid SemanticID) String() string {
	return string(sid)
}

// Method extracts the HTTP method from the semantic ID
func (sid SemanticID) Method() string {
	_, method, _, ok := ParseSemanticID(sid)
	if !ok {
		return ""
	}
	return method
}

// Status extracts the status code from the semantic ID
func (sid SemanticID) Status() int {
	_, _, status, ok := ParseSemanticID(sid)
	if !ok {
		return 0
	}
	return status
}

// Hash extracts the short hash from the semantic ID
func (sid SemanticID) Hash() string {
	hash, _, _, ok := ParseSemanticID(sid)
	if !ok {
		return ""
	}
	return hash
}

// MatchesMethod checks if the semantic ID has the given method
func (sid SemanticID) MatchesMethod(method string) bool {
	return strings.EqualFold(sid.Method(), method)
}

// MatchesStatus checks if the semantic ID has the given status
func (sid SemanticID) MatchesStatus(status int) bool {
	return sid.Status() == status
}

// MatchesStatusRange checks if the semantic ID's status is in the given range
// e.g., "4xx" matches 400-499
func (sid SemanticID) MatchesStatusRange(statusRange string) bool {
	status := sid.Status()
	switch statusRange {
	case "2xx":
		return status >= 200 && status < 300
	case "3xx":
		return status >= 300 && status < 400
	case "4xx":
		return status >= 400 && status < 500
	case "5xx":
		return status >= 500 && status < 600
	default:
		return false
	}
}
