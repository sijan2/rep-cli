package records

import (
	"net/url"
	"strings"
)

func Detect(rawURL, contentType string, body []byte) (site, op string) {
	u, _ := url.Parse(rawURL)
	if u != nil {
		host := strings.ToLower(u.Hostname())
		if host == "reddit.com" || strings.HasSuffix(host, ".reddit.com") || host == "" {
			if strings.HasPrefix(u.Path, "/svc/shreddit/more-comments/") {
				return "reddit", "more_comments"
			}
			if strings.HasPrefix(u.Path, "/r/") && strings.Contains(u.Path, "/comments/") {
				return "reddit", "thread"
			}
		}
		if host == "x.com" || host == "twitter.com" || strings.HasSuffix(host, ".x.com") || strings.HasSuffix(host, ".twitter.com") || host == "" {
			if strings.Contains(u.Path, "/graphql/") && strings.HasSuffix(u.Path, "/TweetDetail") {
				return "x", "tweet_detail"
			}
		}
	}
	trimmed, _ := stripXSSI(body)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		if value, err := readXJSON(trimmed); err == nil {
			if instructions, ok := xInstructions(value); ok {
				if _, full := value.(*xObject); full {
					return "x", "tweet_detail"
				}
				for _, i := range instructions {
					if strings.HasPrefix(xString(i, "type"), "Timeline") {
						return "x", "tweet_detail"
					}
				}
			}
		}
	}
	if strings.Contains(strings.ToLower(contentType), "json") {
		return "", ""
	}
	scanner := htmlScanner{body: body}
	comment := false
	for {
		t, ok := scanner.next()
		if !ok {
			break
		}
		if t.Kind != startTag {
			continue
		}
		if t.Name == "shreddit-post" || t.Name == "shreddit-comment-tree" || t.Name == "shreddit-app" {
			return "reddit", "thread"
		}
		comment = comment || t.Name == "shreddit-comment"
	}
	if comment {
		return "reddit", "more_comments"
	}
	return "", ""
}
