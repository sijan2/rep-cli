package commands

import (
	"encoding/json"
	"testing"
)

func assertListJSONObject(t *testing.T, data []byte) {
	t.Helper()
	var payload struct {
		Source   string            `json:"source"`
		Requests []json.RawMessage `json:"requests"`
		Complete bool              `json:"complete"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("output is not one JSON document: %v\n%s", err, data)
	}
	if payload.Source != "live.json" || payload.Requests == nil || len(payload.Requests) != 0 || !payload.Complete {
		t.Fatalf("unexpected result: %#v", payload)
	}
}
