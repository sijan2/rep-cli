package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
)

func TestEmitListNoPrimaryHonorsJSONMode(t *testing.T) {
	var buffer bytes.Buffer
	if err := emitListNoPrimary(&buffer, "live.json", true, true); err != nil {
		t.Fatal(err)
	}
	assertListJSONEnvelope(t, buffer.Bytes())
}

func TestEmitListEmptyResultHonorsJSONMode(t *testing.T) {
	var buffer bytes.Buffer
	err := emitListEmptyResult(&buffer, output.EmptyResultContext{
		Command:         "list",
		Source:          "live.json",
		TotalCandidates: 3,
		Filters:         store.FilterOptions{PrimaryOnly: true, Method: "POST"},
		SampleDomains:   []string{"example.test"},
	}, true, true)
	if err != nil {
		t.Fatal(err)
	}
	assertListJSONEnvelope(t, buffer.Bytes())
}

func TestEmitListNoPrimaryHonorsRawJSONMode(t *testing.T) {
	var buffer bytes.Buffer
	if err := emitListNoPrimary(&buffer, "live.json", true, false); err != nil {
		t.Fatal(err)
	}
	var payload []json.RawMessage
	if err := json.Unmarshal(buffer.Bytes(), &payload); err != nil {
		t.Fatalf("raw output is not one JSON array: %v\n%s", err, buffer.Bytes())
	}
	if len(payload) != 0 {
		t.Fatalf("expected empty raw result, got %d entries", len(payload))
	}
}

func TestResolvedListOutputModeTreatsJSONFlagAsOutputJSON(t *testing.T) {
	if got := resolveListOutputMode("compact", true); got != store.OutputJSON {
		t.Fatalf("-j resolved to %q, want %q", got, store.OutputJSON)
	}
	if got := resolveListOutputMode("json", false); got != store.OutputJSON {
		t.Fatalf("--output json resolved to %q, want %q", got, store.OutputJSON)
	}
}

func assertListJSONEnvelope(t *testing.T, data []byte) {
	t.Helper()
	var payload struct {
		Source  string            `json:"source"`
		Command string            `json:"command"`
		Data    []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("output is not one JSON document: %v\n%s", err, data)
	}
	if payload.Source != "live.json" || payload.Command != "list" || len(payload.Data) != 0 {
		t.Fatalf("unexpected envelope: %#v", payload)
	}
}
