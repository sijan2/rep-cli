package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/store"
)

func fanoutTestRequest() *store.Request {
	return &store.Request{
		ID: "h_source", Method: "get", URL: "https://api.example.test/search?q=chair&per_page=1",
		Headers: store.HeaderMap{"Accept": {"application/json"}, "Cookie": {"session=secret"}, "Authorization": {"Bearer token"}, "sec-fetch-mode": {"cors"}, "User-Agent": {"x"}},
	}
}

func TestFanoutVariantsStayOnOriginAndDropForbiddenHeaders(t *testing.T) {
	key, values, err := parseMatrixSpec("query.q=[chair, lamp,desk]")
	if err != nil {
		t.Fatal(err)
	}
	variants, origin, err := fanoutVariants(fanoutTestRequest(), key, values, []mutation{{kind: "header", key: "X-Debug", value: "1"}})
	if err != nil || origin != "https://api.example.test" || len(variants) != 3 {
		t.Fatalf("variants not built: %v %s %d", err, origin, len(variants))
	}
	if variants[1].Label != "lamp" || !strings.Contains(variants[1].URL, "q=lamp") || !strings.Contains(variants[1].URL, "per_page=1") || variants[1].Method != "GET" || variants[1].Body != nil {
		t.Fatalf("query mutation wrong: %+v", variants[1])
	}
	headers := variants[0].Headers
	if headers["authorization"] != "Bearer token" || headers["accept"] != "application/json" || headers["x-debug"] != "1" {
		t.Fatalf("allowed headers lost: %v", headers)
	}
	for _, forbidden := range []string{"cookie", "sec-fetch-mode", "user-agent"} {
		if _, present := headers[forbidden]; present {
			t.Fatalf("forbidden header forwarded: %s", forbidden)
		}
	}
	post := fanoutTestRequest()
	post.Method = "POST"
	post.Body = `{"query":"chair","page":1}`
	post.Headers["Content-Type"] = []string{"application/json"}
	bodyKey, bodyValues, _ := parseMatrixSpec("body.page=[1,2]")
	variants, _, err = fanoutVariants(post, bodyKey, bodyValues, nil)
	if err != nil || variants[1].Body == nil || !strings.Contains(*variants[1].Body, `"page":"2"`) || variants[1].Headers["content-type"] != "application/json" {
		t.Fatalf("body mutation wrong: %v %+v", err, variants)
	}
	if _, _, err := fanoutVariants(fanoutTestRequest(), "query.q", make([]string, maxFanoutVariants+1), nil); err == nil {
		t.Fatal("variant limit not enforced")
	}
}

func TestFanoutScriptEmbedsJobSafely(t *testing.T) {
	separator := string(rune(0x2028))
	variants := []fanoutVariant{{Index: 0, Label: "a" + separator + "b</script>", Method: "GET", URL: "https://api.example.test/x?q=a"}}
	script, err := fanoutScript(variants, 4)
	if err != nil {
		t.Fatal(err)
	}
	escaped := string([]byte{'\\', 'u'}) + "2028"
	if strings.Contains(script, separator) || !strings.Contains(script, escaped) || !strings.Contains(script, `"concurrency":4`) || !strings.Contains(script, "arrayBuffer()") {
		t.Fatalf("script not safe or incomplete:\n%s", script)
	}
	start := strings.LastIndex(script, "})(") + 3
	var job map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(script[start:], ")")), &job); err != nil {
		t.Fatalf("embedded job is not JSON: %v", err)
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

func TestFanoutEvaluationValueAcceptsBridgeShapes(t *testing.T) {
	payload := map[string]any{"variants": []map[string]any{{"index": 0, "status": 200}}, "duration_ms": 5}
	for name, raw := range map[string]any{
		"runtime evaluate reply": map[string]any{"result": map[string]any{"type": "object", "value": payload}},
		"bare value":             map[string]any{"value": payload},
		"plain object":           payload,
	} {
		value := fanoutEvaluationValue(raw)
		var decoded struct {
			Variants []map[string]any `json:"variants"`
		}
		if json.Unmarshal(value, &decoded) != nil || len(decoded.Variants) != 1 || decoded.Variants[0]["status"] != float64(200) {
			t.Fatalf("%s: value not extracted: %s", name, value)
		}
	}
	if fanoutEvaluationValue(map[string]any{"result": map[string]any{"type": "object", "description": "Object"}}) != nil {
		t.Fatal("a bounded summary must not be mistaken for results")
	}
	if fanoutEvaluationValue(nil) != nil {
		t.Fatal("missing evaluation must be nil")
	}
}

func TestFanoutReplaysFromTheInitiatingDocument(t *testing.T) {
	source := &store.Request{ID: "h_gql", Method: "POST", URL: "https://gql.example-api.test/graphql", PageURL: "https://www.example.test/search?q=chair", Body: `{"variables":{"query":"chair"}}`, Headers: store.HeaderMap{"Content-Type": {"application/json"}}}
	key, values, _ := parseMatrixSpec("body.variables.query=[chair,lamp]")
	variants, origin, err := fanoutVariants(source, key, values, nil)
	if err != nil || origin != "https://www.example.test" {
		t.Fatalf("replay origin must be the initiating page, got %q (%v)", origin, err)
	}
	if !strings.HasPrefix(variants[1].URL, "https://gql.example-api.test/") || !strings.Contains(*variants[1].Body, `"query":"lamp"`) {
		t.Fatalf("variants keep the request's own target origin: %+v", variants[1])
	}
	plain := &store.Request{ID: "h_api", Method: "GET", URL: "https://api.example.test/s?q=a"}
	if _, origin, err := fanoutVariants(plain, "query.q", []string{"a"}, nil); err != nil || origin != "https://api.example.test" {
		t.Fatalf("without a page url the target origin is the document: %q %v", origin, err)
	}
}
