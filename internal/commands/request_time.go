package commands

import (
	"time"
)

// parseSinceTime parses duration string (5m, 1h, 30s) and returns cutoff timestamp in ms
func parseSinceTime(since string) int64 {
	d, err := time.ParseDuration(since)
	if err != nil {
		return 0
	}
	return time.Now().Add(-d).UnixMilli()
}
