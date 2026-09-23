package contextview

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/store"
)

func testInput(requests ...store.Request) Input {
	return Input{
		Requests: requests, ScopeID: "/work/project/task-a", SourceID: "live:/selected/capture.json", Source: "live",
		Provenance: Provenance{Workspace: "project", Task: "task-a", SourceStatus: "captured"},
	}
}

func testRequest(id, route string, timestamp int64) store.Request {
	return store.Request{ID: id, Method: "GET", URL: "https://example.test" + route, Timestamp: timestamp,
		ResourceType: "fetch", Response: &store.Response{Status: 200, Body: "private response"}}
}

func buildView(t *testing.T, input Input, options Options) (View, []byte) {
	t.Helper()
	data, err := Build(input, options)
	if err != nil {
		t.Fatal(err)
	}
	budget := options.Budget
	if budget == 0 {
		budget = DefaultBudget
	}
	if len(data)+1 > budget {
		t.Fatalf("output including newline is %d bytes, budget %d", len(data)+1, budget)
	}
	var view View
	if err := json.Unmarshal(data, &view); err != nil {
		t.Fatal(err)
	}
	return view, data
}

func TestCompressesDuplicateRoutesAndKeepsRepresentativeDiversity(t *testing.T) {
	requests := make([]store.Request, 2000)
	for i := range requests {
		requests[i] = testRequest(fmt.Sprintf("req-%04d", i), fmt.Sprintf("/api/items/%d?token=PRIVATE_QUERY", i), int64(i+1))
		if i == 20 {
			requests[i].Response.Status = 304
		}
		if i == 21 {
			requests[i].Response.Status = 404
		}
	}
	view, encoded := buildView(t, testInput(requests...), Options{CacheDir: t.TempDir()})
	if len(view.Groups) != 1 || view.Totals.Requests != 2000 || !view.Complete {
		t.Fatalf("unexpected view: %+v", view)
	}
	group := view.Groups[0]
	if group.Route != "/api/items/:n" || group.Count != 2000 || len(group.IDs) != 3 || group.OtherIDs != 1997 {
		t.Fatalf("unexpected grouping: %+v", group)
	}
	if !reflect.DeepEqual(group.IDs, []string{"req-1999", "req-0021", "req-0020"}) {
		t.Fatalf("expected newest plus distinct statuses: %v", group.IDs)
	}
	if len(encoded) > 1500 || strings.Contains(string(encoded), "PRIVATE_QUERY") || strings.Contains(string(encoded), "private response") {
		t.Fatalf("not compact or leaked capture content: %s", encoded)
	}
}

