package records

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Page classification reads at most this much text outside comments.
const maxPageTextBytes = 64 << 10

type redditFrame struct {
	name                          string
	comment, slot, loader, anchor int
	ignore                        bool
	pageText                      bool
}

type redditAnchor struct {
	parent, href, noun string
	text               strings.Builder
}

func DecodeReddit(body []byte, src Source) (Page, error) {
	p := newPage("reddit", "thread")
	if src.Op == "more_comments" {
		p.Op = src.Op
	}
	scanner := htmlScanner{body: body}
	base := redditFrame{comment: -1, slot: -1, loader: -1, anchor: -1, pageText: true}
	stack := []redditFrame{base}
	var bodies []*strings.Builder
	var anchors []*redditAnchor
	var chrome strings.Builder
	postExpected := false
	for {
		t, ok := scanner.next()
		if !ok {
			break
		}
		current := stack[len(stack)-1]
		switch t.Kind {
		case startTag:
			f := current
			f.name = t.Name
			a := t.Attrs
			if t.Name == "script" || t.Name == "style" || t.Name == "reddit-chat" {
				f.ignore = true
			}
			if t.Name == "faceplate-partial" && ignoredRedditPartial(a["src"]) {
				f.ignore = true
			}
			if !f.ignore {
				if current.comment < 0 && (redditGate(t.Name) || redditGate(a["id"])) {
					p.Class = "gated"
				}
				switch t.Name {
				case "shreddit-post":
					f.pageText = false
					if validRedditID(a["id"], "t3_") && p.Thread == nil {
						r := redditRecord(a, src, t.Offset, "post")
						p.Thread = &r
					}
					if n, err := strconv.Atoi(a["comment-count"]); err == nil && n >= 0 {
						p.Expected, postExpected = n, true
					}
				case "shreddit-comment-tree", "shreddit-comment-tree-stats":
					key := "totalcomments"
					if t.Name == "shreddit-comment-tree-stats" {
						key = "total-comments"
					}
					if n, err := strconv.Atoi(a[key]); !postExpected && err == nil && n >= 0 {
						p.Expected = n
					}
				case "shreddit-comment":
					f.comment, f.slot = -1, -1
					if !validRedditID(a["thingid"], "t1_") {
						p.Dropped["missing_id"]++
						break
					}
					r := redditRecord(a, src, t.Offset, "comment")
					if r.Parent == "" && current.comment >= 0 {
						r.Parent = p.Records[current.comment].ID
					}
					if r.Parent == "" {
						r.Parent = a["postid"]
						if r.Parent == "" && p.Thread != nil {
							r.Parent = p.Thread.ID
						}
					}
					f.comment = len(p.Records)
					p.Records = append(p.Records, r)
					bodies = append(bodies, &strings.Builder{})
				case "div":
					if a["slot"] == "comment" && f.comment >= 0 {
						f.slot = f.comment
					}
				case "faceplate-partial":
					u, err := url.Parse(a["src"])
					if err != nil || !strings.HasPrefix(u.Path, "/svc/shreddit/more-comments/") {
						break
					}
					if a["loading"] != "action" && a["loading"] != "lazy" {
						p.Dropped["unknown_loader"]++
						break
					}
					parent := ""
					if f.comment >= 0 {
						parent = p.Records[f.comment].ID
					}
					c := Continuation{Kind: "branch", Parent: parent, Key: ContinuationKey{Path: u.Path}, Src: a["src"], State: "pending"}
					if a["loading"] == "lazy" || a["id"] == "top-level-more-comments-partial" || u.Query().Get("top-level") == "1" {
						c.Kind = "top_level"
						c.CountHint, _ = strconv.Atoi(u.Query().Get("comments-remaining"))
						if p.Thread != nil {
							c.Parent = p.Thread.ID
						}
					}
					f.loader = len(p.Continuations)
					p.Continuations = append(p.Continuations, c)
				case "input":
					if f.loader >= 0 && a["name"] == "cursor" && a["type"] == "hidden" {
						p.Continuations[f.loader].Key.Cursor = a["value"]
					}
				case "faceplate-number":
					if f.loader >= 0 {
						c := &p.Continuations[f.loader]
						n, err := strconv.Atoi(a["number"])
						if err != nil || n < 0 {
							p.Dropped["unparseable_hint"]++
						} else if c.CountHint == 0 {
							c.CountHint = n
						} else if c.CountHint != n {
							p.Dropped["unparseable_hint"]++
						}
					}
				case "a":
					if a["slot"] == "more-comments-permalink" {
						anchor := &redditAnchor{href: a["href"]}
						if f.comment >= 0 {
							anchor.parent = p.Records[f.comment].ID
						}
						f.anchor = len(anchors)
						anchors = append(anchors, anchor)
					}
				case "faceplate-tracker":
					if f.anchor >= 0 && anchors[f.anchor].noun == "" {
						anchors[f.anchor].noun = a["noun"]
					}
				}
				if f.slot >= 0 && blockTag(t.Name) {
					bodies[f.slot].WriteByte('\n')
				}
			}
			if !voidTag(t.Name) && !t.SelfClosing {
				if len(stack) >= maxHTMLDepth {
					return p, fmt.Errorf("HTML nesting exceeds %d levels", maxHTMLDepth)
				}
				stack = append(stack, f)
			}
		case endTag:
			if !current.ignore && current.slot >= 0 && blockTag(t.Name) {
				bodies[current.slot].WriteByte('\n')
			}
			for i := len(stack) - 1; i > 0; i-- {
				if stack[i].name == t.Name {
					stack = stack[:i]
					break
				}
			}
		case textToken:
			if current.ignore {
				continue
			}
			if current.slot >= 0 {
				bodies[current.slot].WriteString(t.Text)
			} else if current.comment < 0 && current.pageText && chrome.Len() < maxPageTextBytes {
				chrome.WriteString(t.Text[:min(len(t.Text), maxPageTextBytes-chrome.Len())])
				chrome.WriteByte(' ')
			}
			if current.anchor >= 0 {
				anchors[current.anchor].text.WriteString(t.Text)
			}
		}
	}
	if scanner.err != nil {
		return p, scanner.err
	}
	for _, f := range stack[1:] {
		if f.name == "shreddit-comment" || f.name == "faceplate-partial" || f.name == "template" || f.name == "shreddit-comment-tree" || f.slot >= 0 {
			return p, fmt.Errorf("incomplete Reddit record or continuation")
		}
	}
	for i := range p.Records {
		p.Records[i].Text = cleanHTMLText(bodies[i].String())
		if p.Records[i].Author == "[deleted]" || p.Records[i].Text == "[deleted]" || p.Records[i].Text == "[removed]" {
			addFlag(&p.Records[i], "deleted")
		}
	}
	branches := map[string]bool{}
	for _, c := range p.Continuations {
		if c.Kind == "branch" {
			branches[c.Parent] = true
		}
	}
	for _, a := range anchors {
		if a.noun == "more_replies" && branches[a.parent] {
			p.Dropped["loader_fallback"]++
			continue
		}
		u, err := url.Parse(a.href)
		if a.noun != "continue_thread" || a.parent == "" || err != nil || u.Path == "" {
			p.Dropped["unknown_anchor"]++
			continue
		}
		p.Continuations = append(p.Continuations, Continuation{Kind: "continue_thread", Parent: a.parent, Label: cleanHTMLText(a.text.String()), Key: ContinuationKey{Path: u.Path}, Src: a.href, State: "pending"})
	}
	text := strings.ToLower(strings.Join(strings.Fields(chrome.String()), " "))
	if len(p.Records) == 0 {
		for _, phrase := range []string{"this community is private", "this community has been banned", "this community is quarantined", "this community has been removed", "you must be 18+"} {
			if strings.Contains(text, phrase) {
				p.Class = "gated"
			}
		}
	}
	if strings.Contains(text, "prove your humanity") || strings.Contains(text, "blocked by network security") {
		p.Class = "challenge"
	}
	if p.Thread == nil && len(p.Records) > 0 {
		p.Op = "more_comments"
	}
	return p, nil
}

