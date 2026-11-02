package commands

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

func netFixture(t *testing.T, requests []store.Request) (agentstate.Capture, store.Export) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("REP_PROFILE", "default")
	selected, err := scope.Configure(scope.Options{Workspace: "net-fixture", Task: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scope.Reset)
	index, err := coreStore()
	if err != nil {
		t.Fatal(err)
	}
	tab, err := index.Register(agentstate.Browser{Name: "headless", Profile: "synthetic-profile", Epoch: "synthetic-epoch"}, 1, selected.Identity(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	result, err := saveCoreCapture(tab, store.Export{Requests: requests, BrowserSession: &store.BrowserSession{URL: "https://fixture.test/page"}})
	if err != nil {
		t.Fatal(err)
	}
	capture, snapshot, err := loadCoreCapture(result["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return capture, snapshot
}
func netRequest(path, body string) store.Request {
	return store.Request{ID: "old-id", Method: "GET", URL: "https://fixture.test" + path, ResourceType: "Fetch", Timestamp: time.Now().UnixMilli(), Response: &store.Response{Status: 200, Headers: store.HeaderMap{"Content-Type": {"application/json"}}, Body: body}, ResponseBodyCapture: &store.BodyCapture{State: "complete", CapturedBytes: int64(len(body))}}
}
func netResult(t *testing.T, args ...string) (map[string]any, string) {
	t.Helper()
	command := &cobra.Command{Use: "net"}
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetContext(context.Background())
	if err := coreNet(command, args); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() > 10000 {
		t.Fatalf("network output exceeded budget: %d", stdout.Len())
	}
	return result, stdout.String()
}
func bodyResult(t *testing.T, id, selector string, request bool, find, out string) (map[string]any, string, error) {
	t.Helper()
	command := &cobra.Command{Use: "body"}
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetContext(context.Background())
	args := []string{id}
	if selector != "" {
		args = append(args, selector)
	}
	err := coreBody(command, args, request, find, out)
	if err != nil {
		return nil, stdout.String(), err
	}
	var result map[string]any
	if err = json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() > 10000 {
		t.Fatalf("body output exceeded budget: %d", stdout.Len())
	}
	return result, stdout.String(), nil
}
func TestCoreNetTermsAreANDedAndDoNotSearchCredentials(t *testing.T) {
	r := netRequest("/api/items?access_token=synthetic-query", `{"count":2,"access_token":"synthetic-body","items":["alpha-12","beta-34"]}`)
	r.Headers = store.HeaderMap{"Authorization": {"Bearer synthetic-header"}, "X-Debug": {"alpha"}}
	r.Body = `{"query":"gamma"}`
	for _, test := range []struct {
		terms []string
		want  bool
	}{
		{[]string{"GET", "2xx", "host:fixture.test", "type:api", "has:items"}, true},
		{[]string{"GET", "POST"}, false}, {[]string{"host:wrong.test", "host:fixture.test"}, false},
		{[]string{"type:image", "type:api"}, false}, {[]string{"has:absent", "has:items"}, false},
		{[]string{"has:gamma", "in:body"}, true}, {[]string{"has:alpha", "in:headers"}, true},
		{[]string{"has:synthetic-header", "in:headers"}, false}, {[]string{"has:synthetic-query"}, false},
		{[]string{"has:synthetic-body", "in:body"}, false}, {[]string{"~alpha-([0-9]+)", "in:body"}, true},
		{[]string{"since:1m", "since:1h"}, true},
	} {
		q, err := parseCoreNet(test.terms)
		if err != nil {
			t.Fatal(err)
		}
		if got := q.match(&r); got != test.want {
			t.Errorf("%v matched=%v", test.terms, got)
		}
	}
	for _, terms := range [][]string{{"has:"}, {"in:body"}, {"in:body", "in:url", "has:x"}, {"since:0s"}, {"~"}, {"~["}, {"type:nope"}, {"host:"}, {"typo"}} {
		if _, err := parseCoreNet(terms); err == nil {
			t.Errorf("accepted invalid query %v", terms)
		}
	}
}
func TestCoreNetPaginationOverviewFullIDsAndBoundedProjection(t *testing.T) {
	requests := []store.Request{}
	for i := 0; i < 90; i++ {
		requests = append(requests, netRequest(fmt.Sprintf("/items/%d", i), `{"item":"alpha-12","also":"alpha-12"}`))
	}
	capture, snapshot := netFixture(t, requests)
	overview, _ := netResult(t, capture.ID)
	hosts := overview["untrusted"].(map[string]any)["hosts"].([]any)
	if !hosts[0].(map[string]any)["primary"].(bool) {
		t.Fatal("page host not marked primary")
	}
	if overview["meta"].(map[string]any)["overview_complete"] != false {
		t.Fatal("truncated overview claimed complete")
	}
	result, _ := netResult(t, capture.ID, "GET", "since:1m", "~alpha-([0-9]+)", "in:body")
	seen := map[string]bool{}
	for {
		rows := result["untrusted"].(map[string]any)["requests"].([]any)
		for _, value := range rows {
			row := value.(map[string]any)
			id := row["id"].(string)
			if !coreRequestHandle.MatchString(id) || seen[id] {
				t.Fatal("duplicate or shortened handle", id)
			}
			seen[id] = true
			match := row["matches"].([]any)
			if len(match) != 1 || match[0].(map[string]any)["groups"].([]any)[0] != "12" {
				t.Fatalf("regex value projection lost groups: %v", match)
			}
		}
		next, _ := result["next"].(string)
		if next == "" {
			break
		}
		cursorID := strings.Fields(next)[2]
		index, _ := coreStore()
		cursor, _ := index.Cursor(cursorID)
		if cursor.Section == "" || cursor.Digest == "" {
			t.Fatal("cursor failed to pin query time/archive")
		}
		result, _ = netResult(t, cursorID)
	}
	if len(seen) != len(snapshot.Requests) || result["complete"] != true {
		t.Fatalf("pagination lost evidence: %d", len(seen))
	}
}
func TestCoreNetArchiveDefaultsAreFailClosed(t *testing.T) {
	capture, snapshot := netFixture(t, []store.Request{netRequest("/one", `{}`)})
	netResult(t)
	index, _ := coreStore()
	other := agentstate.Token("s_")
	if err := index.RegisterCapture(other, capture.Owner, capture.Tab, capture.Archive, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadCoreDataCapture(""); err == nil {
		t.Fatal("ambiguous default chose latest")
	}
	netResult(t, capture.ID)
	command := &cobra.Command{Use: "body"}
	if err := coreBody(command, []string{agentstate.Token("h_")}, false, "", ""); err == nil {
		t.Fatal("missing ID consulted another archive")
	}
	// Registry still points at the archive, but duplicate canonical IDs must fail.
	snapshot.Requests = append(snapshot.Requests, snapshot.Requests[0])
	encoded, _ := json.Marshal(snapshot)
	if err := os.WriteFile(capture.Archive, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := coreDataRequest(snapshot.Requests[0].ID); err == nil {
		t.Fatal("duplicate request IDs were trusted")
	}
	if _, _, err := coreCapturedRequest(snapshot.Requests[0].ID); err == nil {
		t.Fatal("replay/download trusted duplicate request IDs rejected by body/net")
	}
}
func TestCoreBodyRedactionPointersRawExportAndMetadata(t *testing.T) {
	request := netRequest("/api?access_token=synthetic-query", `{"password":"synthetic-secret","ok":true}`)
	request.Headers = store.HeaderMap{"Cookie": {"sid=synthetic-cookie"}, "Content-Type": {"application/json"}}
	request.Response.Headers["Set-Cookie"] = []string{"sid=synthetic-set-cookie"}
	capture, snapshot := netFixture(t, []store.Request{request})
	id := snapshot.Requests[0].ID
	result, text, err := bodyResult(t, id, "/password", false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"synthetic-query", "synthetic-secret", "synthetic-cookie", "synthetic-set-cookie"} {
		if strings.Contains(text, secret) {
			t.Fatal("default output leaked", secret)
		}
	}
	meta := result["meta"].(map[string]any)
	if meta["source_archive"] != capture.ID || meta["method"] != "GET" || meta["status_code"] != float64(200) {
		t.Fatal("body lost detail metadata")
	}
	path := filepath.Join(t.TempDir(), "raw.json")
	result, _, err = bodyResult(t, id, "", false, "", path)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(data) != request.Response.Body || info.Mode().Perm() != 0600 {
		t.Fatal("raw export not private/exact")
	}
	if _, _, err = bodyResult(t, id, "", false, "", path); err == nil {
		t.Fatal("overwrote existing output")
	}
	link := filepath.Join(t.TempDir(), "symlink")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err = bodyResult(t, id, "", false, "", link); err == nil {
		t.Fatal("followed output symlink")
	}
}

func TestCoreCaptureRejectsTrailingArchiveData(t *testing.T) {
	for _, suffix := range []string{` {"requests":[]}`, ` corrupted`} {
		t.Run(suffix, func(t *testing.T) {
			capture, snapshot := netFixture(t, []store.Request{netRequest("/one", `{}`)})
			encoded, err := os.ReadFile(capture.Archive)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(capture.Archive, append(encoded, suffix...), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err = coreDataRequest(snapshot.Requests[0].ID); err == nil {
				t.Fatal("accepted an archive followed by unvalidated data")
			}
		})
	}
}
func TestCoreBodyEventSelectionStopsAndRecordPaginationAdvances(t *testing.T) {
	for _, ct := range []string{"application/x-ndjson", "text/event-stream"} {
		t.Run(ct, func(t *testing.T) {
			var body strings.Builder
			for i := 0; i < 77; i++ {
				if ct == "text/event-stream" {
					fmt.Fprintf(&body, "data: {\"index\":%d}\n\n", i)
				} else {
					fmt.Fprintf(&body, "{\"index\":%d}\n", i)
				}
			}
			request := netRequest("/stream", body.String())
			request.Response.Headers["Content-Type"] = []string{ct}
			_, snapshot := netFixture(t, []store.Request{request})
			id := snapshot.Requests[0].ID
			result, _, err := bodyResult(t, id, "#1", false, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if result["next"] != nil || result["complete"] != true {
				t.Fatal("single event continued into other events", result)
			}
			selector := ""
			count := 0
			for loops := 0; loops < 10; loops++ {
				result, _, err = bodyResult(t, id, selector, false, "", "")
				if err != nil {
					t.Fatal(err)
				}
				count += len(result["untrusted"].(map[string]any)["data"].([]any))
				next, _ := result["next"].(string)
				if next == "" {
					break
				}
				selector = strings.Fields(next)[3]
			}
			if count != 77 || result["complete"] != true {
				t.Fatalf("record pagination repeated/lost rows: %d", count)
			}
		})
	}
}
func TestCoreBodyBoundedEscapingFindAndLargeSpill(t *testing.T) {
	request := netRequest("/large", strings.Repeat("needle\x00\"😀\n", 1400))
	request.Response.Headers["Content-Type"] = []string{"text/plain"}
	_, snapshot := netFixture(t, []store.Request{request})
	id := snapshot.Requests[0].ID
	selector := ""
	joined := ""
	for loops := 0; loops < 40; loops++ {
		result, _, err := bodyResult(t, id, selector, false, "", "")
		if err != nil {
			t.Fatal(err)
		}
		joined += result["untrusted"].(map[string]any)["data"].(string)
		next, _ := result["next"].(string)
		if next == "" {
			break
		}
		selector = strings.Fields(next)[3]
	}
	if joined != request.Response.Body {
		t.Fatal("byte continuation lost UTF-8 or escaped text")
	}
	result, _, err := bodyResult(t, id, "", false, "needle", "")
	if err != nil {
		t.Fatal(err)
	}
	if result["complete"] != false || result["next"] == nil {
		t.Fatal("find did not paginate")
	}
}
func TestCoreBodySpillRedactsAndPartialEvidenceStaysPartial(t *testing.T) {
	request := netRequest("/large", `{"password":"synthetic-large-secret","text":"`+strings.Repeat("x", (8<<20)+1)+`"}`)
	capture, snapshot := netFixture(t, []store.Request{request})
	id := snapshot.Requests[0].ID
	result, text, err := bodyResult(t, id, "", false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	path := result["meta"].(map[string]any)["artifact_path"].(string)
	data, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if bytes.Contains(data, []byte("synthetic-large-secret")) || strings.Contains(text, "synthetic-large-secret") || info.Mode().Perm() != 0600 {
		t.Fatal("spill leaked or lacked private mode")
	}
	snapshot.Requests[0].Response.Body = `{"partial":`
	snapshot.Requests[0].ResponseBodyCapture = &store.BodyCapture{State: "partial", CapturedBytes: int64(len(`{"partial":`))}
	encoded, _ := json.Marshal(snapshot)
	if err = os.WriteFile(capture.Archive, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	result, _, err = bodyResult(t, id, "", false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if result["complete"] != false || result["next"] != nil {
		t.Fatal("incomplete retained JSON was treated as complete or retriable")
	}
}

func TestCoreNetTruncatedProjectionPointsToBodyAndKeepsCaptureHealth(t *testing.T) {
	request := netRequest("/projection", `{"text":"`+strings.Repeat("x", 3000)+`"}`)
	capture, snapshot := netFixture(t, []store.Request{request})
	result, _ := netResult(t, capture.ID, "~x+", "in:body")
	if result["complete"] != false || !strings.HasPrefix(result["next"].(string), "rep body ") {
		t.Fatal("truncated regex projection claimed completion")
	}
	snapshot.BrowserSession.TimedOut = true
	encoded, _ := json.Marshal(snapshot)
	if err := os.WriteFile(capture.Archive, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	result, _ = netResult(t, capture.ID, "GET")
	if result["complete"] != false || result["meta"].(map[string]any)["source_complete"] != false {
		t.Fatal("incomplete capture claimed complete query evidence")
	}
}

func TestCoreBodyCapturedStreamEventUsesPrivateBinaryArtifact(t *testing.T) {
	payload := []byte{0, 1, 255, 254, 2}
	request := store.Request{RecordKind: "websocket", URL: "ws://fixture.test/stream", Stream: &store.StreamCapture{Version: 1, Protocol: "websocket", State: "closed", Events: []store.StreamEvent{{Sequence: 1, Kind: "message", Payload: base64.StdEncoding.EncodeToString(payload), PayloadEncoding: "base64", Bytes: int64(len(payload))}}, Capture: store.StreamCoverage{State: "complete", CapturedEvents: 1, ObservedEvents: 1, CapturedBytes: int64(len(payload)), ObservedBytes: int64(len(payload))}}}
	_, snapshot := netFixture(t, []store.Request{request})
	id := snapshot.Requests[0].ID
	result, _, err := bodyResult(t, id, "#1", false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	path := result["meta"].(map[string]any)["artifact_path"].(string)
	data, _ := os.ReadFile(path)
	if !bytes.Equal(data, payload) {
		t.Fatal("binary event was lossy inline text")
	}
	destination := filepath.Join(t.TempDir(), "event.bin")
	_, _, err = bodyResult(t, id, "#1", false, "", destination)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(destination)
	info, _ := os.Stat(destination)
	if !bytes.Equal(data, payload) || info.Mode().Perm() != 0600 {
		t.Fatal("raw stream export not exact/private")
	}
}

func TestCoreNetMissingCaptureNextAddressesOwnedTab(t *testing.T) {
	capture, _ := netFixture(t, []store.Request{netRequest("/one", `{}`)})
	index, _ := coreStore()
	if err := index.Update(func(state *agentstate.State) error {
		for id, entry := range state.Captures {
			entry.Created = time.Now().Add(-11 * time.Minute)
			state.Captures[id] = entry
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err := coreNet(&cobra.Command{Use: "net"}, nil)
	problem, ok := err.(*agentstate.Error)
	if !ok || problem.Code != "capture_required" || !strings.Contains(problem.Next, "rep page "+capture.Tab+" --capture --reload") {
		t.Fatalf("capture hint omitted its safe explicit target: %v", err)
	}
}
