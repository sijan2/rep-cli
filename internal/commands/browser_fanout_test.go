package commands

import (
	"encoding/json"

	"testing"

	"github.com/repplus/rep-cli/internal/store"
)

func fanoutTestRequest() *store.Request {
	return &store.Request{
		ID: "h_source", Method: "get", URL: "https://api.example.test/search?q=chair&per_page=1",
		Headers: store.HeaderMap{"Accept": {"application/json"}, "Cookie": {"session=secret"}, "Authorization": {"Bearer token"}, "sec-fetch-mode": {"cors"}, "User-Agent": {"x"}},
	}
}

func TestFanoutOutcomesPairVariantsWithCapturedRequests(t *testing.T) {
	variants := []fanoutVariant{
		{Index: 0, Label: "chair", Method: "GET", URL: "https://api.example.test/s?q=chair"},
		{Index: 1, Label: "lamp", Method: "GET", URL: "https://api.example.test/s?q=lamp"},
		{Index: 2, Label: "p1", Method: "POST", URL: "https://api.example.test/graphql"},
		{Index: 3, Label: "p2", Method: "POST", URL: "https://api.example.test/graphql"},
		{Index: 4, Label: "missing", Method: "GET", URL: "https://api.example.test/s?q=missing"},
	}
	evaluation := json.RawMessage(`{"variants":[{"index":0,"status":200,"ok":true,"bytes":10,"content_type":"application/json","elapsed_ms":5},{"index":1,"status":429,"ok":false,"bytes":2,"elapsed_ms":7},{"index":2,"status":200,"ok":true,"bytes":3},{"index":3,"status":200,"ok":true,"bytes":4},{"index":4,"error":"Failed to fetch"}]}`)
	complete := &store.BodyCapture{State: "complete"}
	requests := []store.Request{
		{ID: "h_1", Method: "GET", URL: "https://api.example.test/s?q=chair", StartOrdinal: 1, ResponseBodyCapture: complete},
		{ID: "h_3", Method: "POST", URL: "https://api.example.test/graphql", StartOrdinal: 3},
		{ID: "h_2", Method: "GET", URL: "https://api.example.test/s?q=lamp#frag", StartOrdinal: 2, ResponseBodyCapture: &store.BodyCapture{State: "partial"}},
		{ID: "h_4", Method: "POST", URL: "https://api.example.test/graphql", StartOrdinal: 4},
		{ID: "ws", RecordKind: "websocket", Method: "GET", URL: "https://api.example.test/s?q=missing"},
	}
	outcomes := fanoutOutcomes(variants, evaluation, requests)
	if outcomes[0].RequestID != "h_1" || outcomes[0].BodyState != "complete" || !outcomes[0].OK || outcomes[0].Match != "method_url" {
		t.Fatalf("chair not paired: %+v", outcomes[0])
	}
	if outcomes[1].RequestID != "h_2" || outcomes[1].Status != 429 || outcomes[1].BodyState != "partial" {
		t.Fatalf("lamp not paired through fragment normalization: %+v", outcomes[1])
	}
	if outcomes[2].RequestID != "h_3" || outcomes[3].RequestID != "h_4" || outcomes[2].Match != "method_url_order" {
		t.Fatalf("same-URL posts must pair in start order: %+v %+v", outcomes[2], outcomes[3])
	}
	if outcomes[4].RequestID != "" || outcomes[4].Match != "unmatched" || outcomes[4].Error != "Failed to fetch" {
		t.Fatalf("stream records must never pair with HTTP variants: %+v", outcomes[4])
	}
}