func TestDropsSecretsAndNormalizesIdentifiers(t *testing.T) {
	request := testRequest("req-1", "/api/users/550e8400-e29b-41d4-a716-446655440000/token/PATH_PRIVATE?auth=QUERY_PRIVATE#FRAGMENT_PRIVATE", 1)
	request.URL = strings.Replace(request.URL, "https://", "https://USER_PRIVATE:PASS_PRIVATE@", 1)
	request.Headers = store.HeaderMap{"Authorization": {"Bearer HEADER_PRIVATE"}, "Cookie": {"COOKIE_PRIVATE"}}
	request.Body = "BODY_PRIVATE"
	request.Response.Headers = store.HeaderMap{"Set-Cookie": {"RESPONSE_COOKIE_PRIVATE"}}
	request.Response.Body = "RESPONSE_PRIVATE"
	request.PageURL = "https://page.test/?session=PAGE_PRIVATE"
	request.Initiator = "INITIATOR_PRIVATE"
	cacheDir := t.TempDir()
	view, encoded := buildView(t, testInput(request), Options{CacheDir: cacheDir})
	if view.Groups[0].Host != "example.test" || view.Groups[0].Route != "/api/users/:id/token/:value" {
		t.Fatalf("unexpected route normalization: %+v", view.Groups[0])
	}
	if strings.Contains(string(encoded), "PRIVATE") || strings.Contains(string(encoded), "Authorization") {
		t.Fatalf("capture content leaked: %s", encoded)
	}
	checkpoint, err := os.ReadFile(filepath.Join(cacheDir, view.Cursor+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(checkpoint), "PRIVATE") || strings.Contains(string(checkpoint), "example.test") || strings.Contains(string(checkpoint), "/api/") {
		t.Fatalf("checkpoint must contain only identities and hashes: %s", checkpoint)
	}
}

func TestNormalizedURLCases(t *testing.T) {
	cases := []struct{ raw, host, route string }{
		{"https://EXAMPLE.test.:443/a/123/abcdefab", "example.test", "/a/:n/:id"},
		{"https://example.test/api/products/Ab32Cd57Ef94Gh12", "example.test", "/api/products/:id"},
		{"https://example.test/api/email/person%40example.test", "example.test", "/api/email/:value"},
		{"https://example.test/api/a%2Fb", "example.test", "/api/:value"},
		{"https://example.test/api/v1/items.json?email=secret", "example.test", "/api/v1/items.json"},
		{"https://example.test/", "example.test", "/"},
		{"https://example.test", "example.test", "/"},
		{"data:text/plain,SECRET", "(unknown)", "/:unknown"},
		{"chrome-extension://abcdef/SECRET", "(local)", "/:non-http"},
		{"https://example.test/%zz", "(unknown)", "/:unknown"},
	}
	for _, tc := range cases {
		_, host, route := normalizedURL(tc.raw)
		if host != tc.host || route != tc.route {
			t.Errorf("%s: got %s %s, want %s %s", tc.raw, host, route, tc.host, tc.route)
		}
	}
}

func TestOriginIsolationAndDefaultPortNormalization(t *testing.T) {
	urls := []string{
		"http://localhost:3000/api/users/1",
		"http://localhost:4000/api/users/2",
		"http://localhost/api/users/3",
		"http://localhost:80/api/users/4",
		"https://localhost/api/users/5",
		"https://localhost:443/api/users/6",
		"https://LOCALHOST.:0443/api/users/7",
	}
	input := testInput()
	for index, raw := range urls {
		request := testRequest(fmt.Sprintf("r%d", index), "/", int64(index))
		request.URL = raw
		input.Requests = append(input.Requests, request)
	}
	view, _ := buildView(t, input, Options{CacheDir: t.TempDir()})
	counts := map[string]int{}
	for _, group := range view.Groups {
		counts[group.Origin] = group.Count
		if group.Host != "localhost" {
			t.Fatalf("hostname unexpectedly changed: %+v", group)
		}
	}
	want := map[string]int{"http://localhost:3000": 1, "http://localhost:4000": 1, "http://localhost": 2, "https://localhost": 3}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("origin grouping = %v, want %v", counts, want)
	}
	for _, test := range []struct{ raw, origin string }{
		{"http://[::1]:3000/a", "http://[::1]:3000"},
		{"https://[::1]:443/a", "https://[::1]"},
		{"wss://example.test:443/a", "wss://example.test"},
		{"ws://example.test:80/a", "ws://example.test"},
	} {
		origin, _, _ := normalizedURL(test.raw)
		if origin != test.origin {
			t.Errorf("origin of %s = %s, want %s", test.raw, origin, test.origin)
		}
	}
}

func TestDeltaDetectsLateResponseAndRemoval(t *testing.T) {
	cacheDir := t.TempDir()
	firstReq := testRequest("same-id", "/api/profile", 10)
	firstReq.Response = nil
	secondReq := testRequest("req-two", "/api/notifications", 11)
	first, _ := buildView(t, testInput(firstReq, secondReq), Options{CacheDir: cacheDir})
	unchanged, _ := buildView(t, testInput(firstReq, secondReq), Options{CacheDir: cacheDir, Since: first.Cursor})
	if !unchanged.NoChange || !unchanged.Complete || unchanged.Cursor != first.Cursor || len(unchanged.Groups) != 0 {
		t.Fatalf("same snapshot should be a no-op: %+v", unchanged)
	}
	firstReq.Response = &store.Response{Status: 200, Body: "response delivered later"}
	changed, _ := buildView(t, testInput(firstReq), Options{CacheDir: cacheDir, Since: first.Cursor})
	if changed.NoChange || len(changed.Groups) != 1 || changed.Groups[0].Change != "updated" || len(changed.Removed) != 1 || changed.Totals.Updated != 1 {
		t.Fatalf("missing response update/removal: %+v", changed)
	}
	firstReq.Response.Body = "body changed without ID, timestamp, or status changes"
	bodyChanged, _ := buildView(t, testInput(firstReq), Options{CacheDir: cacheDir, Since: changed.Cursor})
	if bodyChanged.NoChange || bodyChanged.Totals.Updated != 1 || bodyChanged.Cursor == changed.Cursor {
		t.Fatalf("response content was not included in the local digest: %+v", bodyChanged)
	}
	final, _ := buildView(t, testInput(firstReq), Options{CacheDir: cacheDir, Since: bodyChanged.Cursor})
	if !final.NoChange {
		t.Fatalf("new baseline should be unchanged: %+v", final)
	}
}

