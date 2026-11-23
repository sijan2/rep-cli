package records

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestRedditRealFixtureFacts(t *testing.T) {
	for _, name := range []string{"reddit_thread_ssr.html", "reddit_thread_ssr_new.html"} {
		t.Run(name, func(t *testing.T) {
			body := fixture(t, name)
			p, err := DecodeReddit(body, Source{RequestID: "fixture", SHA256: "digest"})
			if err != nil {
				t.Fatal(err)
			}
			thread := NewThread(p)
			want := facts(t, name)
			got := redditFixtureFacts(t, body, p, thread)
			for key, value := range want {
				equalFact(t, key, got[key], value)
			}
			coverage := Check(thread, DefaultContracts())
			if coverage.Code != "frontier_pending" || coverage.NullRates["parent"] > 0.01 {
				t.Fatalf("coverage: %+v", coverage)
			}
			if len(thread.Records) != len(p.Records) {
				t.Fatal("duplicate SSR IDs")
			}
		})
	}
}

func redditFixtureFacts(t *testing.T, body []byte, p Page, thread *Thread) map[string]any {
	t.Helper()
	nodes := fixtureHTML(t, body)
	got := map[string]any{"bytes": len(body), "comments": len(p.Records), "unique_thing_ids": len(thread.Records), "page_class": p.Class}
	counts, histogram := map[string]int{}, map[string]int{}
	parents, top, bodies, chars, mismatch, unresolved, deleted := 0, 0, 0, 0, 0, 0, 0
	for _, r := range thread.Records {
		counts[r.ID]++
		histogram[strconv.Itoa(*r.WireDepth)]++
		if r.Parent == p.Thread.ID {
			top++
		} else {
			parents++
		}
		if r.Text != "" {
			bodies++
		}
		chars += nonspace(r.Text)
		if r.Depth != *r.WireDepth {
			mismatch++
		}
		if hasFlag(r, "orphan") {
			unresolved++
		}
		if hasFlag(r, "deleted") {
			deleted++
		}
		if r.R != "r_"+r.ID || r.Src.RequestID != "fixture" || r.Src.SHA256 != "digest" || r.Time == "" || !strings.HasSuffix(r.Time, "Z") {
			t.Fatalf("record identity/provenance/time: %+v", r)
		}
		if !strings.HasPrefix(string(body[r.Src.Offset:]), "<shreddit-comment ") {
			t.Fatal("comment offset does not locate its opening tag")
		}
	}
	repeated := map[string]int{}
	for id, count := range counts {
		if count > 1 {
			repeated[id] = count
		}
	}
	got["repeated_thing_ids"], got["wire_depth_histogram"], got["wire_depth_histogram_unique"] = repeated, histogram, histogram
	got["with_parentId"], got["top_level"], got["top_level_with_t3_postId"] = parents, top, top
	got["comments_with_body"], got["text_nonspace_chars_total"] = bodies, chars
	got["derived_depth_mismatches"], got["derived_depth_unresolved"], got["deleted"] = mismatch, unresolved, deleted
	got["parents_outside_document"] = []string{}
	got["post"] = map[string]any{"present": p.Thread != nil, "id": p.Thread.ID, "author": p.Thread.Author}
	got["expected"] = map[string]int{"shreddit_post_comment_count": p.Expected, "comment_tree_totalComments": p.Expected, "comment_tree_stats_total_comments": p.Expected}
	camel, lower, slots, nesting := 0, 0, 0, 0
	partials := map[string]int{"challenge_prompt": 0, "human_verification_dialog": 0, "banned_user_banner": 0, "private": 0, "other": 0}
	var loaderNodes, anchorNodes []*fixtureNode
	for _, n := range nodes {
		a := n.token.Attrs
		switch n.token.Name {
		case "shreddit-comment":
			raw := string(body[n.token.Offset:n.token.End])
			camel += strings.Count(raw, " thingId=")
			lower += strings.Count(raw, " thingid=")
			parent := ""
			if ancestor := n.ancestor("shreddit-comment"); ancestor != nil {
				parent = ancestor.token.Attrs["thingid"]
			}
			if parent == a["parentid"] {
				nesting++
			}
			r := thread.Records[thread.byID[a["thingid"]]]
			equalFact(t, "author", r.Author, a["author"])
		case "div":
			if a["slot"] == "comment" {
				slots++
				owner := n.ancestor("shreddit-comment")
				r := thread.Records[thread.byID[owner.token.Attrs["thingid"]]]
				if nonspace(r.Text) != nonspace(n.text) {
					t.Fatal("own body text includes replies or loses markup text")
				}
			}
		case "shreddit-post", "shreddit-comment-tree", "shreddit-comment-tree-stats":
			for _, key := range []string{"comment-count", "totalcomments", "total-comments"} {
				if value, ok := a[key]; ok && value != strconv.Itoa(p.Expected) {
					t.Fatalf("expected-count source %s: %s", key, value)
				}
			}
		case "faceplate-partial":
			if strings.Contains(a["src"], "/more-comments/") {
				loaderNodes = append(loaderNodes, n)
			} else {
				key := "other"
				for _, candidate := range []string{"challenge-prompt", "human-verification-dialog", "banned-user-banner"} {
					if strings.Contains(a["src"], candidate) {
						key = strings.ReplaceAll(candidate, "-", "_")
					}
				}
				if key == "other" && ignoredRedditPartial(a["src"]) {
					key = "private"
				}
				partials[key]++
			}
		case "a":
			if a["slot"] == "more-comments-permalink" {
				anchorNodes = append(anchorNodes, n)
			}
		}
	}
	got["thingId_attrs_camelcase"], got["thingid_attrs_lowercase"], got["body_slots"], got["nesting_parent_equals_parentId"] = camel, lower, slots, nesting
	got["partials"] = partials
	got["markers"] = map[string]int{"over18": strings.Count(string(body), `/over18`) + strings.Count(string(body), `name="over18"`), "quarantine_text": strings.Count(strings.ToLower(nodes[0].parent.text), "quarantin"), "prove_your_humanity_text": strings.Count(strings.ToLower(nodes[0].parent.text), "prove your humanity")}
	l := map[string]any{}
	stats := map[string]int{"total": len(loaderNodes), "branch": 0, "top_level": 0, "branch_hint_sum": 0, "branch_with_button": 0, "exactly_one_hidden_cursor_input": 0, "faceplate_numbers_agree": 0, "faceplate_numbers_in_loaders": 0, "method_post": 0, "src_entity_encoded": 0, "with_sort_startingDepth_seeker_session": 0}
	paramSets, slotCounts, nouns, paths := map[string]int{}, map[string]int{}, map[string]int{}, map[string]bool{}
	hints, items, topIDs, topParams := []int{}, [][]any{}, []string{}, [][]string{}
	branches := map[string]bool{}
	for i, n := range loaderNodes {
		a, c := n.token.Attrs, p.Continuations[i]
		u, _ := url.Parse(c.Src)
		if strings.Contains(c.Src, "&amp;") || c.Key.Path != u.Path {
			t.Fatalf("undecoded continuation: %+v", c)
		}
		paths[c.Key.Path] = true
		params := queryKeys(u.RawQuery)
		paramSets[strings.Join(params, ",")]++
		slot := a["slot"]
		if slot == "" {
			slot = "None"
		}
		slotCounts[slot]++
		if a["method"] == "post" {
			stats["method_post"]++
		}
		if strings.Contains(string(body[n.token.Offset:n.token.End]), "&amp;") {
			stats["src_entity_encoded"]++
		}
		if q := u.Query(); q.Has("sort") && q.Has("startingDepth") && q.Has("seeker-session") {
			stats["with_sort_startingDepth_seeker_session"]++
		}
		inputs := n.descendants("input")
		if len(inputs) == 1 && inputs[0].token.Attrs["type"] == "hidden" && inputs[0].token.Attrs["name"] == "cursor" {
			stats["exactly_one_hidden_cursor_input"]++
			equalFact(t, "cursor", c.Key.Cursor, inputs[0].token.Attrs["value"])
		}
		numbers := n.descendants("faceplate-number")
		stats["faceplate_numbers_in_loaders"] += len(numbers)
		agree := true
		for _, number := range numbers {
			agree = agree && number.token.Attrs["number"] == strconv.Itoa(c.CountHint)
		}
		if agree {
			stats["faceplate_numbers_agree"]++
		}
		for _, tracker := range n.descendants("faceplate-tracker") {
			nouns[tracker.token.Attrs["noun"]]++
		}
		stats[c.Kind]++
		var parent, depth, hint, starting any
		if c.Kind == "branch" {
			parent, depth, hint = c.Parent, *thread.Records[thread.byID[c.Parent]].WireDepth, c.CountHint
			starting, _ = strconv.Atoi(u.Query().Get("startingDepth"))
			branches[c.Parent] = true
			stats["branch_hint_sum"] += c.CountHint
			hints = append(hints, c.CountHint)
			if len(n.descendants("button")) > 0 {
				stats["branch_with_button"]++
			}
		} else {
			topIDs = append(topIDs, a["id"])
			topParams = append(topParams, params)
		}
		items = append(items, []any{c.Key.Cursor, c.Kind, parent, depth, hint, starting, u.RawQuery})
	}
	for k, v := range stats {
		l[k] = v
	}
	pathList := []string{}
	for path := range paths {
		pathList = append(pathList, path)
	}
	sort.Strings(pathList)
	l["paths"], l["param_sets"], l["slots"], l["tracker_noun_counts"] = pathList, paramSets, slotCounts, nouns
	l["branch_hints"], l["items"], l["top_level_ids"], l["top_level_params"] = hints, items, topIDs, topParams
	l["item_columns"] = []string{"cursor", "kind", "parent", "parent_wire_depth", "hint", "startingDepth", "query"}
	got["loaders"] = l
	anchorStats := map[string]any{}
	fallback, continued, withNumber, sum, own := []string{}, []string{}, 0, 0, 0
	nouns = map[string]int{}
	for _, n := range anchorNodes {
		parent := n.ancestor("shreddit-comment").token.Attrs["thingid"]
		if branches[parent] {
			fallback = append(fallback, parent)
		} else {
			continued = append(continued, parent)
		}
		if strings.Contains(n.token.Attrs["href"], "/comment/"+strings.TrimPrefix(parent, "t1_")+"/") {
			own++
		}
		for _, tracker := range n.descendants("faceplate-tracker") {
			nouns[tracker.token.Attrs["noun"]]++
		}
		if ns := n.descendants("faceplate-number"); len(ns) > 0 {
			withNumber++
			number, _ := strconv.Atoi(ns[0].token.Attrs["number"])
			sum += number
		}
	}
	decodedContinued := []string{}
	for _, c := range p.Continuations {
		if c.Kind == "continue_thread" {
			decodedContinued = append(decodedContinued, c.Parent)
		}
	}
	equalFact(t, "anchor classification", decodedContinued, continued)
	equalFact(t, "fallback classification", p.Dropped["loader_fallback"], len(fallback))
	anchorStats["total"], anchorStats["loader_fallback"], anchorStats["continue_thread"] = len(anchorNodes), len(fallback), len(decodedContinued)
	anchorStats["with_number"], anchorStats["number_sum"], anchorStats["points_at_own_parent"] = withNumber, sum, own
	anchorStats["tracker_noun_counts"], anchorStats["loader_fallback_parents"], anchorStats["continue_thread_parents"] = nouns, fallback, decodedContinued
	got["permalink_anchors"] = anchorStats
	return got
}

