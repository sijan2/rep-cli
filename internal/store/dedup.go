package store

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// RequestHash returns a stable hash for de-duplication.
func RequestHash(req *Request) string {
	if req == nil {
		return ""
	}
	if req.RecordKind != "" {
		connectionID := ""
		if req.Stream != nil {
			connectionID = req.Stream.ConnectionID
		}
		// Typed connection identity cannot be inferred from an empty HTTP
		// method/body and a URL: simultaneous sockets commonly share those.
		identity := []any{req.RecordKind, connectionID, req.Method, req.URL, req.Body, req.Timestamp}
		if req.Stream != nil && req.Stream.Source != "" {
			// A page observer and CDP can expose independent records for the
			// same transport. Their labels do not establish a shared identity.
			identity = append(identity, req.Stream.Source)
		}
		if connectionID == "" {
			identity = append(identity, req.ID, req.OriginalID, req.SourceSessionID)
			if req.ID == "" && req.OriginalID == "" {
				identity = append(identity, req.Stream)
			}
		}
		hash := sha256.New()
		_ = json.NewEncoder(hash).Encode(identity)
		return fmt.Sprintf("%x", hash.Sum(nil))
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
