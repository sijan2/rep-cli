package commands

import (
	"encoding/json"

	"net/url"

	"sort"
	"strings"

	"github.com/repplus/rep-cli/internal/store"
)

// browser capture fanout replays one captured request N times with a value
// matrix, from inside a tab on the request's own origin, using the page's own
// session. Requests run in parallel inside the page; the ordinary action
// capture archives every response body. It is the generic form of the
// Marketplace tool's in-page worker pool: no navigation per query, no sleeps.

const maxFanoutVariants = 256

type fanoutVariant struct {
	Index   int               `json:"index"`
	Label   string            `json:"label"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    *string           `json:"body,omitempty"`
}

type fanoutOutcome struct {
	Index       int    `json:"index"`
	Label       string `json:"label"`
	Method      string `json:"method"`
	URL         string `json:"url"`
	Status      int    `json:"status,omitempty"`
	OK          bool   `json:"ok"`
	Bytes       int64  `json:"bytes,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	ElapsedMS   int64  `json:"elapsed_ms,omitempty"`
	Error       string `json:"error,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	BodyState   string `json:"body_state,omitempty"`
	Match       string `json:"match"`
}

// Browsers ignore forbidden request headers in fetch; cookies and identity
// headers come from the page session instead of the captured bytes.
var fanoutDroppedHeaders = map[string]bool{"cookie": true, "cookie2": true, "host": true, "content-length": true, "connection": true, "keep-alive": true, "transfer-encoding": true, "te": true, "trailer": true, "upgrade": true, "origin": true, "referer": true, "user-agent": true, "accept-encoding": true, "accept-charset": true, "date": true, "dnt": true, "expect": true, "via": true, "set-cookie": true, "priority": true}

func fanoutForwardHeaders(headers store.HeaderMap) map[string]string {
	result := map[string]string{}
	for name, values := range headers {
		lower := strings.ToLower(strings.TrimSpace(name))
		if lower == "" || strings.HasPrefix(lower, ":") || strings.HasPrefix(lower, "sec-") || strings.HasPrefix(lower, "proxy-") || fanoutDroppedHeaders[lower] || len(values) == 0 {
			continue
		}
		result[lower] = values[0]
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func fanoutNormalizeURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	parsed.Fragment = ""
	return parsed.String()
}

// fanoutOutcomes joins in-page results with the sealed capture. Same method and
// URL identify a request; identical URLs (body mutations) pair in start order.
// Anything else is reported as unmatched rather than guessed.
func fanoutOutcomes(variants []fanoutVariant, evaluation json.RawMessage, requests []store.Request) []fanoutOutcome {
	outcomes := make([]fanoutOutcome, len(variants))
	type pageResult struct {
		Index       int    `json:"index"`
		Status      int    `json:"status"`
		OK          bool   `json:"ok"`
		Bytes       int64  `json:"bytes"`
		ContentType string `json:"content_type"`
		ElapsedMS   int64  `json:"elapsed_ms"`
		Error       string `json:"error"`
	}
	var page struct {
		Variants []pageResult `json:"variants"`
	}
	_ = json.Unmarshal(evaluation, &page)
	byIndex := map[int]pageResult{}
	for _, item := range page.Variants {
		byIndex[item.Index] = item
	}
	type key struct{ method, url string }
	captured := map[key][]*store.Request{}
	for i := range requests {
		request := &requests[i]
		if request.RecordKind != "" && request.RecordKind != "http" {
			continue
		}
		k := key{strings.ToUpper(strings.TrimSpace(request.Method)), fanoutNormalizeURL(request.URL)}
		captured[k] = append(captured[k], request)
	}
	for _, list := range captured {
		sort.SliceStable(list, func(i, j int) bool { return list[i].StartOrdinal < list[j].StartOrdinal })
	}
	consumed := map[key]int{}
	for i, variant := range variants {
		outcome := fanoutOutcome{Index: variant.Index, Label: variant.Label, Method: variant.Method, URL: variant.URL, Match: "unmatched"}
		if item, ok := byIndex[variant.Index]; ok {
			outcome.Status, outcome.OK, outcome.Bytes, outcome.ContentType, outcome.ElapsedMS, outcome.Error = item.Status, item.OK, item.Bytes, item.ContentType, item.ElapsedMS, item.Error
		} else {
			outcome.Error = "no in-page result"
		}
		k := key{variant.Method, fanoutNormalizeURL(variant.URL)}
		list := captured[k]
		if position := consumed[k]; position < len(list) {
			request := list[position]
			consumed[k] = position + 1
			outcome.RequestID, outcome.Match = request.ID, "method_url"
			if len(list) > 1 {
				outcome.Match = "method_url_order"
			}
			outcome.BodyState = "unknown"
			if request.ResponseBodyCapture != nil {
				outcome.BodyState = browserBodyState(request.ResponseBodyCapture.State)
			}
		}
		outcomes[i] = outcome
	}
	return outcomes
}