func redditRecord(a map[string]string, src Source, offset int, kind string) Record {
	id, created, text := a["thingid"], a["created"], ""
	if kind == "post" {
		id, created, text = a["id"], a["created-timestamp"], a["post-title"]
	}
	src.Offset += offset
	r := Record{ID: id, R: "r_" + id, Parent: a["parentid"], Kind: kind, Author: a["author"], Time: recordTime(created), Text: text, URL: a["permalink"], DOMKey: id, Src: src, Metrics: map[string]int64{}}
	if n, err := strconv.ParseInt(a["score"], 10, 64); err == nil {
		r.Metrics["score"] = n
	}
	if n, err := strconv.Atoi(a["depth"]); err == nil && n >= 0 {
		r.WireDepth, r.Depth = &n, n
	}
	if _, ok := a["is-op"]; ok {
		addFlag(&r, "op")
	}
	return r
}

func recordTime(value string) string {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999-0700", "Mon Jan 02 15:04:05 -0700 2006"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

func validRedditID(id, prefix string) bool {
	if !strings.HasPrefix(id, prefix) || len(id) <= len(prefix) {
		return false
	}
	for _, b := range []byte(id[len(prefix):]) {
		if !letter(b) && (b < '0' || b > '9') {
			return false
		}
	}
	return true
}

func ignoredRedditPartial(src string) bool {
	for _, part := range []string{"header-action-item-chat", "header-action-item-inbox", "user-drawer-", "reddit-chat", "challenge-prompt", "human-verification-dialog", "banned-user-banner"} {
		if strings.Contains(src, part) {
			return true
		}
	}
	return false
}

func redditGate(value string) bool {
	switch strings.ToLower(value) {
	case "over18-interstitial", "quarantine-interstitial", "private-interstitial", "banned-interstitial", "removed-interstitial", "shreddit-content-gate", "shreddit-interstitial":
		return true
	}
	return false
}

func blockTag(name string) bool {
	switch name {
	case "p", "div", "li", "ul", "ol", "blockquote", "br", "pre":
		return true
	}
	return false
}

func cleanHTMLText(text string) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
