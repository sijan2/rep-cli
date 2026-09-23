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

// CDP preserves existing attachment ownership and decodes one protocol response.
func CDP(ctx context.Context, caller Caller, tab int, method string, params any, out any) error {
	var wrapper struct {
		Result json.RawMessage `json:"result"`
	}
	if err := caller.Call(ctx, "browser.cdp", map[string]any{"tab_id": tab, "method": method, "command_params": params, "keep_attached": false}, &wrapper); err != nil {
		return err
	}
	if len(wrapper.Result) == 0 {
		return errors.New("browser returned no CDP result")
	}
	return json.Unmarshal(wrapper.Result, out)
}
