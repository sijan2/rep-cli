package records

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

func rawField(v any, path ...string) any {
	for _, key := range path {
		m, _ := v.(map[string]any)
		v = m[key]
	}
	return v
}

func rawString(v any, path ...string) string { s, _ := rawField(v, path...).(string); return s }
func rawArray(v any, path ...string) []any   { a, _ := rawField(v, path...).([]any); return a }

func TestXRealFixtureFacts(t *testing.T) {
	name := "x_tweetdetail.json"
	body, want := fixture(t, name), facts(t, name)
	focal := want["focal"].(map[string]any)["id"].(string)
	p, err := DecodeXTweetDetail(body, Source{RequestID: "fixture"}, focal)
	if err != nil {
		t.Fatal(err)
	}
	thread := NewThread(p)
	if p.Terminal || Check(thread, DefaultContracts()).Code != "frontier_pending" {
		t.Fatal("Top terminate exhausted the timeline")
	}
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	got := xFixtureFacts(t, raw, p, thread)
	got["bytes"] = len(body)
	for key, value := range want {
		equalFact(t, key, got[key], value)
	}
	for _, r := range append([]Record{*p.Thread}, p.Records...) {
		if r.R != "r_tw_"+r.ID || r.Author == "" || r.Time == "" || r.Src.RequestID != "fixture" || body[r.Src.Offset] != '{' {
			t.Fatalf("tweet metadata: %+v", r)
		}
		if r.ID != focal && (r.Parent != focal || thread.Records[thread.byID[r.ID]].Depth != 0) {
			t.Fatal("direct reply parent/depth")
		}
	}
	instructions := rawField(raw, "data", "threaded_conversation_with_injections_v2", "instructions")
	bare, _ := json.Marshal(instructions)
	for _, input := range [][]byte{bare, append([]byte(")]}'\n"), body...)} {
		decoded, err := DecodeXTweetDetail(input, Source{}, "")
		if err != nil || decoded.Thread.ID != focal || len(decoded.Records) != len(p.Records) {
			t.Fatalf("bare/XSSI: %v", err)
		}
	}
}

