package commands

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

type coreNetQuery struct {
	Methods, Statuses, Hosts, Types, Has []string
	In                                   string
	Regex                                []*regexp.Regexp
	Since                                time.Duration
	Now                                  time.Time
}

var coreStatusTerm = regexp.MustCompile(`^[1-5](?:[0-9]{2}|xx)$`)

func parseCoreNet(terms []string) (coreNetQuery, error) {
	q := coreNetQuery{In: "url", Now: time.Now()}
	fieldSet := false
	for _, term := range terms {
		switch {
		case term == "GET" || term == "HEAD" || term == "POST" || term == "PUT" || term == "PATCH" || term == "DELETE" || term == "OPTIONS" || term == "CONNECT" || term == "TRACE" || term == "write":
			q.Methods = append(q.Methods, term)
		case term == "err" || coreStatusTerm.MatchString(term):
			q.Statuses = append(q.Statuses, term)
		case strings.HasPrefix(term, "host:"):
			host := strings.TrimPrefix(term, "host:")
			if host == "" || strings.ContainsAny(host, "/ ?#@") {
				return q, coreArgument("host: requires a hostname")
			}
			q.Hosts = append(q.Hosts, host)
		case strings.HasPrefix(term, "type:"):
			kind := strings.TrimPrefix(term, "type:")
			if !map[string]bool{"api": true, "script": true, "document": true, "media": true, "image": true, "font": true, "css": true, "xhr": true}[kind] {
				return q, coreArgument("type: must be api, script, document, media, image, font, css, or xhr")
			}
			q.Types = append(q.Types, kind)
		case strings.HasPrefix(term, "has:"):
			value := strings.TrimPrefix(term, "has:")
			if value == "" {
				return q, coreArgument("has: requires literal text")
			}
			q.Has = append(q.Has, value)
		case strings.HasPrefix(term, "in:"):
			field := strings.TrimPrefix(term, "in:")
			if field != "url" && field != "headers" && field != "body" {
				return q, coreArgument("in: must be url, headers, or body")
			}
			if fieldSet && q.In != field {
				return q, coreArgument("provide one in: field for has: and ~regex")
			}
			q.In, fieldSet = field, true
		case strings.HasPrefix(term, "since:"):
			d, e := time.ParseDuration(strings.TrimPrefix(term, "since:"))
			if e != nil || d <= 0 {
				return q, coreArgument("since: requires a positive duration")
			}
			if q.Since == 0 || d < q.Since {
				q.Since = d
			}
		case strings.HasPrefix(term, "~"):
			if len(term) == 1 {
				return q, coreArgument("~ requires a regular expression")
			}
			r, e := regexp.Compile(term[1:])
			if e != nil {
				return q, coreArgument("invalid network regular expression")
			}
			q.Regex = append(q.Regex, r)
		default:
			return q, coreArgument("unknown network query term; use METHOD, STATUS, host:, type:, has:, in:, since:, or ~regex")
		}
	}
	if fieldSet && len(q.Has) == 0 && len(q.Regex) == 0 {
		return q, coreArgument("in: requires has: or ~regex")
	}
	return q, nil
}
func coreRequestType(r *store.Request) string {
	kind := strings.ToLower(r.ResourceType)
	switch kind {
	case "stylesheet":
		return "css"
	case "fetch", "xmlhttprequest", "xhr":
		return "xhr"
	case "audio", "video":
		return "media"
	}
	if kind != "" && kind != "other" {
		return kind
	}
	ct := ""
	if r.Response != nil {
		ct = strings.ToLower(store.HeaderFirst(r.Response.Headers, "content-type"))
	}
	switch {
	case strings.Contains(ct, "json"):
		return "xhr"
	case strings.Contains(ct, "javascript"):
		return "script"
	case strings.Contains(ct, "text/html"):
		return "document"
	case strings.Contains(ct, "text/css"):
		return "css"
	case strings.HasPrefix(ct, "image/"):
		return "image"
	case strings.HasPrefix(ct, "audio/"), strings.HasPrefix(ct, "video/"), strings.Contains(ct, "mpegurl"), strings.Contains(ct, "dash+xml"):
		return "media"
	case strings.HasPrefix(ct, "font/"), strings.Contains(ct, "woff"):
		return "font"
	}
	return "other"
}
func (q coreNetQuery) match(r *store.Request) bool {
	method := strings.ToUpper(r.Method)
	for _, want := range q.Methods {
		if want == "write" {
			if method == "GET" || method == "HEAD" || method == "OPTIONS" {
				return false
			}
		} else if method != want {
			return false
		}
	}
	status := 0
	if r.Response != nil {
		status = r.Response.Status
	}
	for _, want := range q.Statuses {
		if want == "err" {
			if status < 400 && r.ErrorText == "" && r.NetworkState != "failed" {
				return false
			}
		} else if strings.HasSuffix(want, "xx") {
			if strconv.Itoa(status/100) != want[:1] {
				return false
			}
		} else if strconv.Itoa(status) != want {
			return false
		}
	}
	u, _ := url.Parse(r.URL)
	for _, host := range q.Hosts {
		if u == nil || !strings.EqualFold(u.Hostname(), host) {
			return false
		}
	}
	kind := coreRequestType(r)
	for _, want := range q.Types {
		if want == "api" {
			if kind != "xhr" && !(r.Response != nil && strings.Contains(strings.ToLower(store.HeaderFirst(r.Response.Headers, "content-type")), "json")) {
				return false
			}
		} else if kind != want {
			return false
		}
	}
	if q.Since > 0 && (r.Timestamp <= 0 || time.UnixMilli(r.Timestamp).Before(q.Now.Add(-q.Since))) {
		return false
	}
	if len(q.Has) == 0 && len(q.Regex) == 0 {
		return true
	}
	text := q.subject(r)
	for _, want := range q.Has {
		if !strings.Contains(text, want) {
			return false
		}
	}
	for _, pattern := range q.Regex {
		if !pattern.MatchString(text) {
			return false
		}
	}
	return true
}
func (q coreNetQuery) subject(r *store.Request) string {
	switch q.In {
	case "headers":
		headers := map[string]any{"request": output.RedactHeaders(r.Headers)}
		if r.Response != nil {
			headers["response"] = output.RedactHeaders(r.Response.Headers)
		}
		data, _ := json.Marshal(headers)
		return string(data)
	case "body":
		values := []string{}
		for _, requestSide := range []bool{true, false} {
			data, ct, _, err := bodyEvidence(r, requestSide)
			if err == nil {
				values = append(values, output.RedactBody(string(data), ct))
			}
		}
		return strings.Join(values, "\n")
	default:
		return output.RedactURL(r.URL)
	}
}
func newCoreNetCommand() *cobra.Command {
	command := &cobra.Command{Use: "net [QUERY...|s_CAPTURE|p_CURSOR]", Short: "Query captured traffic; terms are ANDed", Args: cobra.ArbitraryArgs}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := coreNet(cmd, args); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}
