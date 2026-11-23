package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeCaller replays canned JSON per method and records the params it saw as
// plain maps, the way the extension would receive them.
type fakeCaller struct {
	t       *testing.T
	script  func(method string, params map[string]any) (string, error)
	methods []string
	params  []map[string]any
	ctxs    []context.Context
	ctxErrs []error // ctx.Err() as seen during the call, before Read's deferred cancel
}

func (f *fakeCaller) Call(ctx context.Context, method string, params any, out any) error {
	data, err := json.Marshal(params)
	if err != nil {
		f.t.Fatalf("params not JSON: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		f.t.Fatalf("params not an object: %v", err)
	}
	f.methods = append(f.methods, method)
	f.params = append(f.params, m)
	f.ctxs = append(f.ctxs, ctx)
	f.ctxErrs = append(f.ctxErrs, ctx.Err())
	reply, err := f.script(method, m)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(reply), out)
}

func TestHeaderValueUnmarshal(t *testing.T) {
	cases := []struct {
		in   string
		want HeaderValue
		err  bool
	}{
		{`"text/html"`, HeaderValue{Value: "text/html"}, false},
		{`""`, HeaderValue{}, false},
		{`null`, HeaderValue{}, false},
		{`{"redacted":true,"bytes":41,"sha8":"0a1b2c3d"}`, HeaderValue{Redacted: true, Bytes: 41, SHA8: "0a1b2c3d"}, false},
		{`{"redacted":true}`, HeaderValue{Redacted: true}, false},
		{`{"redacted":true,"bytes":3,"sha8":"ab","future":1}`, HeaderValue{Redacted: true, Bytes: 3, SHA8: "ab"}, false},
		{`{"redacted":false,"value":"x"}`, HeaderValue{}, true},
		{`{"bytes":3}`, HeaderValue{}, true},
		{`42`, HeaderValue{}, true},
		{`["a"]`, HeaderValue{}, true},
	}
	for _, tc := range cases {
		var v HeaderValue
		err := v.UnmarshalJSON([]byte(tc.in))
		if (err != nil) != tc.err || v != tc.want {
			t.Errorf("%s -> %+v %v, want %+v err=%v", tc.in, v, err, tc.want, tc.err)
		}
	}
	for _, v := range []HeaderValue{{Value: "plain"}, {Redacted: true, Bytes: 9, SHA8: "deadbeef"}, {Redacted: true}} {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var back HeaderValue
		if err := json.Unmarshal(data, &back); err != nil || back != v {
			t.Fatalf("round trip %+v -> %s -> %+v", v, data, back)
		}
	}
	if (HeaderValue{Redacted: true, Bytes: 9, SHA8: "deadbeef"}).String() != "redacted;bytes=9;sha8=deadbeef" || (HeaderValue{Redacted: true}).String() != "redacted" {
		t.Fatal("marker rendering changed")
	}
}

func TestHeaderPairUnmarshal(t *testing.T) {
	cases := []struct {
		in   string
		want Header
		err  bool
	}{
		{`["content-type","text/html"]`, Header{Name: "content-type", Value: HeaderValue{Value: "text/html"}}, false},
		{`["authorization",{"redacted":true,"bytes":12,"sha8":"ffffffff"}]`, Header{Name: "authorization", Value: HeaderValue{Redacted: true, Bytes: 12, SHA8: "ffffffff"}}, false},
		{`{"name":"a","value":"b"}`, Header{}, true},
		{`["only-name"]`, Header{}, true},
		{`["a","b","c"]`, Header{}, true},
		{`[1,"b"]`, Header{}, true},
	}
	for _, tc := range cases {
		var h Header
		err := json.Unmarshal([]byte(tc.in), &h)
		if (err != nil) != tc.err || h != tc.want {
			t.Errorf("%s -> %+v %v", tc.in, h, err)
		}
	}
	data, _ := json.Marshal(Header{Name: "cookie", Value: HeaderValue{Redacted: true, Bytes: 5, SHA8: "01234567"}})
	if string(data) != `["cookie",{"redacted":true,"bytes":5,"sha8":"01234567"}]` {
		t.Fatalf("header marshal = %s", data)
	}
}