func xFixtureFacts(t *testing.T, raw any, p Page, thread *Thread) map[string]any {
	t.Helper()
	got := map[string]any{}
	counts := map[string]int{"entries": 0, "modules": 0, "module_items": 0, "cursor_entries": 0, "focal_tweet_entries": 0, "timeline_tweet_items": 0, "module_tweets_non_promoted": 0, "unknown_entry_typenames": 0, "unknown_item_typenames": 0, "tombstones": 0, "visibility_wrapped": 0, "added_to_module_items": 0, "quoted_status_result_keys": 0, "note_tweet_keys": 0, "legacy_screen_name_keys": 0}
	instructions, directions, promoted, tombstones, wrapped, quotes, added, replaced, unknownTypes := []string{}, []string{}, []string{}, []string{}, []string{}, []string{}, []string{}, []string{}, []string{}
	cursorTypes, resultTypes, screenPaths := map[string]int{}, map[string]int{}, map[string]int{}
	cursors := []map[string]any{}
	takeCursor := func(content any, id, where string) {
		typeName := rawString(content, "cursorType")
		cursorTypes[typeName]++
		cursors = append(cursors, map[string]any{"entry": id, "where": where, "type": typeName, "action_text": rawField(content, "displayTreatment", "actionText")})
		if where == "entry" {
			counts["cursor_entries"]++
		}
	}
	takeItem := func(item any, id, where string) {
		switch rawString(item, "__typename") {
		case "TimelineTimelineCursor":
			takeCursor(item, id, where)
		case "TimelineTweet":
			counts["timeline_tweet_items"]++
			result := rawField(item, "tweet_results", "result")
			kind := rawString(result, "__typename")
			resultTypes[kind]++
			if rawField(item, "promotedMetadata") != nil {
				promoted = append(promoted, id)
				return
			}
			if where == "module" {
				counts["module_tweets_non_promoted"]++
			}
			if kind == "TweetTombstone" {
				counts["tombstones"]++
				tombstones = append(tombstones, id)
				return
			}
			if kind == "TweetWithVisibilityResults" {
				counts["visibility_wrapped"]++
				result = rawField(result, "tweet")
				wrapped = append(wrapped, rawString(result, "rest_id"))
			}
			tweetID := rawString(result, "rest_id")
			if where == "add_to_module" {
				added = append(added, tweetID)
			}
			if quote := rawString(result, "quoted_status_result", "result", "rest_id"); quote != "" {
				quotes = append(quotes, quote)
			}
			r := p.Thread
			if tweetID != p.Thread.ID {
				value := thread.Records[thread.byID[tweetID]]
				r = &value
			}
			text := rawString(result, "note_tweet", "note_tweet_results", "result", "text")
			if text == "" {
				text = rawString(result, "legacy", "full_text")
			} else if !hasFlag(*r, "note_tweet") {
				t.Fatal("missing note_tweet flag")
			}
			equalFact(t, "preferred text", r.Text, text)
			equalFact(t, "tweet parent", r.Parent, rawString(result, "legacy", "in_reply_to_status_id_str"))
			equalFact(t, "core author", r.Author, rawString(result, "core", "user_results", "result", "core", "screen_name"))
		case "":
			counts["unknown_item_typenames"]++
		default:
			counts["unknown_item_typenames"]++
		}
	}
	for _, instruction := range rawArray(raw, "data", "threaded_conversation_with_injections_v2", "instructions") {
		kind := rawString(instruction, "type")
		instructions = append(instructions, kind)
		switch kind {
		case "TimelineAddEntries":
			for _, entry := range rawArray(instruction, "entries") {
				counts["entries"]++
				id, content := rawString(entry, "entryId"), rawField(entry, "content")
				switch rawString(content, "__typename") {
				case "TimelineTimelineModule":
					counts["modules"]++
					for _, item := range rawArray(content, "items") {
						counts["module_items"]++
						takeItem(rawField(item, "item", "itemContent"), rawString(item, "entryId"), "module")
					}
				case "TimelineTimelineItem":
					if strings.HasPrefix(id, "tweet-") {
						counts["focal_tweet_entries"]++
					}
					takeItem(rawField(content, "itemContent"), id, "entry")
				case "TimelineTimelineCursor":
					takeCursor(content, id, "entry")
				default:
					counts["unknown_entry_typenames"]++
					unknownTypes = append(unknownTypes, rawString(content, "__typename"))
				}
			}
		case "TimelineTerminateTimeline":
			directions = append(directions, rawString(instruction, "direction"))
		case "TimelineAddToModule":
			for _, item := range rawArray(instruction, "moduleItems") {
				counts["added_to_module_items"]++
				takeItem(rawField(item, "item", "itemContent"), rawString(item, "entryId"), "add_to_module")
			}
		case "TimelineReplaceEntry":
			replaced = append(replaced, rawString(instruction, "entry_id_to_replace"))
			entry := rawField(instruction, "entry")
			takeCursor(rawField(entry, "content"), rawString(entry, "entryId"), "replace_entry")
		}
	}
	var walk func(any, []string)
	walk = func(value any, path []string) {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				switch key {
				case "screen_name":
					parts := strings.Split(strings.Join(path, "."), "tweet_results.result.")
					keyPath := parts[len(parts)-1]
					screenPaths[keyPath]++
					if strings.HasSuffix(keyPath, "legacy") {
						counts["legacy_screen_name_keys"]++
					}
				case "note_tweet":
					counts["note_tweet_keys"]++
				case "quoted_status_result":
					counts["quoted_status_result_keys"]++
				}
				walk(child, append(append([]string(nil), path...), key))
			}
		case []any:
			for _, child := range v {
				walk(child, path)
			}
		}
	}
	walk(raw, nil)
	for key, count := range counts {
		got[key] = count
	}
	got["instructions"], got["terminate_directions"], got["cursors"], got["cursor_types"] = instructions, directions, cursors, cursorTypes
	got["result_typenames"], got["screen_name_key_paths"] = resultTypes, screenPaths
	got["promoted_entries"], got["tombstone_entries"], got["visibility_wrapped_ids"], got["quoted_ids"] = promoted, tombstones, wrapped, quotes
	got["added_to_module_ids"], got["replaced_entries"] = added, replaced
	sort.Strings(unknownTypes)
	got["unknown_entry_typename_values"] = unknownTypes
	got["unknown_entry_ratio"] = float64(counts["unknown_entry_typenames"]) / float64(counts["entries"])
	got["promoted"] = p.Dropped["promoted"]
	got["focal"] = map[string]any{"id": p.Thread.ID, "author": p.Thread.Author, "reply_count": p.Expected, "note_tweet": hasFlag(*p.Thread, "note_tweet")}
	got["direct_replies_to_focal"] = Check(thread, DefaultContracts()).Records
	organic := 1
	for _, r := range p.Records {
		if r.Kind != "quote" && !hasFlag(r, "tombstone") {
			organic++
		}
	}
	got["organic_tweets"] = organic
	return got
}

