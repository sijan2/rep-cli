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
	Methods             map[string]bool
	Statuses            []string
	Host, Type, Has, In string
	Regex               *regexp.Regexp
	Since               time.Duration
}

func parseCoreNet(terms []string) (coreNetQuery, error) {
	query := coreNetQuery{Methods: map[string]bool{}, In: "url"}
	for _, term := range terms {
		switch {
		case term == "GET" || term == "HEAD" || term == "POST" || term == "PUT" || term == "PATCH" || term == "DELETE" || term == "OPTIONS" || term == "write":
			query.Methods[term] = true
		case term == "err" || regexp.MustCompile(`^[1-5](?:[0-9]{2}|xx)$`).MatchString(term):
			query.Statuses = append(query.Statuses, term)
		case strings.HasPrefix(term, "host:"):
			query.Host = strings.TrimPrefix(term, "host:")
			if query.Host == "" {
				return query, coreArgument("host: requires a hostname")
			}
		case strings.HasPrefix(term, "type:"):
			query.Type = strings.TrimPrefix(term, "type:")
			if !map[string]bool{"api": true, "script": true, "document": true, "media": true, "image": true, "font": true, "css": true, "xhr": true}[query.Type] {
				return query, coreArgument("unknown network type")
			}
		case strings.HasPrefix(term, "has:"):
			query.Has = strings.TrimPrefix(term, "has:")
		case strings.HasPrefix(term, "in:"):
			query.In = strings.TrimPrefix(term, "in:")
			if query.In != "url" && query.In != "headers" && query.In != "body" {
				return query, coreArgument("in: must be url, headers, or body")
			}
		case strings.HasPrefix(term, "since:"):
			duration, e := time.ParseDuration(strings.TrimPrefix(term, "since:"))
			if e != nil || duration <= 0 {
				return query, coreArgument("since: requires a positive duration")
			}
			query.Since = duration
		case strings.HasPrefix(term, "~"):
			pattern, e := regexp.Compile(strings.TrimPrefix(term, "~"))
			if e != nil {
				return query, coreArgument("invalid network regular expression")
			}
			query.Regex = pattern
		default:
			return query, coreArgument("unknown network query term")
		}
	}
	return query, nil
}
func coreRequestType(request *store.Request) string {
	value := strings.ToLower(request.ResourceType)
	switch value {
	case "stylesheet":
		return "css"
	case "fetch", "xmlhttprequest":
		return "xhr"
	}
	if value != "" {
		return value
	}
	if request.Response != nil && strings.Contains(store.HeaderFirst(request.Response.Headers, "content-type"), "json") {
		return "xhr"
	}
	return "other"
}
func (query coreNetQuery) match(request *store.Request) bool {
	method := strings.ToUpper(request.Method)
	for want := range query.Methods {
		if want == "write" {
			if method == "GET" || method == "HEAD" || method == "OPTIONS" {
				return false
			}
		} else if method != want {
			return false
		}
	}
	status := 0
	if request.Response != nil {
		status = request.Response.Status
	}
	for _, want := range query.Statuses {
		if want == "err" {
			if status < 400 && request.ErrorText == "" {
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
	parsed, _ := url.Parse(request.URL)
	if query.Host != "" && (parsed == nil || !strings.EqualFold(parsed.Hostname(), query.Host)) {
		return false
	}
	kind := coreRequestType(request)
	if query.Type != "" {
		if query.Type == "api" {
			if kind != "xhr" && !(request.Response != nil && strings.Contains(store.HeaderFirst(request.Response.Headers, "content-type"), "json")) {
				return false
			}
		} else if kind != query.Type {
			return false
		}
	}
	if query.Since > 0 && time.Since(time.UnixMilli(request.Timestamp)) > query.Since {
		return false
	}
	value := query.subject(request)
	return (query.Has == "" || strings.Contains(value, query.Has)) && (query.Regex == nil || query.Regex.MatchString(value))
}
func (query coreNetQuery) subject(request *store.Request) string {
	switch query.In {
	case "headers":
		headers := map[string]any{"request": output.RedactHeaders(request.Headers)}
		if request.Response != nil {
			headers["response"] = output.RedactHeaders(request.Response.Headers)
		}
		data, _ := json.Marshal(headers)
		return string(data)
	case "body":
		data, contentType, _, e := bodyEvidence(request, false)
		if e != nil {
			return ""
		}
		return output.RedactBody(string(data), contentType)
	default:
		return output.RedactURL(request.URL)
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
		} else if agentstate.CursorPattern.MatchString(arg) {
			if len(args) > 1 {
				return coreArgument("a network cursor already includes its query")
			}
			cursor, e := index.Cursor(arg)
			if e != nil {
				return e
			}
			if cursor.Kind != "net" {
				return coreArgument("cursor is not a network query")
			}
			handle, offset = cursor.Source, cursor.Offset
			if e = json.Unmarshal([]byte(cursor.Find), &terms); e != nil {
				return e
			}
		} else {
			terms = append(terms, arg)
		}
	}
	query, err := parseCoreNet(terms)
	if err != nil {
		return err
	}
	capture, snapshot, err := loadCoreCapture(handle)
	if err != nil {
		return err
	}
	matches := []*store.Request{}
	for i := range snapshot.Requests {
		request := &snapshot.Requests[i]
		if query.match(request) {
			matches = append(matches, request)
		}
	}
	rows := []map[string]any{}
	budget := 7500
	if len(terms) == 0 && offset == 0 {
		budget = 4000
	}
	used := 0
	nextOffset := offset
	for i := offset; i < len(matches); i++ {
		request := matches[i]
		parsed, _ := url.Parse(request.URL)
		host, path := "", ""
		if parsed != nil {
			host, path = parsed.Host, parsed.EscapedPath()
		}
		status := 0
		if request.Response != nil {
			status = request.Response.Status
		}
		data, _, evidence, _ := bodyEvidence(request, false)
		row := map[string]any{"id": request.ID, "method": request.Method, "host": host, "path": path, "status": status, "type": coreRequestType(request), "body_state": evidence.State, "bytes": len(data)}
		if query.Regex != nil {
			row["matches"] = query.Regex.FindAllString(query.subject(request), 10)
		}
		encoded, _ := json.Marshal(row)
		if used+len(encoded) > budget && len(rows) > 0 {
			break
		}
		if len(encoded) > budget {
			delete(row, "matches")
		}
		rows = append(rows, row)
		used += len(encoded)
		nextOffset = i + 1
	}
	result := coreResult{Browser: capture.Browser.Name, Profile: capture.Browser.Profile, Status: "ready", Complete: boolPointer(nextOffset == len(matches)), Meta: map[string]any{"capture": capture.ID, "total": len(matches), "returned": len(rows)}, Untrusted: map[string]any{"requests": rows}}
	if nextOffset < len(matches) {
		selected, _ := scope.Current()
		encoded, _ := json.Marshal(terms)
		cursor, e := index.SaveCursor(agentstate.Cursor{Owner: selected.Identity(), Tab: capture.Tab, Kind: "net", Source: capture.ID, Offset: nextOffset, Find: string(encoded), Budget: 8000})
		if e != nil {
			return e
		}
		result.Next = coreNext("net", cursor)
	}
	if len(terms) == 0 && offset == 0 {
		primary := ""
		if snapshot.BrowserSession != nil {
			u, _ := url.Parse(snapshot.BrowserSession.URL)
			if u != nil {
				primary = u.Hostname()
			}
		}
		groups := map[string]int{}
		hosts := map[string]int{}
		for _, request := range matches {
			u, _ := url.Parse(request.URL)
			if u == nil {
				continue
			}
			hosts[u.Hostname()]++
			groups[request.Method+" "+u.Scheme+"://"+u.Host+u.EscapedPath()]++
		}
		keys := []string{}
		for key := range groups {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		overview := []map[string]any{}
		for i, key := range keys {
			if i >= 12 {
				break
			}
			overview = append(overview, map[string]any{"route": coreByteClip(key, 256), "count": groups[key]})
		}
		hostRows := []map[string]any{}
		keys = nil
		for host := range hosts {
			keys = append(keys, host)
		}
		sort.Strings(keys)
		for i, host := range keys {
			if i >= 12 {
				break
			}
			hostRows = append(hostRows, map[string]any{"host": host, "count": hosts[host], "primary": host == primary})
		}
		result.Untrusted.(map[string]any)["groups"] = overview
		result.Untrusted.(map[string]any)["hosts"] = hostRows
		if health := snapshot.BrowserSession; health != nil {
			result.Meta.(map[string]any)["capture_health"] = map[string]any{"timed_out": health.TimedOut, "received": health.ReceivedRequests, "dropped": health.DroppedRequests, "warnings": health.CaptureWarnings}
		}
	}
	return emitCore(command, result)
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
