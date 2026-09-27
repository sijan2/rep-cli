package cmd

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

type protocolCaptureCaller struct {
	calls        int
	capabilities []string
}

func (c *protocolCaptureCaller) Call(_ context.Context, method string, _ interface{}, out interface{}) error {
	c.calls++
	data, _ := json.Marshal(map[string]any{"capabilities": c.capabilities})
	return json.Unmarshal(data, out)
}

func TestProtocolPayloadPreflightRejectsOldExtensionAndLeavesOrdinaryCaptureFast(t *testing.T) {
	c := &protocolCaptureCaller{}
	if err := requireProtocolPayloadCapture(context.Background(), c, false); err != nil || c.calls != 0 {
		t.Fatalf("ordinary capture added a preflight: %v", err)
	}
	if err := requireProtocolPayloadCapture(context.Background(), c, true); err == nil {
		t.Fatal("old extension accepted a requested unsupported collector")
	}
	c.capabilities = []string{"protocol_payloads_v1"}
	if err := requireProtocolPayloadCapture(context.Background(), c, true); err != nil {
		t.Fatal(err)
	}
}

func TestProtocolPayloadFlagOnlyAppliesToCapturedAction(t *testing.T) {
	var sent map[string]interface{}
	run := func(_ *cobra.Command, _, _ string, params map[string]interface{}, _ time.Duration) error {
		sent = params
		return nil
	}
	c := newBrowserEvaluationCommand(true, run)
	c.SetArgs([]string{"--tab", "7", "--protocol-payloads", "42"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	if sent["protocol_payloads"] != true {
		t.Fatal("capture flag was not handed to the extension")
	}
	plain := newBrowserEvaluationCommand(false, run)
	if plain.Flags().Lookup("protocol-payloads") != nil {
		t.Fatal("an uncaptured evaluation advertised payload capture")
	}
}

func TestMediaCollectorPreflightAndFlag(t *testing.T) {
	c := &protocolCaptureCaller{capabilities: []string{"protocol_payloads_v1"}}
	if err := requireBrowserCollectors(context.Background(), c, true, true); err == nil || c.calls != 1 {
		t.Fatalf("missing media capability was accepted or duplicated preflight: %v", err)
	}
	c.capabilities = append(c.capabilities, "webrtc_media_v1")
	if err := requireBrowserCollectors(context.Background(), c, true, true); err != nil || c.calls != 2 {
		t.Fatalf("combined collectors need one preflight: %v", err)
	}
	var sent map[string]interface{}
	run := func(_ *cobra.Command, _, _ string, params map[string]interface{}, _ time.Duration) error {
		sent = params
		return nil
	}
	command := newBrowserEvaluationCommand(true, run)
	command.SetArgs([]string{"--tab", "7", "--webrtc-media", "42"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if sent["webrtc_media"] != true || sent["protocol_payloads"] != false {
		t.Fatal("media flag must independently reach the capture collector")
	}
	if newBrowserEvaluationCommand(false, run).Flags().Lookup("webrtc-media") != nil {
		t.Fatal("uncaptured evaluation advertised media capture")
	}
}
