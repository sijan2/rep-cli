package sitepolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestShippedPoliciesLoad(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatalf("embedded policies: %v", err)
	}
	var ids []string
	for _, p := range All() {
		ids = append(ids, p.ID)
	}
	if !reflect.DeepEqual(ids, []string{"hn", "reddit", "x"}) {
		t.Fatalf("All() ids = %v", ids)
	}
	for _, id := range ids {
		if _, err := Load(id); err != nil {
			t.Fatalf("Load(%q): %v", id, err)
		}
	}
	if _, err := Load("nope"); err == nil {
		t.Fatal("unknown id must fail")
	}
}

// The numbers below are the design's load limits; a silent edit to a policy
// file must show up here.
func TestShippedPolicyValuesPinned(t *testing.T) {
	cases := []struct {
		id, risk, replay, expected string
		hosts                      []string
		pacing                     Pacing
		statuses                   []int
		sig                        []string
	}{
		{"reddit", "high", "never", "comment_count", []string{"www.reddit.com", "old.reddit.com", "new.reddit.com", "sh.reddit.com", "reddit.com"}, Pacing{MinIntervalMS: 800, MaxInFlight: 1, AdvancesPerRead: 40, DailyRecords: 3000}, []int{401, 403, 429}, nil},
		{"x", "high", "never", "reply_count", []string{"x.com", "www.x.com", "twitter.com", "www.twitter.com", "mobile.twitter.com"}, Pacing{MinIntervalMS: 1500, MaxInFlight: 1, AdvancesPerRead: 15, ReserveMin: 10, ReserveFraction: 0.4, DailyRecords: 2000}, []int{401, 403, 429}, []string{"x-client-transaction-id"}},
		{"hn", "low", "read_only", "comment_count", []string{"news.ycombinator.com"}, Pacing{MinIntervalMS: 2000, MaxInFlight: 1, AdvancesPerRead: 20}, []int{401, 403, 429}, nil},
	}
	for _, tc := range cases {
		p, err := Load(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if p.TOSRisk != tc.risk || p.Replay != tc.replay || p.Expected != tc.expected {
			t.Errorf("%s: risk/replay/expected = %s/%s/%s", tc.id, p.TOSRisk, p.Replay, p.Expected)
		}
		if !reflect.DeepEqual(p.Hosts, tc.hosts) {
			t.Errorf("%s hosts = %v", tc.id, p.Hosts)
		}
		if p.Pacing != tc.pacing {
			t.Errorf("%s pacing = %+v", tc.id, p.Pacing)
		}
		if !reflect.DeepEqual(p.Stop.Statuses, tc.statuses) {
			t.Errorf("%s stop statuses = %v", tc.id, p.Stop.Statuses)
		}
		if len(tc.sig) > 0 && !reflect.DeepEqual(p.SignatureHeaders, tc.sig) {
			t.Errorf("%s signature headers = %v", tc.id, p.SignatureHeaders)
		}
	}
	reddit, _ := Load("reddit")
	if !reflect.DeepEqual(reddit.Stop.TitleContains, []string{"Prove your humanity", "blocked by network security"}) || !reflect.DeepEqual(reddit.Stop.Headers, []string{"cf-mitigated"}) || !reflect.DeepEqual(reddit.Stop.RedirectPathPrefixes, []string{"/login"}) || !reflect.DeepEqual(reddit.Stop.RedirectQueryKeys, []string{"rdt"}) {
		t.Errorf("reddit stop = %+v", reddit.Stop)
	}
	x, _ := Load("x")
	if !reflect.DeepEqual(x.Stop.RedirectPathPrefixes, []string{"/login", "/i/flow/login"}) {
		t.Errorf("x redirects = %v", x.Stop.RedirectPathPrefixes)
	}
	if len(x.BodyOps) != 1 || x.BodyOps[0].PathRe != `^/i/api/graphql/[^/]+/(TweetDetail|TweetResultByRestId)$` || x.BodyOps[0].Host != "x.com" || x.BodyOps[0].Decoder != "x_tweetdetail" {
		t.Errorf("x body ops = %+v", x.BodyOps)
	}
}

func TestLoadReturnsIndependentCopies(t *testing.T) {
	a, _ := Load("reddit")
	a.Hosts[0] = "evil.example"
	a.BodyOps[0].Types[0] = "Script"
	a.DOMKeys["container"] = "x"
	b, _ := Load("reddit")
	if b.Hosts[0] != "www.reddit.com" || b.BodyOps[0].Types[0] != "Document" || b.DOMKeys["container"] != "shreddit-comment" {
		t.Fatal("Load shares mutable state between callers")
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		url    string
		id     string
		inputs Inputs
		ok     bool
	}{
		{"https://www.reddit.com/r/golang/comments/1abc2de/some_slug/", "reddit", Inputs{"sub": "golang", "t3": "1abc2de"}, true},
		{"https://old.reddit.com/r/GoLang/comments/1ABC2DE", "reddit", Inputs{"sub": "golang", "t3": "1abc2de"}, true},
		{"https://new.reddit.com/r/golang/comments/1abc2de?rdt=1", "reddit", Inputs{"sub": "golang", "t3": "1abc2de"}, true},
		{"https://sh.reddit.com/r/golang/comments/1abc2de/", "reddit", Inputs{"sub": "golang", "t3": "1abc2de"}, true},
		{"https://reddit.com/r/golang/comments/1abc2de/", "reddit", Inputs{"sub": "golang", "t3": "1abc2de"}, true},
		{"https://www.reddit.com/r/golang/", "reddit", Inputs{}, true},
		{"https://x.com/someone/status/1234567890123456789", "x", Inputs{"status": "1234567890123456789"}, true},
		{"https://twitter.com/someone/status/42?s=20", "x", Inputs{"status": "42"}, true},
		{"https://x.com/i/web/status/42", "x", Inputs{"status": "42"}, true},
		{"https://x.com/home", "x", Inputs{}, true},
		{"https://news.ycombinator.com/item?id=123456", "hn", Inputs{"item": "123456"}, true},
		{"https://news.ycombinator.com/item?id=abc", "hn", Inputs{}, true},
		{"https://news.ycombinator.com/", "hn", Inputs{}, true},
		{"https://www.reddit.com.evil.net/r/golang/comments/1abc2de/", "", nil, false},
		{"https://notreddit.com/r/golang/comments/1abc2de/", "", nil, false},
		{"https://api.reddit.com/r/golang/comments/1abc2de/", "", nil, false},
		{"https://example.com/r/golang/comments/1abc2de/", "", nil, false},
		{"not a url", "", nil, false},
		{"", "", nil, false},
	}
	for _, tc := range cases {
		p, in, ok := Match(tc.url)
		if ok != tc.ok {
			t.Errorf("Match(%q) ok = %v", tc.url, ok)
			continue
		}
		if !ok {
			continue
		}
		if p.ID != tc.id || !reflect.DeepEqual(in, tc.inputs) {
			t.Errorf("Match(%q) = %s %v", tc.url, p.ID, in)
		}
		if p.Complete(in) != (len(tc.inputs) == len(p.Inputs)) {
			t.Errorf("Complete(%v) wrong for %q", in, tc.url)
		}
	}
}

func TestAdmit(t *testing.T) {
	cases := []struct {
		policy, method, url, typ string
		op                       string
		ok                       bool
	}{
		{"reddit", "GET", "https://www.reddit.com/r/golang/comments/1abc2de/slug/", "Document", "thread", true},
		{"reddit", "get", "https://sh.reddit.com/r/golang/comments/1abc2de", "Document", "thread", true},
		{"reddit", "GET", "https://www.reddit.com/r/golang/comments/1abc2de/", "XHR", "", false},
		{"reddit", "POST", "https://www.reddit.com/svc/shreddit/more-comments/t3_1abc2de/t1_x", "Fetch", "more_comments", true},
		{"reddit", "POST", "https://www.reddit.com/svc/shreddit/more-comments/t3_1abc2de/t1_x", "XHR", "more_comments", true},
		{"reddit", "GET", "https://www.reddit.com/svc/shreddit/more-comments/t3_1abc2de/t1_x", "Fetch", "", false},
		{"reddit", "GET", "https://www.reddit.com/svc/shreddit/comments/r/golang/t3_1abc2de/t1_x", "Fetch", "comments", true},
		{"reddit", "GET", "https://www.reddit.com/svc/shreddit/inbox", "Fetch", "", false},
		{"reddit", "GET", "https://www.reddit.com/svc/shreddit/header-action-item-notifications", "Fetch", "", false},
		{"reddit", "GET", "https://www.reddit.com/r/chatgpt/comments/1abc2de/slug/", "Document", "thread", true},
		{"reddit", "GET", "https://reddit.com.evil.net/r/golang/comments/1abc2de/", "Document", "", false},
		{"reddit", "GET", "https://evilreddit.com/r/golang/comments/1abc2de/", "Document", "", false},
		{"reddit", "GET", "https://www.reddit.com/r/golang/comments/1abc2de/", "Script", "", false},
		{"reddit", "GET", "https://styles.redditmedia.com/r/golang/comments/1abc2de/", "Document", "", false},
		{"x", "GET", "https://x.com/i/api/graphql/AbC123/TweetDetail?variables=%7B%7D", "XHR", "tweet_detail", true},
		{"x", "GET", "https://x.com/i/api/graphql/AbC123/TweetResultByRestId?variables=%7B%7D", "Fetch", "tweet_detail", true},
		{"x", "GET", "https://api.x.com/i/api/graphql/AbC123/TweetDetail", "XHR", "tweet_detail", true},
		{"x", "GET", "https://twitter.com/i/api/graphql/AbC123/TweetDetail", "XHR", "", false},
		{"x", "GET", "https://x.com/i/api/graphql/AbC123/TweetDetailExtra", "XHR", "", false},
		{"x", "GET", "https://x.com/i/api/graphql/AbC123/HomeTimeline", "XHR", "", false},
		{"x", "POST", "https://x.com/i/api/graphql/AbC123/TweetDetail", "XHR", "", false},
		{"x", "GET", "https://x.com/i/api/1.1/dm/inbox_initial_state.json", "XHR", "", false},
		{"x", "GET", "https://x.com/someone/status/42", "Document", "", false},
		{"hn", "GET", "https://news.ycombinator.com/item?id=1", "Document", "item", true},
		{"hn", "GET", "https://news.ycombinator.com/item/1", "Document", "", false},
		{"hn", "GET", "https://news.ycombinator.com/threads?id=me", "Document", "", false},
		{"hn", "GET", "https://news.ycombinator.com/item?id=1", "Image", "", false},
	}
	for _, tc := range cases {
		p, err := Load(tc.policy)
		if err != nil {
			t.Fatal(err)
		}
		op, ok := p.Admit(tc.method, tc.url, tc.typ)
		if ok != tc.ok || op != tc.op {
			t.Errorf("%s Admit(%s %s %s) = %q %v, want %q %v", tc.policy, tc.method, tc.url, tc.typ, op, ok, tc.op, tc.ok)
		}
	}
}

func TestPrivate(t *testing.T) {
	p, _ := Load("reddit")
	for url, want := range map[string]bool{
		"https://www.reddit.com/r/chatgpt/comments/1abc2de/slug/": false,
		"https://www.reddit.com/chat/room/1":                      true,
		"https://chat.reddit.com/":                                true,
		"https://www.reddit.com/message/inbox/":                   true,
		"https://matrix.redditspace.com/_matrix/client/v3/sync":   true,
		"https://www.reddit.com/svc/shreddit/user-drawer-content": true,
	} {
		if p.Private(url) != want {
			t.Errorf("Private(%q) = %v", url, !want)
		}
	}
}

func TestGeneric(t *testing.T) {
	g := Generic()
	if g.ID != "generic" || g.TOSRisk != "low" || g.Replay != "read_only" || len(g.Hosts) != 0 {
		t.Fatalf("generic = %+v", g)
	}
	// Unbound: every URL is first-party to itself.
	if op, ok := g.Admit("GET", "https://www.example.co.uk/thread/1", "Document"); !ok || op != "document" {
		t.Fatalf("unbound document admit = %q %v", op, ok)
	}
	if _, ok := g.Admit("GET", "https://cdn.example.com/app.js", "Script"); ok {
		t.Fatal("static type admitted")
	}
	if _, ok := g.Admit("POST", "https://www.example.com/page", "Document"); ok {
		t.Fatal("POST document admitted")
	}
	b := GenericFor("https://www.example.co.uk/thread/1")
	if !reflect.DeepEqual(b.Hosts, []string{"example.co.uk"}) {
		t.Fatalf("bound hosts = %v", b.Hosts)
	}
	cases := []struct {
		method, url, typ string
		ok               bool
	}{
		{"GET", "https://www.example.co.uk/thread/1", "Document", true},
		{"GET", "https://api.example.co.uk/v1/thread?id=1", "XHR", true},
		{"POST", "https://example.co.uk/graphql", "Fetch", true},
		{"GET", "https://example.co.uk.evil.net/graphql", "Fetch", false},
		{"GET", "https://notexample.co.uk/graphql", "Fetch", false},
		{"GET", "https://cdn.other.com/data.json", "Fetch", false},
		{"GET", "https://api.example.co.uk/v1/thread", "Image", false},
		{"GET", "https://api.example.co.uk/v1/thread", "WebSocket", false},
	}
	for _, tc := range cases {
		if _, ok := b.Admit(tc.method, tc.url, tc.typ); ok != tc.ok {
			t.Errorf("bound Admit(%s %s %s) = %v", tc.method, tc.url, tc.typ, ok)
		}
	}
	params := b.LedgerParams("owner-1", 0, 0, 0)
	if params["max_body_bytes"] != int64(8<<20) || params["ring_bytes"] != int64(64<<20) || params["idle_ms"] != 120000 {
		t.Fatalf("generic defaults = %v", params)
	}
	for _, op := range params["body_ops"].([]map[string]any) {
		if op["host"] != "example.co.uk" {
			t.Fatalf("generic wire op host = %v", op["host"])
		}
	}
}

func TestLedgerParamsWireShape(t *testing.T) {
	p, _ := Load("x")
	params := p.LedgerParams("agent-7", 2<<20, 32<<20, 30000)
	got, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"body_ops":[{"host":"x.com","method":"GET","op":"tweet_detail","path_re":"^/i/api/graphql/[^/]+/(TweetDetail|TweetResultByRestId)$","types":["XHR","Fetch"]}],"idle_ms":30000,"max_body_bytes":2097152,"owner":"agent-7","policy_id":"x","private_paths":["/i/api/1.1/dm/","/i/api/2/notifications","/i/api/1.1/account/","x.com/messages","DMInbox","DmAllSearch"],"ring_bytes":33554432}`
	if string(got) != want {
		t.Fatalf("wire mismatch\n got %s\nwant %s", got, want)
	}
	if _, has := params["tab_id"]; has {
		t.Fatal("tab_id belongs to ledger.Open, not the policy")
	}
	clamped := p.LedgerParams("a", 1<<30, 1<<40, 10_000_000)
	if clamped["max_body_bytes"] != int64(MaxBodyBytes) || clamped["ring_bytes"] != int64(MaxRingBytes) || clamped["idle_ms"] != MaxIdleMS {
		t.Fatalf("ceilings not applied: %v", clamped)
	}
	// A policy op with no host expands to one wire op per policy host.
	multi := Generic()
	multi.Hosts = []string{"a.example", "b.example"}
	ops := multi.LedgerParams("o", 0, 0, 0)["body_ops"].([]map[string]any)
	if len(ops) != 6 || ops[0]["host"] != "a.example" || ops[1]["host"] != "b.example" {
		t.Fatalf("host expansion = %v", ops)
	}
	// Empty lists are arrays, never null, so the extension's .some() works.
	hn, _ := Load("hn")
	hn.PrivatePaths = nil
	hn.BodyOps[0].Types = nil
	data, _ := json.Marshal(hn.LedgerParams("o", 0, 0, 0))
	if !strings.Contains(string(data), `"private_paths":[]`) || !strings.Contains(string(data), `"types":[]`) {
		t.Fatalf("null lists on the wire: %s", data)
	}
}

func TestHasSignature(t *testing.T) {
	x, _ := Load("x")
	if name, ok := x.HasSignature(map[string][]string{"X-Client-Transaction-Id": {"redacted"}}); !ok || name != "x-client-transaction-id" {
		t.Fatalf("signature not detected: %q %v", name, ok)
	}
	if _, ok := x.HasSignature(map[string][]string{"Authorization": {"redacted"}}); ok {
		t.Fatal("unrelated header counted as signature")
	}
	reddit, _ := Load("reddit")
	if _, ok := reddit.HasSignature(map[string][]string{"x-client-transaction-id": {"v"}}); ok {
		t.Fatal("reddit declares no signature header")
	}
}

func TestRegistrableDomain(t *testing.T) {
	for host, want := range map[string]string{
		"www.reddit.com":         "reddit.com",
		"REDDIT.COM.":            "reddit.com",
		"a.b.example.co.uk":      "example.co.uk",
		"example.co.uk":          "example.co.uk",
		"co.uk":                  "co.uk",
		"news.ycombinator.com":   "ycombinator.com",
		"localhost":              "localhost",
		"127.0.0.1":              "127.0.0.1",
		"[::1]":                  "[::1]",
		"www.example.com:8443":   "example.com",
		"shop.example.com.au":    "example.com.au",
		"deep.sub.example.org":   "example.org",
		"x.com":                  "x.com",
		"mobile.twitter.com":     "twitter.com",
		"something.co.in":        "something.co.in",
		"api.something.co.in":    "something.co.in",
		"matrix.redditspace.com": "redditspace.com",
	} {
		if got := RegistrableDomain(host); got != want {
			t.Errorf("RegistrableDomain(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestLoadFailsClosed(t *testing.T) {
	good, err := os.ReadFile(filepath.Join("policies", "hn.json"))
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(from, to string) string {
		if !strings.Contains(string(good), from) {
			t.Fatalf("fixture lacks %q", from)
		}
		return strings.Replace(string(good), from, to, 1)
	}
	cases := map[string]string{
		"unknown field":     mutate(`"version": "1",`, `"version": "1", "bonus": true,`),
		"malformed json":    string(good[:len(good)-3]),
		"trailing data":     string(good) + ` {}`,
		"bad regexp":        mutate(`"path_re": "^/item$", "types"`, `"path_re": "^/item(", "types"`),
		"id/file mismatch":  mutate(`"id": "hn"`, `"id": "hackernews"`),
		"bad tos_risk":      mutate(`"tos_risk": "low"`, `"tos_risk": "none"`),
		"bad replay":        mutate(`"replay": "read_only"`, `"replay": "sometimes"`),
		"static body type":  mutate(`"types": ["Document"]`, `"types": ["Script"]`),
		"unknown decoder":   mutate(`"decoder": "hn_html"`, `"decoder": "magic"`),
		"primary not an op": mutate(`"primary": {"op": "item"`, `"primary": {"op": "nope"`),
		"no hosts":          mutate(`"hosts": ["news.ycombinator.com"]`, `"hosts": []`),
		"zero pacing":       mutate(`"min_interval_ms": 2000`, `"min_interval_ms": 0`),
		"bad contract key":  mutate(`"unknown_share": 0.1`, `"flavour": 0.1`),
		"bad stop status":   mutate(`"statuses": [401, 403, 429]`, `"statuses": [401, 4290]`),
		"lowercase method":  mutate(`"method": "GET"`, `"method": "get"`),
	}
	for name, body := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "hn.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadFrom(os.DirFS(dir), "."); err == nil {
			t.Errorf("%s: loaded without error", name)
		}
	}
	// One bad file poisons the whole set, including a good sibling.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hn.json"), good, 0o600)
	os.WriteFile(filepath.Join(dir, "other.json"), []byte(`{"id":"other"}`), 0o600)
	if _, err := loadFrom(os.DirFS(dir), "."); err == nil {
		t.Fatal("bad sibling did not fail the set")
	}
	// Two files claiming one host is ambiguous and refused.
	dir = t.TempDir()
	os.WriteFile(filepath.Join(dir, "hn.json"), good, 0o600)
	os.WriteFile(filepath.Join(dir, "hn2.json"), []byte(mutate(`"id": "hn"`, `"id": "hn2"`)), 0o600)
	if _, err := loadFrom(os.DirFS(dir), "."); err == nil || !strings.Contains(err.Error(), "claimed by both") {
		t.Fatalf("duplicate host accepted: %v", err)
	}
	// And the untouched file still loads, proving the mutations were the cause.
	dir = t.TempDir()
	os.WriteFile(filepath.Join(dir, "hn.json"), good, 0o600)
	if ps, err := loadFrom(os.DirFS(dir), "."); err != nil || len(ps) != 1 {
		t.Fatalf("pristine copy failed: %v", err)
	}
}
