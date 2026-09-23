package main

import (
	"fmt"
	"os"
	"strconv"
)

type captureLimits struct {
	requestBytes       int64
	snapshotBytes      int64
	totalSnapshotBytes int64
	requests           int
}

var activeCaptureLimits = defaultCaptureLimits()

func defaultCaptureLimits() captureLimits {
	return captureLimits{requestBytes: 384 << 20, snapshotBytes: 512 << 20, totalSnapshotBytes: 1 << 30, requests: MaxLiveRequests}
}

func configureCaptureLimits() error {
	limits := defaultCaptureLimits()
	for _, option := range []struct {
		name  string
		value *int64
	}{
		{"REP_CAPTURE_MAX_REQUEST_BYTES", &limits.requestBytes},
		{"REP_CAPTURE_MAX_SNAPSHOT_BYTES", &limits.snapshotBytes},
		{"REP_CAPTURE_TOTAL_SNAPSHOT_BYTES", &limits.totalSnapshotBytes},
	} {
		if raw := os.Getenv(option.name); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value < 1024 || value > 1<<40 {
				return fmt.Errorf("%s must be an integer byte count between 1024 and 1099511627776", option.name)
			}
			*option.value = value
		}
	}
	if raw := os.Getenv("REP_CAPTURE_MAX_REQUESTS"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 1000000 {
			return fmt.Errorf("REP_CAPTURE_MAX_REQUESTS must be between 1 and 1000000")
		}
		limits.requests = value
	}
	if limits.totalSnapshotBytes < limits.snapshotBytes {
		return fmt.Errorf("REP_CAPTURE_TOTAL_SNAPSHOT_BYTES must be at least REP_CAPTURE_MAX_SNAPSHOT_BYTES")
	}
	activeCaptureLimits = limits
	return nil
}