func TestPartialCursorDrainsChangesWithoutLosingUnseenGroups(t *testing.T) {
	var requests []store.Request
	for i := 0; i < 45; i++ {
		requests = append(requests, testRequest(fmt.Sprintf("r%d", i), fmt.Sprintf("/endpoint-%c%c", 'a'+i/26, 'a'+i%26), int64(i)))
	}
	cacheDir := t.TempDir()
	options := Options{Budget: 2100, CacheDir: cacheDir}
	seen := map[string]bool{}
	first, _ := buildView(t, testInput(requests...), options)
	if first.Complete || first.Omitted.Groups == 0 || first.Omitted.Requests != first.Omitted.Groups {
		t.Fatalf("expected explicit partial coverage: %+v", first)
	}
	for _, group := range first.Groups {
		seen[group.ID] = true
	}
	previous := first
	for round := 0; round < 50; round++ {
		options.Since = previous.Cursor
		next, _ := buildView(t, testInput(requests...), options)
		if next.NoChange {
			if len(seen) != len(requests) || !next.Complete {
				t.Fatalf("cursor skipped unseen groups: seen %d/%d %+v", len(seen), len(requests), next)
			}
			return
		}
		if next.Cursor == previous.Cursor || len(next.Groups) == 0 {
			t.Fatal("partial cursor did not progress")
		}
		for _, group := range next.Groups {
			if seen[group.ID] {
				t.Fatalf("already delivered group repeated: %s", group.ID)
			}
			seen[group.ID] = true
		}
		previous = next
	}
	t.Fatal("failed to drain omitted groups")
}

func TestPartialCursorRetainsUndeliveredUpdatesAndRemovals(t *testing.T) {
	var requests []store.Request
	for i := 0; i < 20; i++ {
		requests = append(requests, testRequest(fmt.Sprintf("r%d", i), fmt.Sprintf("/endpoint-%c", 'a'+i), int64(i)))
	}
	cacheDir := t.TempDir()
	first, _ := buildView(t, testInput(requests...), Options{Budget: 20000, CacheDir: cacheDir})
	requests = requests[:10]
	for i := range requests {
		requests[i].Response.Body = "new body"
	}
	options := Options{Budget: 1700, CacheDir: cacheDir, Since: first.Cursor}
	updated, removed := map[string]bool{}, map[string]bool{}
	for round := 0; round < 30; round++ {
		view, _ := buildView(t, testInput(requests...), options)
		for _, group := range view.Groups {
			if group.Change != "updated" || updated[group.ID] {
				t.Fatalf("invalid/repeated update: %+v", group)
			}
			updated[group.ID] = true
		}
		for _, id := range view.Removed {
			if removed[id] {
				t.Fatal("repeated removal")
			}
			removed[id] = true
		}
		if view.NoChange {
			if len(updated) != 10 || len(removed) != 10 {
				t.Fatalf("lost pending changes: updated %d removed %d", len(updated), len(removed))
			}
			return
		}
		options.Since = view.Cursor
	}
	t.Fatal("failed to drain updates and removals")
}

func TestCursorIsolationAndFailureAreExplicit(t *testing.T) {
	cacheDir := t.TempDir()
	input := testInput(testRequest("r1", "/api/catalog", 1))
	first, _ := buildView(t, input, Options{CacheDir: cacheDir})
	for _, change := range []struct {
		name  string
		alter func(*Input)
	}{
		{"scope", func(in *Input) { in.ScopeID = "/other/workspace/task" }},
		{"source", func(in *Input) { in.SourceID = "saved:session-2" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			other := input
			change.alter(&other)
			if _, err := Build(other, Options{CacheDir: cacheDir, Since: first.Cursor}); err == nil || !strings.Contains(err.Error(), "different scope or source") {
				t.Fatalf("expected foreign cursor error: %v", err)
			}
		})
	}
	for _, cursor := range []string{"../../secret", "c1_" + strings.Repeat("a", 64)} {
		if _, err := Build(input, Options{CacheDir: cacheDir, Since: cursor}); err == nil {
			t.Fatal("must reject missing/malformed cursor instead of silently returning a full view")
		}
	}
	if _, err := Build(input, Options{CacheDir: t.TempDir(), Since: first.Cursor}); err == nil {
		t.Fatal("cursor from another cache should fail")
	}
}

func TestDeterministicDespiteRequestAndMapOrder(t *testing.T) {
	one := testRequest("r1", "/api/items/1", 1)
	one.Headers = store.HeaderMap{"X-B": {"b"}, "X-A": {"a"}}
	two := testRequest("r2", "/api/profile", 2)
	three := testRequest("r3", "/api/items/2", 3)
	options := Options{CacheDir: t.TempDir()}
	_, first := buildView(t, testInput(one, two, three), options)
	one.Headers = store.HeaderMap{"X-A": {"a"}, "X-B": {"b"}}
	_, next := buildView(t, testInput(three, one, two), options)
	if string(first) != string(next) {
		t.Fatalf("output varies with input/map iteration order:\n%s\n%s", first, next)
	}
}

