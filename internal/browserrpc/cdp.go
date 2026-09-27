// Package browserrpc owns the shared wire contract for browser runtime clients.
// Callers choose scope, transport, deadlines, and attachment ownership.
package browserrpc

import (
	"context"
	"encoding/json"
	"errors"
)

type Caller interface {
	Call(context.Context, string, any, any) error
}

// Route identifies the document and debugger session observed by a caller. The
// extension resolves same-process frame contexts and out-of-process sessions;
// callers must never fall back to the root document when this route is stale.
type Route struct {
	FrameID            string `json:"frame_id,omitempty"`
	SessionID          string `json:"session_id,omitempty"`
	DocumentGeneration string `json:"document_generation,omitempty"`
	Generation         string `json:"generation,omitempty"`
	LeaseID            string `json:"lease_id,omitempty"`
	Owner              string `json:"owner,omitempty"`
	ExpectedRootURL    string `json:"expected_root_url,omitempty"`
	ExpectedFrameURL   string `json:"expected_frame_url,omitempty"`
	FramePoint         *Point `json:"frame_point,omitempty"`
}

type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// CDP preserves existing attachment ownership and decodes one protocol response.
func CDP(ctx context.Context, caller Caller, tab int, method string, params any, out any) error {
	return CDPRoute(ctx, caller, tab, Route{}, method, params, out)
}

func CDPRoute(ctx context.Context, caller Caller, tab int, route Route, method string, params any, out any) error {
	var wrapper struct {
		Result json.RawMessage `json:"result"`
	}
	wire := map[string]any{"tab_id": tab, "method": method, "command_params": params, "keep_attached": false}
	data, err := json.Marshal(route)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if err := caller.Call(ctx, "browser.cdp", wire, &wrapper); err != nil {
		return err
	}
	if len(wrapper.Result) == 0 {
		return errors.New("browser returned no CDP result")
	}
	return json.Unmarshal(wrapper.Result, out)
}
