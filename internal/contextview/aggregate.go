package contextview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/repplus/rep-cli/internal/store"
)

var (
	numberSegment = regexp.MustCompile(`^[0-9]+(?:[.-][0-9]+)*$`)
	uuidSegment   = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	hexSegment    = regexp.MustCompile(`(?i)^[0-9a-f]{8,}$`)
	safeID        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,95}$`)
)

type requestRef struct {
	id        string
	status    string
	timestamp int64
	digest    string
}

type accumulator struct {
	group      Group
	statuses   map[string]int
	types      map[string]int
	bodyStates map[string]int
	requests   []requestRef
}

func aggregateRequests(requests []store.Request) ([]aggregate, int) {
	groups := map[string]*accumulator{}
	hosts := map[string]bool{}
	for i := range requests {
		req := &requests[i]
		method := normalizedMethod(req.Method)
		origin, host, route := normalizedURL(req.URL)
		key := method + "\x00" + origin + "\x00" + route
		group, exists := groups[key]
		if !exists {
			group = &accumulator{
				group:    Group{ID: "g1_" + digestString(key)[:32], Method: method, Host: host, Origin: origin, Route: route, IDs: []string{}},
				statuses: map[string]int{}, types: map[string]int{}, bodyStates: map[string]int{},
			}
			groups[key] = group
		}
		hosts[host] = true
		group.group.Count++
		if req.Timestamp > group.group.Latest {
			group.group.Latest = req.Timestamp
		}
		status := "pending"
		if req.Response != nil {
			if req.Response.Status >= 100 && req.Response.Status <= 599 {
				status = strconv.Itoa(req.Response.Status)
			} else {
				status = "other"
			}
		}
		group.statuses[status]++
		group.types[normalizedType(req.ResourceType)]++
		bodyState := "unknown"
		if req.ResponseBodyCapture != nil {
			bodyState = req.ResponseBodyCapture.State
		} else if req.ResponseBodyTruncated {
			bodyState = "partial"
		} else if req.ResponseBodyError != "" {
			bodyState = "unavailable"
		}
		switch bodyState {
		case "complete", "partial", "unavailable", "pending", "not_applicable":
		default:
			bodyState = "unknown"
		}
		group.bodyStates[bodyState]++

		// Hash the original capture locally, including response content. This
		// detects a response arriving later under the same request ID and time.
		// The cache receives only these hashes, never the encoded capture.
		hash := sha256.New()
		_ = json.NewEncoder(hash).Encode(req)
		ref := requestRef{status: status, timestamp: req.Timestamp, digest: hex.EncodeToString(hash.Sum(nil))}
		if safeID.MatchString(req.ID) {
			ref.id = req.ID
		} else {
			group.group.RequestsWithoutSafeIDs++
		}
		group.requests = append(group.requests, ref)
	}
	result := make([]aggregate, 0, len(groups))
	for _, group := range groups {
		group.group.Statuses = sortedCounts(group.statuses)
		group.group.Types = sortedCounts(group.types)
		group.group.BodyStates = sortedCounts(group.bodyStates)
		group.group.IDs, group.group.OtherIDs = representatives(group.requests)
		sort.Slice(group.requests, func(i, j int) bool { return group.requests[i].digest < group.requests[j].digest })
		hash := sha256.New()
		for _, req := range group.requests {
			_, _ = hash.Write([]byte(req.digest))
		}
		// Bind checkpoints to the delivered projection as well as raw evidence.
		// Adding metadata (such as completeness counts) must reach callers whose
		// prior cursor acknowledged an older projection of unchanged requests.
		_ = json.NewEncoder(hash).Encode(group.group)
		result = append(result, aggregate{group: group.group, digest: hex.EncodeToString(hash.Sum(nil))})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].group.ID < result[j].group.ID })
	return result, len(hosts)
}

func representatives(requests []requestRef) ([]string, int) {
	refs := append([]requestRef(nil), requests...)
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].timestamp != refs[j].timestamp {
			return refs[i].timestamp > refs[j].timestamp
		}
		if refs[i].id != refs[j].id {
			return refs[i].id < refs[j].id
		}
		return refs[i].digest < refs[j].digest
	})
	allIDs := map[string]bool{}
	for _, ref := range refs {
		if ref.id != "" {
			allIDs[ref.id] = true
		}
	}
	result := []string{}
	chosen, statuses := map[string]bool{}, map[string]bool{}
	// The newest request is always included. Then preserve response-status
	// diversity before filling remaining slots with the newest distinct IDs.
	for _, ref := range refs {
		if ref.id != "" && !chosen[ref.id] && !statuses[ref.status] {
			result = append(result, ref.id)
			chosen[ref.id], statuses[ref.status] = true, true
			if len(result) == 3 {
				break
			}
		}
	}
	if len(result) < 3 {
		for _, ref := range refs {
			if ref.id != "" && !chosen[ref.id] {
				result = append(result, ref.id)
				chosen[ref.id] = true
				if len(result) == 3 {
					break
				}
			}
		}
	}
	return result, len(allIDs) - len(result)
}

func sortedCounts(values map[string]int) []Count {
	result := make([]Count, 0, len(values))
	for value, count := range values {
		result = append(result, Count{Value: value, Count: count})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Value < result[j].Value })
	return result
}

func normalizedMethod(method string) string {
	method = strings.ToUpper(method)
	if method == "" || len(method) > 16 {
		return "OTHER"
	}
	for _, r := range method {
		if r < 'A' || r > 'Z' {
			return "OTHER"
		}
	}
	return method
}

func normalizedType(value string) string {
	switch strings.ToLower(value) {
	case "main_frame", "sub_frame", "stylesheet", "script", "image", "font", "object", "xmlhttprequest", "xhr", "fetch", "ping", "csp_report", "media", "websocket", "webtransport", "webbundle", "document", "manifest":
		return strings.ToLower(value)
	default:
		return "other"
	}
}

func normalizedURL(raw string) (string, string, string) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "(unknown)", "(unknown)", "/:unknown"
	}
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "http", "https", "ws", "wss":
	default:
		return "(local)", "(local)", "/:non-http"
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if len(host) > 253 {
		return "(invalid)", "(invalid)", "/:unknown"
	}
	port := parsed.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 0 || number > 65535 {
			return "(invalid)", "(invalid)", "/:unknown"
		}
		port = strconv.Itoa(number)
		if (number == 80 && (scheme == "http" || scheme == "ws")) || (number == 443 && (scheme == "https" || scheme == "wss")) {
			port = ""
		}
	}
	originHost := host
	if port != "" {
		originHost = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		originHost = "[" + host + "]"
	}
	origin := (&url.URL{Scheme: scheme, Host: originHost}).String()
	segments := strings.SplitN(parsed.EscapedPath(), "/", 18)
	parts := make([]string, 0, len(segments))
	hideNext := false
	for i, encoded := range segments {
		if i == 17 {
			parts = append(parts, ":more")
			break
		}
		if encoded == "" {
			continue
		}
		segment, err := url.PathUnescape(encoded)
		if err != nil {
			segment = ":value"
		}
		if hideNext {
			parts = append(parts, ":value")
			hideNext = false
			continue
		}
		lower := strings.ToLower(segment)
		switch lower {
		case "token", "tokens", "secret", "secrets", "password", "authorization", "api-key", "apikey", "access_token", "refresh_token", "session_id", "email":
			hideNext = true
		}
		parts = append(parts, normalizedSegment(segment))
	}
	return origin, host, bounded("/"+strings.Join(parts, "/"), 192)
}

func normalizedSegment(segment string) string {
	if numberSegment.MatchString(segment) {
		return ":n"
	}
	if uuidSegment.MatchString(segment) || hexSegment.MatchString(segment) {
		return ":id"
	}
	if len(segment) > 48 {
		return ":id"
	}
	var lower, upper, digit bool
	for _, r := range segment {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		case r == '-' || r == '_' || r == '.':
		default:
			return ":value"
		}
	}
	if (len(segment) >= 12 && digit && (lower || upper)) || (len(segment) >= 20 && lower && upper) {
		return ":id"
	}
	return segment
}

// bounded preserves valid UTF-8 and signals shortening with a suffix bound to
// the original value. No accidental route collision comes from plain clipping.
func bounded(value string, limit int) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, value)
	if len(clean) <= limit {
		return clean
	}
	suffix := "~" + digestString(value)[:8]
	end := limit - len(suffix)
	for end > 0 && !utf8.RuneStart(clean[end]) {
		end--
	}
	return clean[:end] + suffix
}
