package records

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

type xObject struct {
	fields map[string]any
	offset int
}

func xField(value any, path ...string) any {
	for _, key := range path {
		object, ok := value.(*xObject)
		if !ok || object == nil {
			return nil
		}
		value = object.fields[key]
	}
	return value
}

func xString(value any, path ...string) string {
	s, _ := xField(value, path...).(string)
	return s
}

func xArray(value any, path ...string) []any {
	a, _ := xField(value, path...).([]any)
	return a
}

func xNumber(value any) (int64, bool) {
	var s string
	switch v := value.(type) {
	case json.Number:
		s = v.String()
	case string:
		s = v
	default:
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// Keep object offsets while decoding. Token's UseNumber avoids rounding tweet
// IDs and counters, and the explicit depth limit bounds hostile JSON nesting.
func readXJSON(body []byte) (any, error) {
	if !utf8.Valid(body) {
		return nil, fmt.Errorf("JSON requires valid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 512 {
			return nil, fmt.Errorf("JSON nesting exceeds 512 levels")
		}
		offset := int(d.InputOffset())
		for offset < len(body) && (space(body[offset]) || body[offset] == ',' || body[offset] == ':') {
			offset++
		}
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch token {
		case json.Delim('{'):
			o := &xObject{fields: map[string]any{}, offset: offset}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				name, ok := key.(string)
				if !ok {
					return nil, fmt.Errorf("invalid JSON object key")
				}
				if _, exists := o.fields[name]; exists {
					return nil, fmt.Errorf("repeated JSON object key")
				}
				value, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				o.fields[name] = value
			}
			_, err = d.Token()
			return o, err
		case json.Delim('['):
			a := []any{}
			for d.More() {
				v, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			_, err = d.Token()
			return a, err
		default:
			return token, nil
		}
	}
	v, err := read(0)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON at byte %d: %w", d.InputOffset(), err)
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing JSON")
	}
	return v, nil
}

func xInstructions(value any) ([]any, bool) {
	if a, ok := value.([]any); ok {
		return a, true
	}
	a, ok := xField(value, "data", "threaded_conversation_with_injections_v2", "instructions").([]any)
	return a, ok
}

func stripXSSI(body []byte) ([]byte, int) {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	offset := len(body) - len(trimmed)
	if bytes.HasPrefix(trimmed, []byte(")]}'")) {
		if end := bytes.IndexByte(trimmed, '\n'); end >= 0 {
			return trimmed[end+1:], offset + end + 1
		}
	}
	return trimmed, offset
}

func DecodeXTweetDetail(body []byte, src Source, focalID string) (Page, error) {
	p := newPage("x", "tweet_detail")
	body, prefix := stripXSSI(body)
	src.Offset += prefix
	value, err := readXJSON(body)
	if err != nil {
		return p, err
	}
	instructions, ok := xInstructions(value)
	if !ok {
		for _, e := range xArray(value, "errors") {
			if n, _ := xNumber(xField(e, "code")); n == 32 || n == 89 || n == 215 {
				p.Class = "auth"
				return p, nil
			}
		}
		return p, fmt.Errorf("missing TweetDetail instructions")
	}
	entries := []any{}
	index := map[string]int{}
	put := func(entry any, replace string) {
		id := xString(entry, "entryId")
		if id == "" {
			p.Dropped["unparseable_entry"]++
			return
		}
		if at, ok := index[replace]; replace != "" && ok {
			if other, exists := index[id]; exists && other != at {
				entries[other] = nil
			}
			delete(index, replace)
			entries[at], index[id] = entry, at
		} else if at, ok := index[id]; ok {
			entries[at] = entry
		} else {
			index[id] = len(entries)
			entries = append(entries, entry)
		}
	}
	for _, instruction := range instructions {
		switch xString(instruction, "type") {
		case "TimelineClearCache":
			entries, index = []any{}, map[string]int{}
			p.Terminal = false
		case "TimelineAddEntries":
			for _, entry := range xArray(instruction, "entries") {
				put(entry, "")
			}
		case "TimelineReplaceEntry":
			put(xField(instruction, "entry"), xString(instruction, "entry_id_to_replace"))
		case "TimelineAddToModule":
			id := xString(instruction, "moduleEntryId")
			items := xArray(instruction, "moduleItems")
			if id == "" || items == nil {
				p.Dropped["unparseable_instruction"]++
				continue
			}
			at, exists := index[id]
			if !exists {
				put(&xObject{fields: map[string]any{"entryId": id, "content": &xObject{fields: map[string]any{"entryType": "TimelineTimelineModule", "items": items}}}}, "")
				continue
			}
			content, ok := xField(entries[at], "content").(*xObject)
			if !ok || (xString(content, "entryType") != "TimelineTimelineModule" && xString(content, "__typename") != "TimelineTimelineModule") {
				p.Dropped["unparseable_instruction"]++
				continue
			}
			old := xArray(content, "items")
			if prepend, _ := xField(instruction, "prepend").(bool); prepend {
				content.fields["items"] = append(items, old...)
			} else {
				content.fields["items"] = append(old, items...)
			}
		case "TimelineTerminateTimeline":
			direction := xString(instruction, "direction")
			if direction == "Bottom" || direction == "TopAndBottom" {
				p.Terminal = true
			}
		default:
			p.Dropped["unknown_instruction"]++
		}
	}
	preferred := ""
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		id := xString(entry, "entryId")
		if strings.HasPrefix(id, "tweetdetailrelatedtweets-") {
			p.Dropped["related"]++
			continue
		}
		content := xField(entry, "content")
		if xField(entry, "promotedMetadata") != nil || xField(content, "promotedMetadata") != nil {
			p.Dropped["promoted"]++
			continue
		}
		kind := xString(content, "entryType")
		if kind == "" {
			kind = xString(content, "__typename")
		}
		switch kind {
		case "TimelineTimelineModule":
			for _, item := range xArray(content, "items") {
				if xField(item, "promotedMetadata") != nil || xField(item, "item", "promotedMetadata") != nil {
					p.Dropped["promoted"]++
					continue
				}
				decodeXItem(&p, xField(item, "item", "itemContent"), xString(item, "entryId"), strings.TrimPrefix(id, "conversationthread-"), src)
			}
		case "TimelineTimelineItem":
			before := len(p.Records)
			decodeXItem(&p, xField(content, "itemContent"), id, "", src)
			if preferred == "" && strings.HasPrefix(id, "tweet-") && len(p.Records) > before {
				preferred = p.Records[before].ID
			}
		case "TimelineTimelineCursor":
			decodeXCursor(&p, content, "")
		default:
			p.Dropped["unknown_entry"]++
		}
	}
	selected := focalID
	if selected == "" {
		selected = preferred
		if selected == "" && len(p.Records) > 0 {
			selected = p.Records[0].ID
		}
	}
	kept := p.Records[:0]
	for _, r := range p.Records {
		if r.ID == selected && r.Kind != "quote" {
			if p.Thread == nil {
				root := r
				p.Thread = &root
			} else {
				fillRecord(p.Thread, r)
			}
		} else {
			kept = append(kept, r)
		}
	}
	p.Records = kept
	if p.Thread != nil {
		if replies, ok := p.Thread.Metrics["replies"]; ok && replies >= 0 && int64(int(replies)) == replies {
			p.Expected = int(replies)
		}
	}
	for i := range p.Continuations {
		c := &p.Continuations[i]
		if c.Parent == "" && p.Thread != nil {
			c.Parent = p.Thread.ID
		}
		if p.Terminal && c.Kind == "bottom" {
			c.State, c.Reason = "resolved", "terminal"
		}
	}
	return p, nil
}