func coreNet(command *cobra.Command, args []string) error {
	handle := ""
	terms := []string{}
	offset := 0
	now := time.Now()
	digest := ""
	index, err := coreStore()
	if err != nil {
		return err
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "s_") {
			if handle != "" {
				return coreArgument("provide one capture")
			}
			handle = arg
		} else if strings.HasPrefix(arg, "p_") {
			if len(args) != 1 {
				return coreArgument("a network cursor already includes its query")
			}
			cursor, e := index.Cursor(arg)
			if e != nil {
				return e
			}
			if cursor.Kind != "net" {
				return coreArgument("cursor is not a network query")
			}
			handle, offset, digest = cursor.Source, cursor.Offset, cursor.Digest
			if e = json.Unmarshal([]byte(cursor.Find), &terms); e != nil {
				return coreArgument("invalid network cursor")
			}
			now, e = time.Parse(time.RFC3339Nano, cursor.Section)
			if e != nil {
				now = cursor.Created
			}
		} else {
			terms = append(terms, arg)
		}
	}
	query, err := parseCoreNet(terms)
	if err != nil {
		return err
	}
	query.Now = now
	capture, snapshot, err := loadCoreDataCapture(handle)
	if err != nil {
		if problem, ok := err.(*agentstate.Error); ok && problem.Code == "capture_required" {
			selected, _ := scope.Current()
			state, stateErr := index.Read()
			if stateErr != nil {
				return stateErr
			}
			candidates := []string{}
			for _, tab := range state.Tabs {
				if tab.Owner == selected.Identity() && !tab.Closed && tab.TransportOrigin == "" {
					candidates = append(candidates, tab.Handle)
				}
			}
			if len(candidates) == 1 {
				problem.Next = coreNext("page", candidates[0], "--capture", "--reload")
			} else {
				problem.Next = coreNext("tabs")
			}
		}
		return err
	}
	sourceDigest := coreDataDigest(snapshot)
	if digest != "" && digest != sourceDigest {
		return agentstate.Failure("archive_changed", "the retained capture changed; start a fresh query", coreNext("net", capture.ID))
	}
	matches := []*store.Request{}
	for i := range snapshot.Requests {
		if query.match(&snapshot.Requests[i]) {
			matches = append(matches, &snapshot.Requests[i])
		}
	}
	if offset < 0 || offset > len(matches) {
		return coreArgument("network cursor is outside its capture")
	}
	primary := coreCaptureHost(snapshot)
	rows := []map[string]any{}
	used := 0
	nextOffset := offset
	projectionNext := ""
	budget := 6500
	if len(terms) == 0 && offset == 0 {
		budget = 3500
	}
	for i := offset; i < len(matches); i++ {
		r := matches[i]
		u, _ := url.Parse(output.RedactURL(r.URL))
		host, path := "", ""
		if u != nil {
			host, path = u.Host, u.EscapedPath()
		}
		status := 0
		if r.Response != nil {
			status = r.Response.Status
		}
		data, _, evidence, e := bodyEvidence(r, false)
		state := evidence.State
		if e != nil {
			state = "invalid"
		}
		row := map[string]any{"id": r.ID, "method": coreByteClip(r.Method, 24), "host": coreByteClip(host, 256), "path": coreByteClip(path, 1024), "status": status, "type": coreRequestType(r), "body_state": state, "bytes": len(data), "primary": u != nil && strings.EqualFold(u.Hostname(), primary)}
		if len(path) > 1024 {
			row["path_truncated"] = true
		}
		if len(query.Regex) > 0 {
			row["matches"], row["matches_complete"] = coreNetProjection(query, r)
		}
		encoded, _ := json.Marshal(row)
		if used+len(encoded) > budget && len(rows) > 0 {
			break
		}
		if projectionNext == "" && (row["path_truncated"] == true || row["matches_complete"] == false) {
			projectionNext = coreNext("body", r.ID)
		}
		rows = append(rows, row)
		used += len(encoded)
		nextOffset = i + 1
	}
	meta := map[string]any{"capture": capture.ID, "source_archive": capture.ID, "total": len(matches), "returned": len(rows), "offset": offset}
	untrusted := map[string]any{"requests": rows}
	sourceComplete := true
	if health := snapshot.BrowserSession; health != nil {
		sourceComplete = !health.TimedOut && health.CaptureError == "" && health.DroppedRequests == 0 && len(health.CaptureWarnings) == 0
		meta["capture_health"] = map[string]any{"timed_out": health.TimedOut, "received": health.ReceivedRequests, "dropped": health.DroppedRequests, "warnings": len(health.CaptureWarnings), "failed": health.CaptureError != ""}
	}
	meta["source_complete"] = sourceComplete
	result := coreResult{Browser: capture.Browser.Name, Profile: capture.Browser.Profile, Status: "ready", Complete: boolPointer(nextOffset == len(matches) && sourceComplete && projectionNext == ""), Meta: meta, Untrusted: untrusted}
	result.Next = projectionNext
	if nextOffset < len(matches) {
		encoded, _ := json.Marshal(terms)
		cursor, e := index.SaveCursor(agentstate.Cursor{Owner: capture.Owner, Tab: capture.Tab, Kind: "net", Source: capture.ID, Offset: nextOffset, Find: string(encoded), Section: now.Format(time.RFC3339Nano), Digest: sourceDigest, Budget: budget})
		if e != nil {
			return e
		}
		result.Next = coreNext("net", cursor)
	}
	if len(terms) == 0 && offset == 0 {
		coreNetOverview(snapshot, matches, primary, meta, untrusted)
	}
	return emitCore(command, result)
}
func coreNetProjection(q coreNetQuery, r *store.Request) ([]any, bool) {
	rows := []any{}
	seen := map[string]bool{}
	used := 0
	for _, pattern := range q.Regex {
		// A bounded projection is explicit when it omits matches; body keeps the full evidence.
		matches := pattern.FindAllStringSubmatch(q.subject(r), 65)
		for _, match := range matches {
			encoded, _ := json.Marshal(match)
			if seen[string(encoded)] {
				continue
			}
			seen[string(encoded)] = true
			if len(rows) >= 16 || used+len(encoded) > 1800 {
				return rows, false
			}
			if len(match) == 1 {
				rows = append(rows, match[0])
			} else {
				rows = append(rows, map[string]any{"value": match[0], "groups": match[1:]})
			}
			used += len(encoded)
		}
		if len(matches) == 65 {
			return rows, false
		}
	}
	return rows, true
}
func coreCaptureHost(snapshot store.Export) string {
	if snapshot.BrowserSession != nil {
		if u, e := url.Parse(snapshot.BrowserSession.URL); e == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	for _, r := range snapshot.Requests {
		if u, e := url.Parse(r.PageURL); e == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	return ""
}
func coreNetOverview(snapshot store.Export, matches []*store.Request, primary string, meta, untrusted map[string]any) {
	groups, hosts := map[string]int{}, map[string]int{}
	for _, r := range matches {
		u, e := url.Parse(output.RedactURL(r.URL))
		if e != nil {
			continue
		}
		hosts[u.Hostname()]++
		groups[r.Method+" "+u.Scheme+"://"+u.Host+u.EscapedPath()]++
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	overview := []map[string]any{}
	for _, key := range keys[:min(len(keys), 8)] {
		overview = append(overview, map[string]any{"route": coreByteClip(key, 160), "count": groups[key]})
	}
	hostRows := []map[string]any{}
	keys = nil
	for key := range hosts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, host := range keys[:min(len(keys), 8)] {
		hostRows = append(hostRows, map[string]any{"host": coreByteClip(host, 256), "count": hosts[host], "primary": strings.EqualFold(host, primary)})
	}
	untrusted["groups"], untrusted["hosts"] = overview, hostRows
	meta["overview_complete"] = len(groups) <= 8 && len(hosts) <= 8
	meta["groups_total"], meta["hosts_total"] = len(groups), len(hosts)
	if health := snapshot.BrowserSession; health != nil {
		meta["capture_health"] = map[string]any{"timed_out": health.TimedOut, "received": health.ReceivedRequests, "dropped": health.DroppedRequests, "warnings": len(health.CaptureWarnings), "failed": health.CaptureError != ""}
	}
}
func init() { rootCmd.AddCommand(newCoreNetCommand()) }
func coreByteClip(value string, size int) string {
	if len(value) <= size {
		return value
	}
	end := size
	for end > 0 && (value[end]&0xc0) == 0x80 {
		end--
	}
	return value[:end]
}