func TestRedditFragmentMerge(t *testing.T) {
	base, err := DecodeReddit(fixture(t, "reddit_thread_ssr.html"), Source{})
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := DecodeReddit(fixture(t, "reddit_more_comments.synthetic.html"), Source{})
	if err != nil {
		t.Fatal(err)
	}
	thread := NewThread(base)
	var from *Continuation
	for i := range thread.Continuations {
		if thread.Continuations[i].Key.Cursor == "cursor_17" {
			from = &thread.Continuations[i]
		}
	}
	if from == nil {
		t.Fatal("missing fixture frontier")
	}
	delta := thread.Merge(fragment, from)
	if delta.Added != 4 || delta.Duplicates != 1 || len(thread.Records) != 104 || from.State != "resolved" || from.Yield != (Yield{Records: 4, NestedHints: 2}) {
		t.Fatalf("merge: %+v continuation: %+v", delta, from)
	}
	want := facts(t, "reddit_more_comments.synthetic.html")
	depths := want["derived_depth"].(map[string]any)
	for id, depth := range depths {
		r := thread.Records[thread.byID[id]]
		equalFact(t, id, r.Depth, depth)
		if hasFlag(r, "orphan") || !hasFlag(r, "wire_depth_mismatch") || r.Depth-*r.WireDepth != 1 {
			t.Fatalf("relative wire depth: %+v", r)
		}
	}
	if !hasFlag(thread.Records[thread.byID["t1_c900003"]], "deleted") {
		t.Fatal("deleted comment lost")
	}
	if len(thread.Continuations) != len(base.Continuations)+2 {
		t.Fatal("nested frontier lost")
	}
	if base.Continuations[16].State != "pending" {
		t.Fatal("merge mutated input page")
	}
	if repeated := thread.Merge(fragment, from); repeated.Added != 0 || from.State != "resolved" || from.Yield != (Yield{Records: 4, NestedHints: 2}) {
		t.Fatalf("repeated fragment changed yield: %+v %+v", repeated, from)
	}
}

