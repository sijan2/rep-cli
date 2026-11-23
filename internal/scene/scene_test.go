package scene

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/records"
)

// The fixtures are the reducer's own output over the synthetic Reddit and X
// documents in the extension's tests/semantic-geometry.test.js, so the Go side
// is exercised on the exact wire shape the JS emits.
func load(t testing.TB, name string) Snapshot {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func build(t testing.TB, name string, keys Keys, network []records.Record) *Graph {
	t.Helper()
	g, err := Build(Input{Snapshot: load(t, name), Keys: keys, Network: network})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func record(t testing.TB, g *Graph, r string) Record {
	t.Helper()
	for _, rec := range g.Records {
		if rec.R == r {
			return rec
		}
	}
	t.Fatalf("record %s missing; have %v", r, handles(g))
	return Record{}
}

func handles(g *Graph) []string {
	out := make([]string, 0, len(g.Records))
	for _, r := range g.Records {
		out = append(out, r.R)
	}
	return out
}

func names(cs []Control) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func controlByName(t testing.TB, cs []Control, name string) Control {
	t.Helper()
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("control %q missing; have %v", name, names(cs))
	return Control{}
}

func allControls(g *Graph) []Control {
	var out []Control
	for _, r := range g.Records {
		out = append(out, r.Controls...)
	}
	return append(out, g.Orphans...)
}

func TestFrame6AndHandles(t *testing.T) {
	f := Frame6("F1", "L1")
	if !regexp.MustCompile(`^[0-9a-f]{6}$`).MatchString(f) {
		t.Fatalf("frame6 %q", f)
	}
	if Frame6("F1", "L2") == f || Frame6("F2", "L1") == f {
		t.Fatal("frame6 must change with the frame or loader id")
	}
	ref, err := ParseHandle(NodeHandle(f, 18801))
	if err != nil || ref != (Ref{Kind: "n", Frame6: f, Backend: 18801}) {
		t.Fatalf("node handle round trip: %+v %v", ref, err)
	}
	cases := map[string]Ref{
		"r_t1_abc":                 {Kind: "r", Key: "t1_abc"},
		"r_t3_post1":               {Kind: "r", Key: "t3_post1"},
		"r_tw_1851000000000000001": {Kind: "r", Key: "1851000000000000001"},
		"r_tw_1851.q1":             {Kind: "r", Key: "1851.q1"},
		"r_dom_item-7":             {Kind: "r", Key: "item-7"},
	}
	for in, want := range cases {
		got, err := ParseHandle(in)
		if err != nil || got != want {
			t.Errorf("ParseHandle(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "x_1", "n_abc.1", "n_9c1e0a", "n_9c1e0a.0", "n_9c1e0a.-1", "n_9c1e0a.01", "n_9C1E0A.5", "r_", "r_tw", "r_tw_", "r__k", "r_t w_1", "n_9c1e0a.1 "} {
		if _, err := ParseHandle(bad); err == nil {
			t.Errorf("ParseHandle(%q) accepted", bad)
		}
	}
	if RecordHandle("", "t1_abc") != "r_t1_abc" || RecordHandle("tw", "185") != "r_tw_185" {
		t.Fatal("record handle format")
	}
	for in, want := range map[string]Locator{
		"r_t1_abc":                 {Attr: "thingid", Value: "t1_abc"},
		"t1_abc":                   {Attr: "thingid", Value: "t1_abc"},
		"r_tw_1851000000000000001": {Href: "/status/1851000000000000001"},
		"1851000000000000001":      {Href: "/status/1851000000000000001"},
		"r_dom_item-7":             {Value: "item-7"},
	} {
		got, err := Locate(in)
		if err != nil || got != want {
			t.Errorf("Locate(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	if _, err := Locate(`r_tw_1"]`); err == nil {
		t.Fatal("Locate accepted a key with quotes")
	}
}

func TestDecodeRejectsMisalignedColumns(t *testing.T) {
	s := load(t, "reddit_thread.synthetic.json")
	s.Nodes.Text = s.Nodes.Text[:len(s.Nodes.Text)-1]
	if err := s.Validate(); err == nil {
		t.Fatal("short text column accepted")
	}
	s = load(t, "reddit_thread.synthetic.json")
	s.Nodes.Parent[5] = 9
	if err := s.Validate(); err == nil {
		t.Fatal("forward parent accepted")
	}
}

func TestRedditRecordsAndOwnership(t *testing.T) {
	g := build(t, "reddit_thread.synthetic.json", RedditKeys, nil)
	if g.Frame6 != Frame6("F1", "L1") {
		t.Fatalf("frame6 %q", g.Frame6)
	}
	want := []string{"r_t1_abc", "r_t1_def", "r_t1_ghi", "r_t1_jkl", "r_t1_mno"}
	if got := handles(g); !reflect.DeepEqual(got, want) {
		t.Fatalf("records %v, want %v", got, want)
	}
	abc, def, ghi, jkl, mno := record(t, g, "r_t1_abc"), record(t, g, "r_t1_def"), record(t, g, "r_t1_ghi"), record(t, g, "r_t1_jkl"), record(t, g, "r_t1_mno")
	for _, tc := range []struct {
		r             Record
		parent        string
		depth, order  int
		author, when  string
		metric, start string
	}{
		{abc, "", 0, 0, "alice", "4d", "s41", "SMTP relays charge per 1k because"},
		{def, "r_t1_abc", 1, 1, "bob", "3d", "s7", "Thanks, that clears it up."},
		{ghi, "r_t1_def", 2, 2, "carol", "2d", "s3", "Agreed — the feedback loops"},
		{jkl, "", 0, 3, "dave", "1d", "s5", "Different angle"},
		{mno, "", 0, 4, "erin", "20h", "s2", "Late reply here."},
	} {
		if tc.r.Parent != tc.parent || tc.r.Depth != tc.depth || tc.r.Order != tc.order {
			t.Errorf("%s nesting: parent %q depth %d order %d", tc.r.R, tc.r.Parent, tc.r.Depth, tc.r.Order)
		}
		if tc.r.Author != tc.author || tc.r.Time != tc.when || tc.r.Metric != tc.metric || !strings.HasPrefix(tc.r.Summary, tc.start) {
			t.Errorf("%s summary: %q %q %q %q", tc.r.R, tc.r.Author, tc.r.Time, tc.r.Metric, tc.r.Summary)
		}
		if tc.r.Kind != "comment" || tc.r.Src != "dom" || tc.r.Key != strings.TrimPrefix(tc.r.R, "r_") {
			t.Errorf("%s identity: kind %q src %q key %q", tc.r.R, tc.r.Kind, tc.r.Src, tc.r.Key)
		}
	}
	// Nested replies' text never leaks into the parent summary, and the
	// summary never exceeds 120 bytes even when the text node was 160.
	if strings.Contains(abc.Summary, "Thanks") || len(abc.Summary) > 120 || len(abc.Summary) < 100 {
		t.Fatalf("abc summary %q", abc.Summary)
	}

	// Ownership: every control inside a comment belongs to the innermost
	// comment, including the ones Reddit renders inside shadow roots.
	wantNames := map[string][]string{
		"r_t1_abc": {"alice", "Collapse", "Upvote", "Downvote", "Reply", "Share", "More options"},
		"r_t1_def": {"bob", "Upvote", "Downvote", "Reply", "Share"},
		"r_t1_ghi": {"carol", "Upvote", "Downvote", "Reply", "Share"},
		"r_t1_jkl": {"dave", "Upvote", "Downvote", "Reply", "Share", "3 more replies"},
		"r_t1_mno": {"erin", "Upvote", "Downvote", "Reply", "Share"},
	}
	for r, want := range wantNames {
		if got := names(record(t, g, r).Controls); !reflect.DeepEqual(got, want) {
			t.Errorf("%s controls %v, want %v", r, got, want)
		}
	}
	for _, r := range g.Records {
		for _, c := range r.Controls {
			if c.Owner != r.R {
				t.Errorf("control %s in %s has owner %q", c.Name, r.R, c.Owner)
			}
			if c.Rel == "contains" && !c.Box.Empty() && !(c.Box.X >= r.Box.X && c.Box.Right() <= r.Box.Right() && c.Box.Y >= r.Box.Y && c.Box.Bottom() <= r.Box.Bottom()) {
				t.Errorf("control %s owned by %s lies outside its box", c.Name, r.R)
			}
		}
	}
	// 100% ownership on labeled controls: a named, visible control that sits
	// geometrically inside a record is never an orphan.
	for _, o := range g.Orphans {
		if o.Vis == "hidden" {
			continue
		}
		for _, r := range g.Records {
			if o.Box.X >= r.Box.X && o.Box.Right() <= r.Box.Right() && o.Box.Y >= r.Box.Y && o.Box.Bottom() <= r.Box.Bottom() {
				t.Errorf("orphan %q lies inside %s", o.Name, r.R)
			}
		}
	}
	// The nameless orphan is the visibility:hidden login link: its text node
	// was dropped by the reducer, so nothing is left to name it by.
	if got := names(g.Orphans); !reflect.DeepEqual(got, []string{"Home", "Notifications", "Sort by", "Search Reddit", "Search", "", "View more comments"}) {
		t.Fatalf("orphans %v", got)
	}

	// Name sources follow the precedence: aria-label, aria-labelledby, text,
	// svg title.
	srcs := map[string]string{}
	for _, c := range allControls(g) {
		srcs[c.Name] = c.NameSrc
	}
	for name, src := range map[string]string{"Upvote": "aria-label", "Sort by": "aria-labelledby", "Reply": "text", "Notifications": "svg-title", "alice": "text", "Collapse": "text", "Search Reddit": "placeholder"} {
		if srcs[name] != src {
			t.Errorf("%s named from %q, want %q", name, srcs[name], src)
		}
	}
	// Roles: tags imply roles, cursor:pointer spans are "pointer".
	roles := map[string]string{}
	for _, c := range allControls(g) {
		roles[c.Name] = c.Role
	}
	for name, role := range map[string]string{"Upvote": "button", "alice": "link", "Collapse": "pointer", "3 more replies": "button"} {
		if roles[name] != role {
			t.Errorf("%s role %q, want %q", name, roles[name], role)
		}
	}

	// continues: the "3 more replies" button sits 12px under t1_jkl, well
	// within 1.5x the median sibling gap; "View more comments" is far below.
	more := controlByName(t, jkl.Controls, "3 more replies")
	if more.Rel != "continues" || more.Owner != "r_t1_jkl" {
		t.Fatalf("continues: %+v", more)
	}
	if g.Stats["continues"] != 1 {
		t.Fatalf("stats %v", g.Stats)
	}

	// Visibility from the box against the viewport, or from hidden styles.
	vis := map[string]string{}
	for _, c := range allControls(g) {
		vis[c.Name] = c.Vis
	}
	for name, want := range map[string]string{"Home": "in_viewport", "Search": "hidden", "": "hidden", "Search Reddit": "in_viewport", "View more comments": "offscreen", "3 more replies": "offscreen"} {
		if vis[name] != want {
			t.Errorf("%q vis %q, want %q", name, vis[name], want)
		}
	}
	if controlByName(t, mno.Controls, "Reply").Vis != "offscreen" || controlByName(t, abc.Controls, "Reply").Vis != "in_viewport" {
		t.Fatal("record control visibility")
	}
	if g.Stats["records"] != 5 || g.Stats["orphans"] != 7 || g.Stats["owned"] != 28 || g.Stats["controls"] != 35 {
		t.Fatalf("stats %v", g.Stats)
	}
}

func TestHandlesRoundTripAndStayStableUnderTickingText(t *testing.T) {
	g := build(t, "reddit_thread.synthetic.json", RedditKeys, nil)
	for _, r := range g.Records {
		ref, err := ParseHandle(r.R)
		if err != nil || ref.Kind != "r" || ref.Key != r.Key {
			t.Fatalf("record handle %s: %+v %v", r.R, ref, err)
		}
	}
	for _, c := range allControls(g) {
		ref, err := ParseHandle(c.H)
		if err != nil || ref.Kind != "n" || ref.Frame6 != g.Frame6 || ref.Backend != c.Backend || NodeHandle(ref.Frame6, ref.Backend) != c.H {
			t.Fatalf("control handle %s: %+v %v", c.H, ref, err)
		}
	}

	// The clock ticks: relative times and scores change, the DOM does not.
	s := load(t, "reddit_thread.synthetic.json")
	for i, txt := range s.Nodes.Text {
		switch txt {
		case "4d ago":
			s.Nodes.Text[i] = "5d ago"
		case "41":
			s.Nodes.Text[i] = "42"
		}
	}
	later, err := Build(Input{Snapshot: s, Keys: RedditKeys})
	if err != nil {
		t.Fatal(err)
	}
	strip := func(g *Graph) ([]string, map[string]string) {
		var hs []string
		owners := map[string]string{}
		for _, c := range allControls(g) {
			hs = append(hs, c.H)
			owners[c.H] = c.Owner + "/" + c.Rel + "/" + c.Name
		}
		return hs, owners
	}
	h1, o1 := strip(g)
	h2, o2 := strip(later)
	if !reflect.DeepEqual(h1, h2) || !reflect.DeepEqual(o1, o2) || !reflect.DeepEqual(handles(g), handles(later)) {
		t.Fatal("ticking timestamp changed handles or ownership")
	}
	if record(t, later, "r_t1_abc").Time != "5d" || record(t, later, "r_t1_abc").Metric != "s42" {
		t.Fatal("ticked values not reflected in the summary")
	}
}

func TestRedditProjection(t *testing.T) {
	g := build(t, "reddit_thread.synthetic.json", RedditKeys, nil)
	p := g.Project(View{})
	if p.Truncated || len(p.Lines) != 6 {
		t.Fatalf("lines %d truncated %v: %q", len(p.Lines), p.Truncated, p.Lines)
	}
	// Actions lead, the author link trails; the summary shrank to 40 bytes to
	// make room for all seven handles.
	line := regexp.MustCompile(`^r_t1_abc d0 @alice 4d s41 'SMTP relays charge per 1k because delive' \[collapse n_[0-9a-f]{6}\.\d+ \| upvote n_[0-9a-f]{6}\.\d+ \| downvote n_[0-9a-f]{6}\.\d+ \| reply n_[0-9a-f]{6}\.\d+ \| share n_[0-9a-f]{6}\.\d+ \| more options n_[0-9a-f]{6}\.\d+ \| alice n_[0-9a-f]{6}\.\d+\]$`)
	if !line.MatchString(p.Lines[0]) {
		t.Fatalf("line style: %q", p.Lines[0])
	}
	if !strings.HasPrefix(p.Lines[1], "r_t1_def d1 @bob 3d s7 'Thanks, that clears it up.' [") {
		t.Fatalf("nested line: %q", p.Lines[1])
	}
	if !strings.Contains(p.Lines[3], "3 more replies n_") || !strings.HasPrefix(p.Lines[3], "r_t1_jkl d0 @dave") {
		t.Fatalf("continues line: %q", p.Lines[3])
	}
	// Viewport first: the offscreen record comes last, before the trailer.
	if !strings.HasPrefix(p.Lines[4], "r_t1_mno") || p.Lines[5] != "offscreen: 0 records" {
		t.Fatalf("order: %q", p.Lines)
	}
	for _, l := range p.Lines {
		if len(l) > MaxLineBytes {
			t.Fatalf("line over %d bytes: %q", MaxLineBytes, l)
		}
	}
	if len(p.Records) != 5 {
		t.Fatalf("projection records %d", len(p.Records))
	}

	tight := g.Project(View{MaxBytes: 600})
	if !tight.Truncated || len(tight.Lines) != 3 || tight.Lines[2] != "offscreen: 3 records" {
		t.Fatalf("budget: %q", tight.Lines)
	}
	total := 0
	for _, l := range tight.Lines {
		total += len(l) + 1
	}
	if total > 600 {
		t.Fatalf("budget exceeded: %d", total)
	}
	since := g.Project(View{Since: 3})
	if len(since.Lines) != 3 || !strings.HasPrefix(since.Lines[0], "r_t1_jkl") {
		t.Fatalf("since: %q", since.Lines)
	}
	if json.Valid([]byte(`{}`)) && len(must(json.Marshal(p))) == 0 {
		t.Fatal("projection must marshal")
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

func TestRedditNetworkJoin(t *testing.T) {
	network := []records.Record{
		{R: "r_t1_jkl", ID: "t1_jkl", DOMKey: "t1_jkl", Kind: "comment", Author: "dave", Depth: 0, Metrics: map[string]int64{"score": 5}},
		{R: "r_t1_zzz", ID: "t1_zzz", DOMKey: "t1_zzz", Parent: "t1_jkl", Kind: "comment", Author: "zed", Time: "1d", Text: "Collapsed by the page, present on the wire.", Depth: 1, Metrics: map[string]int64{"score": 1}},
		{R: "r_t1_abc", ID: "t1_abc", DOMKey: "t1_abc", Kind: "comment", Author: "alice", Depth: 0, Metrics: map[string]int64{"score": 41}},
	}
	g := build(t, "reddit_thread.synthetic.json", RedditKeys, network)
	want := []string{"r_t1_jkl", "r_t1_zzz", "r_t1_abc", "r_t1_def", "r_t1_ghi", "r_t1_mno"}
	if got := handles(g); !reflect.DeepEqual(got, want) {
		t.Fatalf("network order %v, want %v", got, want)
	}
	zzz := record(t, g, "r_t1_zzz")
	if !zzz.NetOnly || zzz.Src != "network" || zzz.Parent != "r_t1_jkl" || zzz.Depth != 1 || len(zzz.Controls) != 0 || zzz.Metric != "s1" || zzz.Author != "zed" {
		t.Fatalf("net only: %+v", zzz)
	}
	if record(t, g, "r_t1_abc").Src != "both" || record(t, g, "r_t1_def").Src != "dom" {
		t.Fatal("src")
	}
	// DOM facts win over the wire where both exist; the wire fills gaps.
	if a := record(t, g, "r_t1_abc"); a.Time != "4d" || len(a.Controls) != 7 {
		t.Fatalf("joined record lost DOM facts: %+v", a)
	}
	if g.Stats["net_only"] != 1 {
		t.Fatalf("stats %v", g.Stats)
	}
	p := g.Project(View{Mode: "records"})
	if !strings.HasPrefix(p.Lines[1], "r_t1_zzz d1 net_only @zed 1d s1 'Collapsed by the page") {
		t.Fatalf("records view: %q", p.Lines)
	}
	if v := g.Project(View{}); strings.Contains(strings.Join(v.Lines, "\n"), "r_t1_zzz") {
		t.Fatal("viewport view must not list net_only records")
	}
}

func TestKeysInferredFromURL(t *testing.T) {
	if KeysForURL("https://www.reddit.com/r/x/comments/1/") != RedditKeys || KeysForURL("https://x.com/a/status/1") != XKeys || KeysForURL("https://twitter.com/a") != XKeys || KeysForURL("https://example.com/") != (Keys{}) {
		t.Fatal("KeysForURL")
	}
	if g := build(t, "reddit_thread.synthetic.json", Keys{}, nil); len(g.Records) != 5 {
		t.Fatalf("reddit inferred: %v", handles(g))
	}
	if g := build(t, "x_conversation.synthetic.json", Keys{}, nil); len(g.Records) != 4 {
		t.Fatalf("x inferred: %v", handles(g))
	}
	// Unknown sites segment nothing and keep every control as an orphan.
	reddit := build(t, "reddit_thread.synthetic.json", RedditKeys, nil)
	s := load(t, "reddit_thread.synthetic.json")
	s.URL = "https://example.com/thread"
	g, err := Build(Input{Snapshot: s})
	if err != nil || len(g.Records) != 0 || len(g.Orphans) != reddit.Stats["controls"] {
		t.Fatalf("generic: %d records %d orphans (want %d) %v", len(g.Records), len(g.Orphans), reddit.Stats["controls"], err)
	}
}

func TestXRecordsQuotesAndOwnership(t *testing.T) {
	g := build(t, "x_conversation.synthetic.json", XKeys, nil)
	want := []string{"r_tw_1851000000000000001", "r_tw_1849000000000000009", "r_tw_1851000000000000002", "r_tw_1851000000000000003"}
	if got := handles(g); !reflect.DeepEqual(got, want) {
		t.Fatalf("records %v, want %v", got, want)
	}
	bob := record(t, g, "r_tw_1851000000000000001")
	quote := record(t, g, "r_tw_1849000000000000009")
	dan := record(t, g, "r_tw_1851000000000000002")
	erin := record(t, g, "r_tw_1851000000000000003")

	// The outer key is the permalink wrapping <time> outside the quote, not
	// the quote's own permalink (earlier in document order) nor the
	// analytics /status/ link in the action row.
	if bob.Key != "1851000000000000001" || bob.Kind != "tweet" || bob.Depth != 0 || bob.Parent != "" {
		t.Fatalf("bob: %+v", bob)
	}
	if quote.Kind != "quote" || quote.Parent != bob.R || quote.Depth != 1 || quote.Author != "carol" || quote.Time != "Oct 28" || !strings.HasPrefix(quote.Summary, "Original post: we measured 0 of 546") {
		t.Fatalf("quote: %+v", quote)
	}
	if bob.Author != "bob" || bob.Time != "2h" || bob.Metric != "likes=120" || bob.Summary != "Shipping today: the new perception layer lands with records, controls and handles." {
		t.Fatalf("bob summary: %+v", bob)
	}
	if dan.Metric != "likes=4" || dan.Author != "dan" || dan.Time != "1h" || erin.Time != "45m" {
		t.Fatalf("replies: %+v %+v", dan, erin)
	}

	// The outer action row stays with the outer tweet; the quote owns only
	// what lies inside its container.
	if got := names(bob.Controls); !reflect.DeepEqual(got, []string{"Bob", "@bob", "2h", "26 Replies. Reply", "5 reposts. Repost", "120 Likes. Like", "3000 views. View post analytics", "Bookmark", "Share post"}) {
		t.Fatalf("bob controls %v", got)
	}
	if got := names(quote.Controls); !reflect.DeepEqual(got, []string{"open", "Oct 28"}) {
		t.Fatalf("quote controls %v", got)
	}
	open := controlByName(t, quote.Controls, "open")
	if open.Role != "link" || open.NameSrc != "container" || open.Owner != quote.R {
		t.Fatalf("quote container control %+v", open)
	}
	for _, r := range g.Records {
		for _, c := range r.Controls {
			if c.Owner != r.R {
				t.Errorf("%s owner %q", c.Name, c.Owner)
			}
		}
	}
	more := controlByName(t, erin.Controls, "Show more replies")
	if more.Rel != "continues" || more.Role != "button" || more.Vis != "offscreen" {
		t.Fatalf("continues %+v", more)
	}
	if got := names(g.Orphans); !reflect.DeepEqual(got, []string{"Home", "Search and explore", "Show probable spam"}) {
		t.Fatalf("orphans %v", got)
	}
	if g.Stats["quotes"] != 1 || g.Stats["continues"] != 1 || g.Stats["records"] != 4 {
		t.Fatalf("stats %v", g.Stats)
	}
	if controlByName(t, bob.Controls, "120 Likes. Like").Vis != "in_viewport" || controlByName(t, erin.Controls, "2 Likes. Like").Vis != "offscreen" {
		t.Fatal("vis")
	}
}

func TestXProjectionFoldsQuotes(t *testing.T) {
	g := build(t, "x_conversation.synthetic.json", XKeys, nil)
	p := g.Project(View{})
	if len(p.Lines) != 4 || p.Truncated {
		t.Fatalf("lines %q", p.Lines)
	}
	first := p.Lines[0]
	// Nine controls plus a quote cannot fit 250 bytes with the prose, so the
	// summary shrinks to 40 bytes, the quote folds to its handle and open
	// control, and the trailing controls are counted: the agent still sees
	// the words to pick the record and the action handles to use on it.
	if !strings.HasPrefix(first, "r_tw_1851000000000000001 d0 @bob 2h likes=120 'Shipping today: the new perception layer' [quote r_tw_1849000000000000009 @carol open n_") {
		t.Fatalf("head: %q", first)
	}
	if !strings.Contains(first, "[reply n_") || !strings.Contains(first, "| like n_") || strings.Contains(first, "26 replies") || !regexp.MustCompile(` \| \+[4-6]\]$`).MatchString(first) {
		t.Fatalf("controls: %q", first)
	}
	if len(first) > MaxLineBytes {
		t.Fatalf("line %d bytes: %q", len(first), first)
	}
	// Replies with fewer controls keep their words.
	if !strings.Contains(p.Lines[1], "'Does this handle quoted posts?'") {
		t.Fatalf("reply line lost its summary: %q", p.Lines[1])
	}
	// The quote record is in the JSON form, right after its parent.
	if len(p.Records) != 4 || p.Records[1].Kind != "quote" {
		t.Fatalf("records %v", handles(&Graph{Records: p.Records}))
	}
	if !strings.HasPrefix(p.Lines[2], "r_tw_1851000000000000003") || !strings.Contains(p.Lines[2], "show more replies n_") || p.Lines[3] != "offscreen: 0 records" {
		t.Fatalf("tail %q", p.Lines[2:])
	}
}

func TestLineBudgetDropsControlsNotHandles(t *testing.T) {
	g := build(t, "x_conversation.synthetic.json", XKeys, nil)
	bob := record(t, g, "r_tw_1851000000000000001")
	// Inflate the name list so the stage machine has to drop controls.
	for i := 0; i < 20; i++ {
		bob.Controls = append(bob.Controls, Control{H: NodeHandle(g.Frame6, 9000+i), Role: "button", Name: "Extremely verbose synthetic control name", Vis: "in_viewport", Owner: bob.R, Rel: "contains"})
	}
	line := g.line(bob, nil)
	if len(line) > MaxLineBytes || !strings.Contains(line, " | +") || !strings.HasPrefix(line, "r_tw_1851000000000000001 d0 @bob 2h likes=120") {
		t.Fatalf("budgeted line (%d): %q", len(line), line)
	}
	handle := regexp.MustCompile(`n_[0-9a-f]{6}\.\d+`)
	for _, h := range handle.FindAllString(line, -1) {
		if _, err := ParseHandle(h); err != nil {
			t.Fatalf("truncated handle %q in %q", h, line)
		}
	}
}
