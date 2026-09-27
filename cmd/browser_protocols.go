package cmd

import (
	"context"
	"fmt"
)

// Check before navigation/evaluation: an older extension must not silently
// accept a requested collector it cannot install. Ordinary captures add no RPC.
func requireProtocolPayloadCapture(ctx context.Context, client browserCaptureCaller, requested bool) error {
	return requireBrowserCollectors(ctx, client, requested, false)
}

func requireBrowserCollectors(ctx context.Context, client browserCaptureCaller, protocolPayloads, webrtcMedia bool) error {
	if !protocolPayloads && !webrtcMedia {
		return nil
	}
	var status struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := client.Call(ctx, "browser.status", nil, &status); err != nil {
		return fmt.Errorf("check browser collectors: %w", err)
	}
	if protocolPayloads && !hasCapability(status.Capabilities, "protocol_payloads_v1") {
		return fmt.Errorf("--protocol-payloads requires the updated extension; finish active captures, reload it, and check browser status")
	}
	if webrtcMedia && !hasCapability(status.Capabilities, "webrtc_media_v1") {
		return fmt.Errorf("--webrtc-media requires the updated extension; finish active captures, reload it, and check browser status")
	}
	return nil
}