func decodeXItem(p *Page, item any, entryID, parent string, src Source) {
	if xField(item, "promotedMetadata") != nil {
		p.Dropped["promoted"]++
		return
	}
	if strings.HasPrefix(entryID, "tweetdetailrelatedtweets-") {
		p.Dropped["related"]++
		return
	}
	kind := xString(item, "itemType")
	if kind == "" {
		kind = xString(item, "__typename")
	}
	switch kind {
	case "TimelineTweet":
		fallback := ""
		if at := strings.LastIndex(entryID, "-tweet-"); at >= 0 {
			fallback = entryID[at+7:]
		} else if strings.HasPrefix(entryID, "tweet-") {
			fallback = strings.TrimPrefix(entryID, "tweet-")
		}
		decodeXTweet(p, xField(item, "tweet_results", "result"), fallback, "", src, 0)
	case "TimelineTimelineCursor":
		decodeXCursor(p, item, parent)
	default:
		p.Dropped["unknown_item"]++
	}
}

func decodeXTweet(p *Page, result any, fallback, quoteParent string, src Source, depth int) {
	if depth > 32 {
		p.Dropped["unparseable_quote"]++
		return
	}
	kind := xString(result, "__typename")
	if kind == "TweetWithVisibilityResults" {
		result = xField(result, "tweet")
		kind = xString(result, "__typename")
		if kind == "" && xString(result, "rest_id") != "" {
			kind = "Tweet"
		}
	}
	if kind != "Tweet" && kind != "TweetTombstone" && kind != "TweetUnavailable" {
		p.Dropped["unknown_tweet"]++
		return
	}
	id := xString(result, "rest_id")
	if id == "" {
		id = xString(result, "legacy", "id_str")
	}
	if id == "" && kind != "Tweet" {
		id = fallback
	}
	if id == "" {
		p.Dropped["missing_id"]++
		return
	}
	if object, ok := result.(*xObject); ok {
		src.Offset += object.offset
	}
	r := Record{R: "r_tw_" + id, ID: id, Kind: "tweet", Parent: xString(result, "legacy", "in_reply_to_status_id_str"), Metrics: map[string]int64{}, DOMKey: id, URL: "https://x.com/i/status/" + id, Src: src}
	if quoteParent != "" {
		r.Kind, r.Parent = "quote", quoteParent
		addFlag(&r, "quote")
	}
	if kind != "Tweet" {
		flag := "tombstone"
		if kind == "TweetUnavailable" {
			flag = "unavailable"
		}
		addFlag(&r, flag)
	} else {
		user := xField(result, "core", "user_results", "result")
		r.Author = xString(user, "core", "screen_name")
		if r.Author == "" {
			r.Author = xString(user, "legacy", "screen_name")
		}
		r.Time = recordTime(xString(result, "legacy", "created_at"))
		r.Text = xString(result, "legacy", "full_text")
		if text, ok := xField(result, "note_tweet", "note_tweet_results", "result", "text").(string); ok {
			r.Text = text
			addFlag(&r, "note_tweet")
		}
		for key, field := range map[string]string{"replies": "reply_count", "reposts": "retweet_count", "likes": "favorite_count", "quotes": "quote_count", "bookmarks": "bookmark_count"} {
			if n, ok := xNumber(xField(result, "legacy", field)); ok {
				r.Metrics[key] = n
			}
		}
		if n, ok := xNumber(xField(result, "views", "count")); ok {
			r.Metrics["views"] = n
		}
	}
	p.Records = append(p.Records, r)
	if quote := xField(result, "quoted_status_result", "result"); quote != nil {
		// Object offsets are absolute within the decoded JSON, not relative to
		// the containing tweet.
		if object, ok := result.(*xObject); ok {
			src.Offset -= object.offset
		}
		decodeXTweet(p, quote, "", id, src, depth+1)
	}
}

func decodeXCursor(p *Page, item any, parent string) {
	kinds := map[string]string{"Bottom": "bottom", "ShowMore": "show_more", "ShowMoreThreads": "show_more_threads", "ShowMoreThreadsPrompt": "spam_prompt"}
	typeName := xString(item, "cursorType")
	if typeName == "Top" {
		return
	}
	kind, ok := kinds[typeName]
	if !ok {
		p.Dropped["unknown_cursor"]++
		return
	}
	cursor := xString(item, "value")
	if cursor == "" {
		p.Dropped["unparseable_cursor"]++
		return
	}
	p.Continuations = append(p.Continuations, Continuation{Kind: kind, Parent: parent, Label: xString(item, "displayTreatment", "actionText"), Key: ContinuationKey{Path: "TweetDetail", Cursor: cursor}, Src: "TweetDetail", State: "pending"})
}
