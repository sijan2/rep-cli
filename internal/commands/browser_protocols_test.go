package commands

import (
	"context"
	"encoding/json"
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
