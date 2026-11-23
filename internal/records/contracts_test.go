package records

import (
	"fmt"
	"testing"
)

func contractPage(count, expected int) Page {
	p := newPage("reddit", "thread")
	p.Expected, p.Thread = expected, &Record{ID: "root"}
	for i := 0; i < count; i++ {
		p.Records = append(p.Records, testRecord(fmt.Sprintf("id%d", i), "root"))
	}
	return p
}

func TestCoverageContracts(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		edit       func(*Page)
	}{
		{"complete", "complete", func(p *Page) {}},
		{"ratio", "server_gap", func(p *Page) { p.Expected = 100 }},
		{"author", "drift_fields", func(p *Page) {
			for i := 0; i < 4; i++ {
				p.Records[i].Author = ""
			}
		}},
		{"unknown", "drift_unknown", func(p *Page) { p.Dropped["unknown_entry"] = 10 }},
		{"pending", "frontier_pending", func(p *Page) { p.Continuations = []Continuation{{State: "pending"}} }},
		{"requested", "frontier_pending", func(p *Page) { p.Continuations = []Continuation{{State: "requested"}} }},
		{"failed", "frontier_pending", func(p *Page) { p.Continuations = []Continuation{{State: "failed", Reason: "timeout"}} }},
		{"short", "resolved_short", func(p *Page) { p.Continuations = []Continuation{{State: "resolved_short", Reason: "hint_mismatch"}} }},
		{"skipped", "complete", func(p *Page) { p.Continuations = []Continuation{{State: "skipped", Reason: "not_followed"}} }},
		{"skip without reason", "frontier_pending", func(p *Page) { p.Continuations = []Continuation{{State: "skipped"}} }},
		{"gated", "gated", func(p *Page) { p.Class = "gated" }},
		{"challenge", "challenge", func(p *Page) { p.Class = "challenge" }},
		{"auth", "auth", func(p *Page) { p.Class = "auth" }},
		{"deleted text", "complete", func(p *Page) {
			for i := range p.Records {
				p.Records[i].Text = ""
				p.Records[i].Flags = []string{"deleted"}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := contractPage(60, 60)
			tc.edit(&p)
			c := Check(NewThread(p), DefaultContracts())
			if c.Code != tc.code || (c.Status == "complete") != (tc.code == "complete") {
				t.Fatalf("coverage: %+v", c)
			}
			if tc.code == "server_gap" && (c.Ratio != 0.6 || c.GapReason == "" || !c.FrontierExhausted) {
				t.Fatal("server gap incomplete")
			}
		})
	}
}

func TestHintConservation(t *testing.T) {
	p := contractPage(0, -1)
	p.Continuations = []Continuation{{Kind: "branch", Parent: "root", CountHint: 6, State: "pending"}}
	tree := NewThread(p)
	fragment := contractPage(1, -1)
	delta := tree.Merge(fragment, &tree.Continuations[0])
	if delta.Added != 1 || tree.Continuations[0].State != "resolved_short" || tree.Continuations[0].Reason != "hint_mismatch" || Check(tree, DefaultContracts()).Code != "resolved_short" {
		t.Fatal("hint mismatch was hidden")
	}
}

func TestCoverageRatioUsesOnlyDirectXTweets(t *testing.T) {
	p := newPage("x", "tweet_detail")
	p.Thread, p.Expected = &Record{ID: "root"}, 1
	p.Records = []Record{testRecord("a", "root"), testRecord("b", "a"), testRecord("q", "a")}
	p.Records[0].Kind, p.Records[1].Kind, p.Records[2].Kind = "tweet", "tweet", "quote"
	c := Check(NewThread(p), DefaultContracts())
	if c.Records != 1 || c.Ratio != 1 || c.Code != "complete" {
		t.Fatalf("counted nested/quoted tweets as direct replies: %+v", c)
	}
}