func TestXSyntheticInstructionsAndUnknown(t *testing.T) {
	for _, name := range []string{"x_tweetdetail_showmore.synthetic.json", "x_tweetdetail_unknown.synthetic.json"} {
		t.Run(name, func(t *testing.T) {
			body, want := fixture(t, name), facts(t, name)
			p, err := DecodeXTweetDetail(body, Source{}, "")
			if err != nil {
				t.Fatal(err)
			}
			thread := NewThread(p)
			var raw any
			_ = json.Unmarshal(body, &raw)
			got := xFixtureFacts(t, raw, p, thread)
			got["bytes"] = len(body)
			for key, value := range want {
				if key != "expect" {
					equalFact(t, key, got[key], value)
				}
			}
			if strings.Contains(name, "unknown") {
				if Check(thread, DefaultContracts()).Code != "drift_unknown" {
					t.Fatal("unknown drift hidden by pending cursor")
				}
			} else {
				if !p.Terminal || len(p.Continuations) != 1 || p.Continuations[0].Kind != "show_more" || p.Continuations[0].Label != "Show replies" {
					t.Fatalf("ShowMore: %+v", p.Continuations)
				}
				quote := thread.Records[thread.byID["1910000000000000150"]]
				if quote.Kind != "quote" || !hasFlag(quote, "quote") || quote.Parent != "1910000000000000106" {
					t.Fatalf("quote: %+v", quote)
				}
				if !hasFlag(thread.Records[thread.byID["1910000000000000105"]], "tombstone") {
					t.Fatal("tombstone lost")
				}
				if thread.Records[thread.byID["1910000000000000103"]].Depth != 2 {
					t.Fatal("AddToModule item not appended")
				}
			}
		})
	}
}

func TestXCursorReplacementAndKinds(t *testing.T) {
	for kind, want := range map[string]string{"Bottom": "bottom", "ShowMore": "show_more", "ShowMoreThreads": "show_more_threads", "ShowMoreThreadsPrompt": "spam_prompt"} {
		input := `[ {"type":"TimelineAddEntries","entries":[{"entryId":"cursor-old","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"old"}}]}, {"type":"TimelineReplaceEntry","entry_id_to_replace":"cursor-old","entry":{"entryId":"cursor-new","content":{"entryType":"TimelineTimelineCursor","cursorType":"` + kind + `","value":"new"}}} ]`
		p, err := DecodeXTweetDetail([]byte(input), Source{}, "")
		if err != nil || len(p.Continuations) != 1 || p.Continuations[0].Key.Cursor != "new" || p.Continuations[0].Kind != want {
			t.Fatalf("replace cursor: %+v %v", p.Continuations, err)
		}
	}
	p, err := DecodeXTweetDetail([]byte(`[{"type":"TimelineTerminateTimeline","direction":"TopAndBottom"}]`), Source{}, "")
	if err != nil || !p.Terminal {
		t.Fatal("TopAndBottom lost")
	}
}

func TestXModulePrependAndDuplicateFocal(t *testing.T) {
	input := `[
 {"type":"TimelineAddEntries","entries":[
 {"entryId":"tweet-1","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":{"__typename":"Tweet","rest_id":"1"}}}}},
 {"entryId":"conversationthread-1","content":{"__typename":"TimelineTimelineModule","items":[
 {"entryId":"conversationthread-1-tweet-1","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":{"__typename":"Tweet","rest_id":"1","legacy":{"reply_count":7}}}}}},
 {"entryId":"conversationthread-1-tweet-3","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":{"__typename":"Tweet","rest_id":"3","legacy":{"in_reply_to_status_id_str":"1"}}}}}},
 {"entryId":"conversationthread-1-tweet-4","promotedMetadata":{},"item":{"itemContent":{"itemType":"TimelineTweet"}}}
 ]}}]},
 {"type":"TimelineAddToModule","moduleEntryId":"conversationthread-1","prepend":true,"moduleItems":[
 {"entryId":"conversationthread-1-tweet-2","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":{"__typename":"Tweet","rest_id":"2","legacy":{"in_reply_to_status_id_str":"1"}}}}}}
 ]}
]`
	p, err := DecodeXTweetDetail([]byte(input), Source{}, "")
	if err != nil || p.Expected != 7 || len(p.Records) != 2 || p.Records[0].ID != "2" || p.Records[1].ID != "3" || p.Dropped["promoted"] != 1 {
		t.Fatalf("module prepend and duplicate focal: %+v %v", p, err)
	}
}

