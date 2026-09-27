package cmd

// Carry host budgets into the collector before it allocates payload buffers.
// Serialization can expand text/base64, so leave conservative snapshot room.
// Host serialized-byte limits remain authoritative and fail publication.
func (handoff browserCaptureHandoff) applyLimits(params map[string]interface{}) {
	requests := 10000
	if handoff.MaxRequests > 0 && handoff.MaxRequests < requests {
		requests = handoff.MaxRequests
	}
	body := int64(64 << 20)
	if handoff.MaxSnapshotBytes > 0 && handoff.MaxSnapshotBytes/8 < body {
		body = handoff.MaxSnapshotBytes / 8
	}
	params["capture_limits"] = map[string]interface{}{
		"max_requests": requests, "max_total_body_bytes": body, "max_events": 10000, "max_native_backlog_bytes": 16 << 20,
	}
	if handoff.MaxRequestBytes > 0 {
		if configured, ok := params["max_body_bytes"].(int); ok && int64(configured) > handoff.MaxRequestBytes/8 {
			params["max_body_bytes"] = int(handoff.MaxRequestBytes / 8)
		}
	}
}