func TestBreadthBeforeDuplicateBucketDepth(t *testing.T) {
	var requests []store.Request
	for i := 0; i < 30; i++ {
		requests = append(requests, testRequest(fmt.Sprintf("chatty-%d", i), fmt.Sprintf("/chatty-%c%c", 'a'+i/26, 'a'+i%26), int64(1000+i)))
	}
	post := testRequest("post", "/submit", 1)
	post.Method = "POST"
	otherHost := testRequest("other-host", "/help", 1)
	otherHost.URL = "https://another.test/help"
	otherStatus := testRequest("other-status", "/missing", 1)
	otherStatus.Response.Status = 404
	requests = append(requests, post, otherHost, otherStatus)
	view, _ := buildView(t, testInput(requests...), Options{Budget: 2700, CacheDir: t.TempDir()})
	if len(view.Groups) < 4 {
		t.Fatalf("test budget must allow four groups: %+v", view)
	}
	keys := map[string]bool{}
	for _, group := range view.Groups[:4] {
		keys[group.Host+":"+group.Method+":"+group.Statuses[0].Value] = true
	}
	if len(keys) != 4 {
		t.Fatalf("chatty bucket crowded out method/host/status diversity: %+v", view.Groups[:4])
	}
}

func TestExactByteBudgetsAndNoProgressError(t *testing.T) {
	input := testInput()
	for i := 0; i < 100; i++ {
		input.Requests = append(input.Requests, testRequest(fmt.Sprintf("r%d", i), fmt.Sprintf("/name-%c%c", 'a'+i/26, 'a'+i%26), int64(i)))
	}
	cacheDir := t.TempDir()
	for _, budget := range []int{1024, 1100, 1200, 1500, 2000, 4096, 8192, 20000} {
		data, err := Build(input, Options{Budget: budget, CacheDir: cacheDir})
		if err != nil {
			if !strings.Contains(err.Error(), "budget") {
				t.Fatalf("unexpected error for %d: %v", budget, err)
			}
			continue
		}
		if len(data)+1 > budget {
			t.Errorf("budget %d returned %d bytes", budget, len(data)+1)
		}
	}
	input.Provenance = Provenance{Workspace: strings.Repeat("work", 50), Task: strings.Repeat("task", 50), CaptureSessionID: strings.Repeat("s", 100), ExportedAt: time.Now().UTC().Format(time.RFC3339), SourceStatus: "captured"}
	if _, err := Build(input, Options{Budget: 1024, CacheDir: cacheDir}); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("should fail explicitly if no progress fits: %v", err)
	}
	for _, budget := range []int{-1, MinBudget - 1, MaxBudget + 1} {
		if _, err := Build(input, Options{Budget: budget, CacheDir: cacheDir}); err == nil {
			t.Fatalf("invalid budget accepted: %d", budget)
		}
	}
}

func TestEmptySourceAndMetadataChanges(t *testing.T) {
	options := Options{CacheDir: t.TempDir()}
	input := testInput()
	input.Provenance.SourceStatus = "no_capture"
	first, _ := buildView(t, input, options)
	if !first.Complete || !first.NoChange || first.Totals.Requests != 0 || first.Groups == nil || first.Removed == nil {
		t.Fatalf("unexpected empty view: %+v", first)
	}
	input.Provenance.SourceStatus = "captured"
	input.Provenance.CaptureSessionID = "new-session"
	input.Provenance.ExportedAt = "2026-09-20T00:00:00Z"
	options.Since = first.Cursor
	next, _ := buildView(t, input, options)
	if !next.NoChange || next.Provenance.CaptureSessionID != "new-session" {
		t.Fatalf("metadata should refresh even with no group changes: %+v", next)
	}
}

func TestBoundsUntrustedStringsAndDoesNotInventRequestIDs(t *testing.T) {
	request := testRequest("unsafe/id?password", "/"+strings.Repeat("abcdefghij/", 50), 1)
	request.Method = "GET\nPRIVATE"
	request.ResourceType = strings.Repeat("PRIVATE", 100)
	input := testInput(request)
	input.Provenance.Task = strings.Repeat("工程", 100)
	view, _ := buildView(t, input, Options{CacheDir: t.TempDir()})
	group := view.Groups[0]
	if group.Method != "OTHER" || group.Types[0].Value != "other" || len(group.IDs) != 0 || group.RequestsWithoutSafeIDs != 1 || len(group.Route) > 192 {
		t.Fatalf("failed to bound untrusted input: %+v", group)
	}
	if !view.ProvenanceTruncated || len(view.Provenance.Task) > 64 {
		t.Fatalf("provenance not bounded: %+v", view.Provenance)
	}
}
