package browserrpc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type callFunc func(context.Context, string, any, any) error

func (f callFunc) Call(ctx context.Context, method string, in, out any) error {
	return f(ctx, method, in, out)
}

func TestRoutedCDPPreservesGuardAndLeaseContract(t *testing.T) {
	caller := callFunc(func(_ context.Context, method string, input, output any) error {
		p := input.(map[string]any)
		if method != "browser.cdp" || p["frame_id"] != "child" || p["session_id"] != "session" || p["document_generation"] != "document" || p["generation"] != "epoch" || p["owner"] != "scope" || p["lease_id"] != "lease" || p["expected_root_url"] != "https://parent.test/" || p["expected_frame_url"] != "https://child.test/" {
			t.Fatalf("routing guard lost: %#v", p)
		}
		if p["keep_attached"] != false {
			t.Fatal("one command cannot own attachment lifetime")
		}
		point := p["frame_point"].(map[string]any)
		if point["x"] != float64(3) || point["y"] != float64(4) {
			t.Fatal("frame geometry lost")
		}
		return json.Unmarshal([]byte(`{"result":{"value":7}}`), output)
	})
	var result struct {
		Value int `json:"value"`
	}
	err := CDPRoute(context.Background(), caller, 12, Route{FrameID: "child", SessionID: "session", DocumentGeneration: "document", Generation: "epoch", Owner: "scope", LeaseID: "lease", ExpectedRootURL: "https://parent.test/", ExpectedFrameURL: "https://child.test/", FramePoint: &Point{X: 3, Y: 4}}, "Runtime.evaluate", map[string]any{"expression": "7"}, &result)
	if err != nil || result.Value != 7 {
		t.Fatalf("bad CDP decode: %+v %v", result, err)
	}
}

func TestInvalidLeaseAndMissingProtocolResponseFailClosed(t *testing.T) {
	caller := callFunc(func(_ context.Context, _ string, _ any, out any) error { return json.Unmarshal([]byte(`{}`), out) })
	if _, err := Acquire(context.Background(), caller, 12, "owner", "execute"); err == nil {
		t.Fatal("invalid lease accepted")
	}
	if err := CDP(context.Background(), caller, 12, "Page.getFrameTree", nil, new(any)); err == nil {
		t.Fatal("missing protocol response accepted")
	}
	calls := 0
	busy := callFunc(func(_ context.Context, _ string, _ any, _ any) error { calls++; return errors.New("tab_busy") })
	if _, err := Acquire(context.Background(), busy, 12, "owner", "execute"); err == nil || calls != 1 {
		t.Fatal("busy lease acquisition was retried")
	}
}