func TestOpenShapesParams(t *testing.T) {
	f := &fakeCaller{t: t, script: func(method string, p map[string]any) (string, error) {
		return `{"ledger_id":"L1","seq":0,"main_frame_id":"F1","attached":true,"already_attached":false}`, nil
	}}
	policy := map[string]any{
		"owner": "stale-owner", "policy_id": "reddit",
		"body_ops":      []map[string]any{{"op": "thread", "host": "reddit.com", "method": "GET", "path_re": "^/r/", "types": []string{"Document"}}},
		"private_paths": []string{"/message/"}, "max_body_bytes": 8 << 20, "ring_bytes": 64 << 20, "idle_ms": 120000,
	}
	c, err := Open(context.Background(), f, 77, "agent-1", policy)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "L1" || c.MainFrame != "F1" || c.Tab != 77 || c.Owner != "agent-1" || c.Caller != f {
		t.Fatalf("client = %+v", c)
	}
	p := f.params[0]
	if f.methods[0] != "browser.ledger.open" || p["tab_id"] != float64(77) || p["owner"] != "agent-1" || p["policy_id"] != "reddit" {
		t.Fatalf("open params = %v", p)
	}
	if policy["owner"] != "stale-owner" {
		t.Fatal("Open mutated the caller's params map")
	}
	for _, key := range []string{"body_ops", "private_paths", "max_body_bytes", "ring_bytes", "idle_ms"} {
		if _, ok := p[key]; !ok {
			t.Fatalf("policy key %s dropped", key)
		}
	}
	empty := &fakeCaller{t: t, script: func(string, map[string]any) (string, error) { return `{"seq":0}`, nil }}
	if _, err := Open(context.Background(), empty, 1, "o", nil); err == nil {
		t.Fatal("open without ledger_id accepted")
	}
	failing := &fakeCaller{t: t, script: func(string, map[string]any) (string, error) { return "", errors.New("not_owned") }}
	if _, err := Open(context.Background(), failing, 1, "o", nil); err == nil || err.Error() != "not_owned" {
		t.Fatalf("rpc error not surfaced: %v", err)
	}
	if _, err := Open(context.Background(), nil, 1, "o", nil); err == nil {
		t.Fatal("nil caller accepted")
	}
}