func TestXTweetFallbacksAndMalformedInput(t *testing.T) {
	input := `[
 {"type":"TimelineAddEntries","entries":[
  {"entryId":"tweet-1","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":{"__typename":"Tweet","rest_id":"1","core":{"user_results":{"result":{"legacy":{"screen_name":"fallback"}}}},"legacy":{"full_text":"first","reply_count":0},"views":{"count":"9007199254740993"}}}}}},
  {"entryId":"tweet-2","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":{"__typename":"TweetUnavailable"}}}}},
  {"entryId":"tweetdetailrelatedtweets-3","content":{}},
  {"entryId":"weird","content":{"entryType":"TimelineTimelineItem","itemContent":{"itemType":"NewType"}}}
 ]},
 {"type":"UnknownInstruction"}
]`
	p, err := DecodeXTweetDetail([]byte(input), Source{}, "1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Thread.Author != "fallback" || p.Thread.Metrics["views"] != 9007199254740993 || len(p.Records) != 1 || !hasFlag(p.Records[0], "unavailable") || p.Dropped["related"] != 1 || p.Dropped["unknown_item"] != 1 || p.Dropped["unknown_instruction"] != 1 {
		t.Fatalf("fallbacks: %+v", p)
	}
	p, err = DecodeXTweetDetail([]byte(input), Source{}, "missing")
	if err != nil || p.Thread != nil {
		t.Fatal("explicit missing focal silently replaced")
	}
	for _, input := range []string{`{}`, `[{`, `[] {}`, `{"a":1,"a":2}`, strings.Repeat("[", 514) + strings.Repeat("]", 514)} {
		if _, err := DecodeXTweetDetail([]byte(input), Source{}, ""); err == nil {
			t.Fatal("accepted malformed/unsupported JSON")
		}
	}
	p, err = DecodeXTweetDetail([]byte(`{"errors":[{"code":89}]}`), Source{}, "")
	if err != nil || p.Class != "auth" {
		t.Fatal("auth response lost")
	}
}

func TestXInstructionOrderAndMissingCore(t *testing.T) {
	input := `[
 {"type":"TimelineAddEntries","entries":[
 {"entryId":"cursor-a","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"a"}},
 {"entryId":"cursor-b","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"b"}}]},
 {"type":"TimelineReplaceEntry","entry_id_to_replace":"cursor-a","entry":{"entryId":"cursor-b","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"replacement"}}}
]`
	p, err := DecodeXTweetDetail([]byte(input), Source{}, "")
	if err != nil || len(p.Continuations) != 1 || p.Continuations[0].Key.Cursor != "replacement" {
		t.Fatalf("replacement collision: %+v %v", p.Continuations, err)
	}
	input = `[
 {"type":"TimelineTerminateTimeline","direction":"Bottom"},
 {"type":"TimelineClearCache"},
 {"type":"TimelineAddToModule","moduleEntryId":"conversationthread-1","moduleItems":[
 {"entryId":"conversationthread-1-tweet-2","item":{"itemContent":{"itemType":"TimelineTweet","tweet_results":{"result":{"__typename":"Tweet","rest_id":"2","legacy":{"full_text":"legacy","in_reply_to_status_id_str":"1"},"note_tweet":{"note_tweet_results":{"result":{"text":""}}}}}}}}]}
]`
	p, err = DecodeXTweetDetail([]byte(input), Source{}, "1")
	if err != nil || p.Terminal || len(p.Records) != 1 || p.Records[0].Author != "" || p.Records[0].Text != "" || !hasFlag(p.Records[0], "note_tweet") {
		t.Fatalf("standalone module/missing core/empty note: %+v %v", p, err)
	}
}