func TestRedditMarkupAndClasses(t *testing.T) {
	for _, tc := range []struct{ input, class string }{
		{string(fixture(t, "reddit_gated.synthetic.html")), "gated"},
		{string(fixture(t, "reddit_challenge.synthetic.html")), "challenge"},
		{`<div id=quarantine-interstitial>Quarantined</div>`, "gated"},
		{`<div id=private-interstitial>Private</div>`, "gated"},
		{`<div id=banned-interstitial>Banned</div>`, "gated"},
		{`<h1>blocked by network security</h1>`, "challenge"},
		{`<main><h1>This community is private</h1></main>`, "gated"},
		{`<main><h1>This community has been banned</h1></main>`, "gated"},
		{`<shreddit-post id=t3_p><div>Prove your humanity</div></shreddit-post>`, "ok"},
		{`<faceplate-partial src="/challenge-prompt"><h1>Prove your humanity</h1></faceplate-partial><script>"Prove your humanity"</script>`, "ok"},
	} {
		p, err := DecodeReddit([]byte(tc.input), Source{})
		if err != nil || p.Class != tc.class {
			t.Fatalf("class %s: %+v %v", tc.class, p, err)
		}
	}
	input := `<TEMPLATE><SHREDDIT-COMMENT thingId='t1_a' postId=t3_p depth=0 author=u created="2024-01-01T00:00:00Z"><details role=article><div slot=comment id=x title="a > b"><div><div>one <em>two</em></div>three<br/>four &amp; &#39;</div><script>"</div>"</script><!-- <div>ignored</div> --></div></details><shreddit-comment thingId=t1_b parentId=t1_a depth=1><div slot=comment>reply</div></shreddit-comment></SHREDDIT-COMMENT></TEMPLATE>`
	p, err := DecodeReddit([]byte(input), Source{})
	if err != nil || len(p.Records) != 2 || p.Records[0].Text != "one two\nthree\nfour & '" || p.Records[1].Text != "reply" {
		t.Fatalf("body ownership: %+v %v", p.Records, err)
	}
	for _, bad := range []string{input[:len(input)/2], `<shreddit-comment thingId=t1_a><div slot=comment>unfinished`, `<template><shreddit-comment thingId=t1_a></shreddit-comment>`, strings.Repeat("<div>", maxHTMLDepth+1)} {
		if _, err := DecodeReddit([]byte(bad), Source{}); err == nil {
			t.Fatal("accepted incomplete/overdeep body")
		}
	}
	for _, tag := range []string{"header-action-item-chat", "header-action-item-inbox", "user-drawer-menu", "reddit-chat"} {
		p, err := DecodeReddit([]byte(fmt.Sprintf(`<faceplate-partial src="/%s"><shreddit-comment thingId=t1_private><div slot=comment>secret</div></shreddit-comment></faceplate-partial>`, tag)), Source{})
		if err != nil || len(p.Records) != 0 {
			t.Fatal("private partial admitted")
		}
	}
}