const batchJSON = `{
  "entries": [
    {"seq": 1, "rev": 2, "request_id": "R1", "loader_id": "LD", "frame_id": "F1", "type": "Document", "method": "GET",
     "url": "https://www.reddit.com/r/golang/comments/1abc2de/slug/?token=redacted",
     "initiator": {"type": "other", "url": ""},
     "request": {"headers": [["accept", "text/html"], ["cookie", {"redacted": true, "bytes": 212, "sha8": "c00c1e00"}], ["authorization", {"redacted": true, "bytes": 44, "sha8": "a0a0a0a0"}]]},
     "response": {"status": 200, "mime": "text/html", "protocol": "h2", "from_sw": false, "from_cache": true,
                  "headers": [["content-type", "text/html; charset=utf-8"], ["x-ratelimit-remaining", "996.0"], ["x-ratelimit-reset", "123"], ["retry-after", "7"]]},
     "body": {"state": "complete", "bytes": 1234, "sha256": "abc", "encoding": "base64"},
     "t": {"start": 1000, "response": 1100, "end": 1200}},
    {"seq": 2, "rev": 1, "request_id": "R2", "loader_id": "LD", "frame_id": "F1", "type": "Fetch", "method": "POST",
     "url": "https://www.reddit.com/svc/shreddit/more-comments/t3_1abc2de/t1_x",
     "initiator": {"type": "script", "url": "https://www.redditstatic.com/shreddit/en-US/app.js"},
     "request": {"headers": [["content-type", "application/x-www-form-urlencoded"], ["x-csrf-token", {"redacted": true, "bytes": 40, "sha8": "c5f0c5f0"}], ["x-client-transaction-id", {"redacted": true, "bytes": 90, "sha8": "7a7a7a7a"}]],
                 "post_data": "sort=confidence&csrf_token=redacted&depth=5"},
     "response": {"status": 429, "mime": "text/html", "protocol": "h2", "from_sw": false, "from_cache": false, "headers": [["retry-after", "30"], ["cf-mitigated", "challenge"]]},
     "body": {"state": "skipped", "reason": "too_large", "bytes": 9000000},
     "t": {"start": 2000}},
    {"seq": 3, "request_id": "R3", "type": "XHR", "method": "POST", "url": "https://www.reddit.com/svc/shreddit/graphql",
     "request": {"headers": [], "post_data": {"redacted": true, "bytes": 512, "sha8": "f00dface"}},
     "body": {"state": "pending", "bytes": 0}, "error": "net::ERR_ABORTED", "t": {"start": 3000}}
  ],
  "navs": [{"seq": 1, "url": "https://www.reddit.com/r/golang/comments/1abc2de/slug/", "same_document": false, "loader_id": "LD", "frame_id": "F1"}],
  "next_seq": 4, "state": "open",
  "tab": {"url": "https://www.reddit.com/r/golang/comments/1abc2de/slug/", "active": false},
  "stats": {"entries": 3, "bodies": 1, "body_bytes": 1234, "evicted": 0, "skipped_by_reason": {"too_large": 1, "not_admitted": 4}}
}`

func newClient(t *testing.T, script func(method string, p map[string]any) (string, error)) (*Client, *fakeCaller) {
	f := &fakeCaller{t: t, script: script}
	return &Client{Caller: f, Tab: 5, Owner: "agent-1", ID: "L1", MainFrame: "F1"}, f
}

