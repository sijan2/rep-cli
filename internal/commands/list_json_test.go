package commands

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
	assertListJSONObject(t, buffer.Bytes())
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
	assertListJSONObject(t, buffer.Bytes())
}

func TestEmitListNoPrimaryHonorsRawJSONMode(t *testing.T) {
	var buffer bytes.Buffer
	if err := emitListNoPrimary(&buffer, "live.json", true, false); err != nil {
		t.Fatal(err)
	}
	assertListJSONObject(t, buffer.Bytes())
}

func TestResolvedListOutputModeTreatsJSONFlagAsOutputJSON(t *testing.T) {
	if got := resolveListOutputMode("compact", true); got != store.OutputJSON {
		t.Fatalf("-j resolved to %q, want %q", got, store.OutputJSON)
	}
	if got := resolveListOutputMode("json", false); got != store.OutputJSON {
		t.Fatalf("--output json resolved to %q, want %q", got, store.OutputJSON)
	}
}

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