func TestRedditUnparseableItems(t *testing.T) {
	p, err := DecodeReddit([]byte(`<shreddit-comment></shreddit-comment><faceplate-partial src="/svc/shreddit/more-comments/r/test/t3_p" loading=unknown></faceplate-partial>`), Source{})
	if err != nil || p.Dropped["missing_id"] != 1 || p.Dropped["unknown_loader"] != 1 {
		t.Fatalf("unparseable items: %+v %v", p.Dropped, err)
	}
}

func TestRedditBodyResumesAfterNestedComment(t *testing.T) {
	input := `<shreddit-comment thingId=t1_a><div slot=comment>before<shreddit-comment thingId=t1_b><div slot=comment>child</div></shreddit-comment>after</div></shreddit-comment>`
	p, err := DecodeReddit([]byte(input), Source{})
	if err != nil || len(p.Records) != 2 || p.Records[0].Text != "beforeafter" || p.Records[1].Text != "child" {
		t.Fatalf("resuming own body after nested record: %+v %v", p.Records, err)
	}
}

// Sanitizing dropped the scripts and ads that precede the comments on the live
// page; they carry attribute values far over the scanner's limits.
func TestRedditIgnoresOversizedPageAttributes(t *testing.T) {
	body := fixture(t, "reddit_thread_ssr.html")
	want, err := DecodeReddit(body, Source{})
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("A", 2*maxAttrBytes)
	padded := append([]byte(`<script src="data:text/javascript;base64,`+big+`"></script><shreddit-comments-page-ad ad-events='`+big+`'></shreddit-comments-page-ad>`), body...)
	got, err := DecodeReddit(padded, Source{})
	if err != nil || len(got.Records) != len(want.Records) || len(got.Continuations) != len(want.Continuations) || got.Expected != want.Expected {
		t.Fatalf("oversized page attributes: %d records, %d continuations, expected %d, %v", len(got.Records), len(got.Continuations), got.Expected, err)
	}
	if site, op := Detect("", "text/html", padded); site != "reddit" || op != "thread" {
		t.Fatalf("detect: %s %s", site, op)
	}
}

func TestRedditSortMergeFacts(t *testing.T) {
	a, _ := DecodeReddit(fixture(t, "reddit_thread_ssr.html"), Source{})
	b, _ := DecodeReddit(fixture(t, "reddit_thread_ssr_new.html"), Source{})
	thread := NewThread(a)
	delta := thread.Merge(b, nil)
	f := facts(t, "_cross")
	equalFact(t, "sort union", len(thread.Records), f["reddit_sorts_union_ids"])
	equalFact(t, "shared IDs", len(b.Records)-delta.Added, f["reddit_sorts_shared_ids"])
}