func TestReadDecodesBatch(t *testing.T) {
	c, f := newClient(t, func(string, map[string]any) (string, error) { return batchJSON, nil })
	b, err := c.Read(context.Background(), 0, Match{URLRe: "/comments/", Method: "GET", Types: []string{"Document", "Fetch"}, AdmittedOnly: true, Phase: "end"}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	p := f.params[0]
	if f.methods[0] != "browser.ledger.read" || p["tab_id"] != float64(5) || p["ledger_id"] != "L1" || p["owner"] != "agent-1" || p["since"] != float64(0) || p["wait_ms"] != float64(2000) {
		t.Fatalf("read params = %v", p)
	}
	match := p["match"].(map[string]any)
	if match["url_re"] != "/comments/" || match["method"] != "GET" || match["admitted_only"] != true || match["phase"] != "end" || len(match["types"].([]any)) != 2 {
		t.Fatalf("match = %v", match)
	}
	if b.NextSeq != 4 || b.State != "open" || b.Tab.URL == "" || b.Tab.Active || len(b.Navs) != 1 || b.Navs[0].SameDocument || b.Navs[0].LoaderID != "LD" {
		t.Fatalf("batch = %+v", b)
	}
	if b.Stats.Entries != 3 || b.Stats.BodyBytes != 1234 || b.Stats.SkippedByReason["not_admitted"] != 4 {
		t.Fatalf("stats = %+v", b.Stats)
	}
	if len(b.Entries) != 3 {
		t.Fatalf("entries = %d", len(b.Entries))
	}
	e := b.Entries[0]
	if e.Seq != 1 || e.Rev != 2 || e.RequestID != "R1" || e.LoaderID != "LD" || e.FrameID != "F1" || e.Type != "Document" || e.Method != "GET" || e.Initiator.Type != "other" {
		t.Fatalf("entry 1 = %+v", e)
	}
	if e.Response == nil || e.Response.Status != 200 || !e.Response.FromCache || e.Response.FromSW || e.Response.Protocol != "h2" || e.Response.Mime != "text/html" {
		t.Fatalf("response = %+v", e.Response)
	}
	if e.Body.State != "complete" || e.Body.Bytes != 1234 || e.Body.SHA256 != "abc" || e.T.Start != 1000 || e.T.Response != 1100 || e.T.End != 1200 {
		t.Fatalf("body/t = %+v %+v", e.Body, e.T)
	}
	if v, ok := e.Header("Cookie"); !ok || !v.Redacted || v.Bytes != 212 || v.SHA8 != "c00c1e00" || v.Value != "" {
		t.Fatalf("cookie = %+v %v", v, ok)
	}
	if v, ok := e.Header("ACCEPT"); !ok || v.Value != "text/html" {
		t.Fatalf("accept = %+v", v)
	}
	if _, ok := e.Header("x-missing"); ok {
		t.Fatal("missing header found")
	}
	if v, ok := e.ResponseHeader("Content-Type"); !ok || v.Value != "text/html; charset=utf-8" {
		t.Fatalf("response content-type = %+v", v)
	}
	h := e.HTTPHeader()
	if h.Get("X-Ratelimit-Remaining") != "996.0" || h.Get("Retry-After") != "7" || h.Get("x-ratelimit-reset") != "123" {
		t.Fatalf("http header = %v", h)
	}
	e2 := b.Entries[1]
	if e2.Request.PostData.Value != "sort=confidence&csrf_token=redacted&depth=5" || e2.Request.PostData.Redacted {
		t.Fatalf("post_data = %+v", e2.Request.PostData)
	}
	if e2.Body.State != "skipped" || e2.Body.Reason != "too_large" || e2.Response.Status != 429 || e2.T.End != 0 {
		t.Fatalf("entry 2 = %+v", e2)
	}
	req := e2.RequestHTTPHeader()
	if req.Get("X-Client-Transaction-Id") != "redacted;bytes=90;sha8=7a7a7a7a" {
		t.Fatalf("request header marker = %q", req.Get("X-Client-Transaction-Id"))
	}
	e3 := b.Entries[2]
	if e3.Response != nil || e3.Error != "net::ERR_ABORTED" || !e3.Request.PostData.Redacted || e3.Request.PostData.Bytes != 512 || e3.Request.PostData.SHA8 != "f00dface" {
		t.Fatalf("entry 3 = %+v", e3)
	}
	if e3.HTTPHeader() == nil || len(e3.HTTPHeader()) != 0 {
		t.Fatal("no response must yield an empty header")
	}
	if _, ok := e3.ResponseHeader("x"); ok {
		t.Fatal("no response has no headers")
	}
}

func TestReadOmitsEmptyMatch(t *testing.T) {
	c, f := newClient(t, func(string, map[string]any) (string, error) {
		return `{"entries":[],"navs":[],"next_seq":0,"state":"open"}`, nil
	})
	if _, err := c.Read(context.Background(), 9, Match{}, 0); err != nil {
		t.Fatal(err)
	}
	if _, has := f.params[0]["match"]; has {
		t.Fatalf("empty match sent: %v", f.params[0])
	}
	if f.params[0]["since"] != float64(9) || f.params[0]["wait_ms"] != float64(0) {
		t.Fatalf("params = %v", f.params[0])
	}
}

func TestReadCapsWaitAndDerivesDeadline(t *testing.T) {
	c, f := newClient(t, func(string, map[string]any) (string, error) {
		return `{"entries":[],"navs":[],"next_seq":0,"state":"open"}`, nil
	})
	before := time.Now()
	if _, err := c.Read(context.Background(), 0, Match{}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if f.params[0]["wait_ms"] != float64(10000) {
		t.Fatalf("wait not capped: %v", f.params[0]["wait_ms"])
	}
	deadline, has := f.ctxs[0].Deadline()
	if !has {
		t.Fatal("no deadline derived")
	}
	if d := deadline.Sub(before); d < 14*time.Second || d > 16*time.Second {
		t.Fatalf("deadline = wait+%v, want +15s", d-10*time.Second)
	}
	if f.ctxErrs[0] != nil {
		t.Fatal("derived context cancelled before the call")
	}
	if f.ctxs[0].Err() == nil {
		t.Fatal("derived context must be released once Read returns")
	}
	// Negative waits are clamped to zero; the deadline is then the 5 s slack.
	c.Read(context.Background(), 0, Match{}, -time.Second)
	if f.params[1]["wait_ms"] != float64(0) {
		t.Fatalf("negative wait = %v", f.params[1]["wait_ms"])
	}
	if d, _ := f.ctxs[1].Deadline(); d.Sub(before) > 6*time.Second {
		t.Fatalf("zero-wait deadline = %v", d.Sub(before))
	}
	// A caller's own deadline is respected, not replaced.
	own, cancel := context.WithDeadline(context.Background(), before.Add(time.Hour))
	defer cancel()
	c.Read(own, 0, Match{}, time.Second)
	if d, _ := f.ctxs[2].Deadline(); !d.Equal(before.Add(time.Hour)) {
		t.Fatalf("caller deadline replaced with %v", d)
	}
	failing, _ := newClient(t, func(string, map[string]any) (string, error) { return "", errors.New("ledger_lost") })
	if _, err := failing.Read(context.Background(), 0, Match{}, 0); err == nil || err.Error() != "ledger_lost" {
		t.Fatalf("rpc error not surfaced: %v", err)
	}
}

func bodyScript(t *testing.T, body []byte, sha string, pageHook func(offset int, page []byte) []byte) func(string, map[string]any) (string, error) {
	return func(method string, p map[string]any) (string, error) {
		if method != "browser.ledger.body" {
			t.Fatalf("unexpected method %s", method)
		}
		if p["tab_id"] != float64(5) || p["ledger_id"] != "L1" || p["owner"] != "agent-1" || p["seq"] != float64(42) {
			t.Fatalf("body params = %v", p)
		}
		offset, length := int(p["offset"].(float64)), int(p["length"].(float64))
		end := offset + length
		if end > len(body) {
			end = len(body)
		}
		if offset > len(body) {
			offset = len(body)
		}
		page := body[offset:end]
		if pageHook != nil {
			page = pageHook(offset, page)
		}
		reply, _ := json.Marshal(map[string]any{"seq": 42, "offset": offset, "length": len(page), "total": len(body), "sha256": sha, "encoding": "base64", "data": base64.StdEncoding.EncodeToString(page)})
		return string(reply), nil
	}
}

func shaOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestBodyPagesByOffsetAndVerifies(t *testing.T) {
	old := bodyPage
	bodyPage = 1000
	defer func() { bodyPage = old }()
	body := make([]byte, 2500)
	for i := range body {
		body[i] = byte(i * 7)
	}
	c, f := newClient(t, bodyScript(t, body, strings.ToUpper(shaOf(body)), nil))
	got, sha, err := c.Body(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) || sha != shaOf(body) {
		t.Fatalf("body mismatch: %d bytes sha %s", len(got), sha)
	}
	var offsets []float64
	for _, p := range f.params {
		offsets = append(offsets, p["offset"].(float64))
		if p["length"] != float64(1000) {
			t.Fatalf("page length = %v", p["length"])
		}
	}
	if fmt.Sprint(offsets) != "[0 1000 2000]" {
		t.Fatalf("offsets = %v", offsets)
	}
	// An exact multiple needs no extra empty page; an empty body needs one call.
	exact := body[:2000]
	c, f = newClient(t, bodyScript(t, exact, shaOf(exact), nil))
	if _, _, err := c.Body(context.Background(), 42); err != nil || len(f.params) != 2 {
		t.Fatalf("exact multiple: %v calls=%d", err, len(f.params))
	}
	c, f = newClient(t, bodyScript(t, nil, shaOf(nil), nil))
	if b, _, err := c.Body(context.Background(), 42); err != nil || len(b) != 0 || len(f.params) != 1 {
		t.Fatalf("empty body: %v %d calls=%d", err, len(b), len(f.params))
	}
}

func TestBodyRejectsBadPages(t *testing.T) {
	old := bodyPage
	bodyPage = 1000
	defer func() { bodyPage = old }()
	body := []byte(strings.Repeat("abcdefghij", 250))
	cases := map[string]func(string, map[string]any) (string, error){
		"sha mismatch": bodyScript(t, body, shaOf([]byte("other")), nil),
		"corrupted page": bodyScript(t, body, shaOf(body), func(offset int, page []byte) []byte {
			if offset == 1000 {
				page = append([]byte{}, page...)
				page[0] ^= 1
			}
			return page
		}),
		"empty page before total": bodyScript(t, body, shaOf(body), func(offset int, page []byte) []byte {
			if offset == 1000 {
				return nil
			}
			return page
		}),
		"wrong encoding": func(string, map[string]any) (string, error) {
			return `{"seq":42,"offset":0,"length":1,"total":1,"sha256":"x","encoding":"hex","data":"61"}`, nil
		},
		"wrong offset": func(string, map[string]any) (string, error) {
			return `{"seq":42,"offset":5,"length":1,"total":1,"sha256":"x","encoding":"base64","data":"YQ=="}`, nil
		},
		"implausible total": func(string, map[string]any) (string, error) {
			return `{"seq":42,"offset":0,"length":1,"total":99999999999,"sha256":"x","encoding":"base64","data":"YQ=="}`, nil
		},
		"page overruns total": func(string, map[string]any) (string, error) {
			return `{"seq":42,"offset":0,"length":2,"total":1,"sha256":"x","encoding":"base64","data":"YWI="}`, nil
		},
		"bad base64": func(string, map[string]any) (string, error) {
			return `{"seq":42,"offset":0,"length":1,"total":1,"sha256":"x","encoding":"base64","data":"!!"}`, nil
		},
		"body_unavailable": func(string, map[string]any) (string, error) { return "", errors.New("body_unavailable") },
	}
	for name, script := range cases {
		c, _ := newClient(t, script)
		if b, _, err := c.Body(context.Background(), 42); err == nil {
			t.Errorf("%s: accepted %d bytes", name, len(b))
		}
	}
}

func TestClose(t *testing.T) {
	c, f := newClient(t, func(string, map[string]any) (string, error) { return `{"closed":true}`, nil })
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.methods[0] != "browser.ledger.close" || len(f.params[0]) != 3 || f.params[0]["ledger_id"] != "L1" {
		t.Fatalf("close params = %v", f.params[0])
	}
	c, _ = newClient(t, func(string, map[string]any) (string, error) { return `{}`, nil })
	if err := c.Close(context.Background()); err == nil {
		t.Fatal("unacknowledged close accepted")
	}
	if err := (&Client{}).Close(context.Background()); err == nil {
		t.Fatal("nil caller accepted")
	}
}

// The values below are what the extension redacted before the batch left it.
// None of them is in batchJSON, so if any shows up after decoding, the client
// invented it; and every marker must still be marked, so a downstream printer
// cannot mistake a redacted field for an empty one.
func TestDecodedBatchCarriesNoCredentialValues(t *testing.T) {
	secrets := []string{
		"Bearer AAAAAAAAAAAAAAAAAAAAANRILgAAAAAAnNwIzUejRCOuH5E6I8xnZz4puTs",
		"reddit_session=eyJhbGciOi.secret.sig; token_v2=abc.def.ghi",
		"csrf-TOKEN-1234567890abcdef",
		"txn-1ab2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0",
		"form-csrf-9f8e7d6c5b4a",
		"eyJhbGciOi",
	}
	for _, s := range secrets {
		if strings.Contains(batchJSON, s) {
			t.Fatalf("fixture leaks %q before decoding; the test would prove nothing", s)
		}
	}
	c, _ := newClient(t, func(string, map[string]any) (string, error) { return batchJSON, nil })
	b, err := c.Read(context.Background(), 0, Match{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	walkStrings(reflect.ValueOf(b), func(s string) { seen = append(seen, s) })
	if len(seen) < 20 {
		t.Fatalf("walker saw only %d strings; it is not reaching the decoded fields", len(seen))
	}
	remarshaled, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		for _, s := range seen {
			if strings.Contains(s, secret) {
				t.Errorf("decoded struct contains %q", secret)
			}
		}
		if strings.Contains(string(remarshaled), secret) {
			t.Errorf("re-marshaled batch contains %q", secret)
		}
	}
	// Markers survive, with their counters, on every redacted field.
	want := map[string]struct {
		seq   int
		bytes int
		sha8  string
	}{
		"cookie":                  {0, 212, "c00c1e00"},
		"authorization":           {0, 44, "a0a0a0a0"},
		"x-csrf-token":            {1, 40, "c5f0c5f0"},
		"x-client-transaction-id": {1, 90, "7a7a7a7a"},
	}
	for name, w := range want {
		v, ok := b.Entries[w.seq].Header(name)
		if !ok || !v.Redacted || v.Bytes != w.bytes || v.SHA8 != w.sha8 || v.Value != "" {
			t.Errorf("%s = %+v %v", name, v, ok)
		}
	}
	if pd := b.Entries[2].Request.PostData; !pd.Redacted || pd.Bytes != 512 || pd.Value != "" {
		t.Errorf("post_data marker lost: %+v", pd)
	}
	if !strings.Contains(b.Entries[1].Request.PostData.Value, "csrf_token=redacted") {
		t.Errorf("form field redaction text changed: %q", b.Entries[1].Request.PostData.Value)
	}
	if !strings.Contains(string(remarshaled), `["cookie",{"redacted":true,"bytes":212,"sha8":"c00c1e00"}]`) || !strings.Contains(string(remarshaled), `"post_data":{"redacted":true,"bytes":512,"sha8":"f00dface"}`) {
		t.Fatalf("markers not re-emitted as markers: %s", remarshaled)
	}
	// Markers also stay markers through the http.Header views.
	for _, h := range []string{b.Entries[1].RequestHTTPHeader().Get("X-Csrf-Token"), b.Entries[0].RequestHTTPHeader().Get("Authorization")} {
		if !strings.HasPrefix(h, "redacted;") {
			t.Errorf("http view rendered %q", h)
		}
	}
}

func walkStrings(v reflect.Value, visit func(string)) {
	switch v.Kind() {
	case reflect.String:
		visit(v.String())
	case reflect.Ptr, reflect.Interface:
		if !v.IsNil() {
			walkStrings(v.Elem(), visit)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			walkStrings(v.Field(i), visit)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkStrings(v.Index(i), visit)
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			walkStrings(iter.Key(), visit)
			walkStrings(iter.Value(), visit)
		}
	}
}

func TestEntryRoundTrip(t *testing.T) {
	var b Batch
	if err := json.Unmarshal([]byte(batchJSON), &b); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(b.Entries)
	if err != nil {
		t.Fatal(err)
	}
	var back []Entry
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, b.Entries) {
		t.Fatalf("round trip changed entries:\n%s", data)
	}
	if !strings.Contains(string(data), `"from_cache":true`) || !strings.Contains(string(data), `"request_id":"R1"`) {
		t.Fatalf("wire names not preserved: %s", data)
	}
	if strings.Contains(string(data), `"response":null`) {
		t.Fatal("absent response must be omitted, not null")
	}
}
