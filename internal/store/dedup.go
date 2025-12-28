package store

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// RequestHash returns a stable hash for de-duplication.
func RequestHash(req *Request) string {
	if req == nil {
		return ""
	}
	raw := fmt.Sprintf("%s|%s|%s|%d", req.Method, req.URL, req.Body, req.Timestamp)
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", sum)
}

// RequestFingerprint returns the stable ID when available, otherwise a hash.
func RequestFingerprint(req *Request) string {
	if req == nil {
		return ""
	}
	if isStableID(req) {
		return req.ID
	}
	return RequestHash(req)
}

func requestIndexKeys(req *Request) []string {
	hash := RequestHash(req)
	keys := []string{}
	if hash != "" {
		keys = append(keys, "hash:"+hash)
	}
	if isStableID(req) && req.ID != "" {
		keys = append(keys, "id:"+req.ID)
	}
	return keys
}

func isStableID(req *Request) bool {
	if req == nil || req.ID == "" {
		return false
	}
	if req.OriginalID != "" {
		return true
	}
	return strings.HasPrefix(req.ID, "h_")
}
