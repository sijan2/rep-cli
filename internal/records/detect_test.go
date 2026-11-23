package records

import "testing"

func TestDetect(t *testing.T) {
	for _, tc := range []struct{ url, mime, body, site, op string }{
		{"https://www.reddit.com/r/test/comments/abc/title", "text/html", "", "reddit", "thread"},
		{"https://sh.reddit.com/svc/shreddit/more-comments/r/test/t3_abc?sort=new", "", "", "reddit", "more_comments"},
		{"https://x.com/i/api/graphql/any-query-id/TweetDetail", "", "", "x", "tweet_detail"},
		{"https://twitter.com/i/api/graphql/different/TweetDetail", "", "", "x", "tweet_detail"},
		{"https://x.com.evil.test/i/api/graphql/q/TweetDetail", "", "", "", ""},
		{"", "text/html", "<template><SHREDDIT-COMMENT thingId=t1_a></SHREDDIT-COMMENT></template>", "reddit", "more_comments"},
		{"", "application/json", `[{"type":"TimelineAddEntries","entries":[]}]`, "x", "tweet_detail"},
		{"", "application/json", `{"data":{"threaded_conversation_with_injections_v2":{"instructions":[]}}}`, "x", "tweet_detail"},
		{"", "text/html", "<!-- <shreddit-post> --><script>'<shreddit-post>'</script>", "", ""},
		{"", "application/json", `{"queryId":"TweetDetail"}`, "", ""},
	} {
		site, op := Detect(tc.url, tc.mime, []byte(tc.body))
		if site != tc.site || op != tc.op {
			t.Fatalf("detect %s %s: %s/%s", tc.url, tc.body, site, op)
		}
	}
}
