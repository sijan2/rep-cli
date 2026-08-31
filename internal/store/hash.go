package store

import (
	"crypto/sha256"
	"encoding/hex"
)

// GenerateHashID creates a short, stable hash identifier with an optional prefix.
func GenerateHashID(prefix string, parts ...string) string {
	hasher := sha256.New()
	for _, part := range parts {
		if part == "" {
			continue
		}
		hasher.Write([]byte(part))
		hasher.Write([]byte{0})
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	short := sum
	if len(sum) > 10 {
		short = sum[:10]
	}
	if prefix == "" {
		return short
	}
	return prefix + "_" + short
}
