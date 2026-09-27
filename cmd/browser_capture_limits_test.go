package cmd

import "testing"

func TestCaptureLimitsNegotiateBeforeCollection(t *testing.T) {
	handoff := browserCaptureHandoff{MaxRequests: 7, MaxRequestBytes: 4096, MaxSnapshotBytes: 8192}
	params := map[string]interface{}{"max_body_bytes": 8 << 20}
	handoff.applyLimits(params)
	limits, ok := params["capture_limits"].(map[string]interface{})
	if !ok || limits["max_requests"] != 7 || limits["max_total_body_bytes"] != int64(1024) || params["max_body_bytes"] != 512 {
		t.Fatalf("host budgets did not reach collection params: %#v", params)
	}
	if limits["max_native_backlog_bytes"] != 16<<20 || limits["max_events"] != 10000 {
		t.Fatal("capture queue/events are unbounded")
	}
}

func TestCaptureLimitsKeepSmallerExplicitBodyLimit(t *testing.T) {
	handoff := browserCaptureHandoff{MaxRequests: 1000000, MaxRequestBytes: 384 << 20, MaxSnapshotBytes: 512 << 20}
	params := map[string]interface{}{"max_body_bytes": 128}
	handoff.applyLimits(params)
	limits := params["capture_limits"].(map[string]interface{})
	if params["max_body_bytes"] != 128 || limits["max_requests"] != 10000 || limits["max_total_body_bytes"] != int64(64<<20) {
		t.Fatalf("negotiation expanded the explicit limit or defaults: %#v", params)
	}
}
