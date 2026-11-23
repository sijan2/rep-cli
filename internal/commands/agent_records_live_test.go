package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

// newRecordsBridge is the download harness with a profile identity, which
// the core browser resolver requires before it trusts a registry.
func newRecordsBridge(t *testing.T, handler func(bridge.RPCRequest) fakeBrowserBridgeReply) *fakeBrowserBridgeServer {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rep-rec-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "bridge.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	registry := bridge.Registry{PID: os.Getpid(), Browser: "dia", Socket: socket, ProfileID: "profile_" + strings.Repeat("a", 32), Epoch: "epoch_" + strings.Repeat("b", 32), StartedAt: now, LastSeen: now}
	data, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bridge-test.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REP_BRIDGE_DIR", dir)
	server := &fakeBrowserBridgeServer{listener: listener, handler: handler, closed: make(chan struct{})}
	go server.serve()
	t.Cleanup(server.Close)
	return server
}

type ledgerFixtureBody struct {
	seq  uint64
	data []byte
}

func (b ledgerFixtureBody) sha() string {
	sum := sha256.Sum256(b.data)
	return hex.EncodeToString(sum[:])
}

func ledgerEntry(seq uint64, method, rawURL, kind, postData string, body ledgerFixtureBody, status int) map[string]any {
	entry := map[string]any{
		"seq": seq, "rev": 2, "request_id": "req-" + method, "loader_id": "L1", "frame_id": "main-frame", "type": kind, "method": method, "url": rawURL,
		"initiator": map[string]any{"type": "other", "url": ""},
		"request":   map[string]any{"headers": [][]any{{"accept", "text/html"}, {"authorization", map[string]any{"redacted": true, "bytes": 12, "sha8": "deadbeef"}}}},
		"response":  map[string]any{"status": status, "mime": "text/html", "protocol": "h2", "from_sw": false, "from_cache": false, "headers": [][]any{{"content-type", "text/html; charset=utf-8"}, {"x-ratelimit-remaining", "97"}, {"x-ratelimit-reset", "412"}}},
		"body":      map[string]any{"state": "complete", "bytes": len(body.data), "sha256": body.sha(), "encoding": "base64"},
		"t":         map[string]any{"start": 1, "response": 2, "end": 3},
	}
	if postData != "" {
		entry["request"].(map[string]any)["post_data"] = postData
	}
	return entry
}

const recordsFixtureThreadURL = "https://www.reddit.com/r/programming/comments/p000001/title/"

func recordsLiveHandler(t *testing.T, state string, bodies map[uint64]ledgerFixtureBody, entries []map[string]any) func(bridge.RPCRequest) fakeBrowserBridgeReply {
	t.Helper()
	return func(request bridge.RPCRequest) fakeBrowserBridgeReply {
		ok := func(result any) fakeBrowserBridgeReply { return fakeBrowserBridgeReply{result: result} }
		switch fakeBrowserRequestName(request) {
		case "bridge.ping":
			return ok(map[string]any{"connected": true})
		case "browser.create":
			return ok(map[string]any{"created": true, "tab_id": 77})
		case "browser.ledger.open":
			return ok(map[string]any{"ledger_id": "lg_fixture", "seq": 0, "main_frame_id": "main-frame", "attached": true})
		case "browser.lease.acquire":
			return ok(map[string]any{"lease_id": "l_" + strings.Repeat("c", 32), "generation": "g1"})
		case "browser.cdp/Page.navigate":
			return ok(map[string]any{"result": map[string]any{"frameId": "main-frame", "loaderId": "L1"}})
		case "browser.ledger.read":
			return ok(map[string]any{"entries": entries, "navs": []any{map[string]any{"seq": 1, "url": recordsFixtureThreadURL, "same_document": false, "loader_id": "L1", "frame_id": "main-frame"}}, "next_seq": 10, "state": "open", "tab": map[string]any{"url": recordsFixtureThreadURL, "active": false}, "stats": map[string]any{"entries": len(entries), "bodies": len(bodies), "body_bytes": 1, "evicted": 0, "skipped_by_reason": map[string]any{"static_type": 12}}})
		case "browser.ledger.body":
			var params struct {
				Seq uint64 `json:"seq"`
			}
			decodeFakeBrowserParams(t, request, &params)
			body, found := bodies[params.Seq]
			if !found {
				return fakeBrowserBridgeReply{rpcErr: &bridge.RPCError{Code: "body_unavailable", Message: "no such body"}}
			}
			return ok(map[string]any{"seq": params.Seq, "offset": 0, "length": len(body.data), "total": len(body.data), "sha256": body.sha(), "encoding": "base64", "data": base64.StdEncoding.EncodeToString(body.data)})
		case "browser.ledger.close":
			return ok(map[string]any{"closed": true})
		case "browser.cdp/Page.close":
			return ok(map[string]any{"result": map[string]any{}})
		case "browser.lease.release":
			return ok(map[string]any{"released": true})
		}
		return fakeBrowserBridgeReply{rpcErr: &bridge.RPCError{Code: "unknown_method", Message: fakeBrowserRequestName(request)}}
	}
}

func TestRecordsLiveReadsTheLedgerWithoutInput(t *testing.T) {
	recordsTestScope(t)
	primary, err := os.ReadFile(recordsFixturePath("reddit_thread_ssr.html"))
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := os.ReadFile(recordsFixturePath("reddit_more_comments.synthetic.html"))
	if err != nil {
		t.Fatal(err)
	}
	bodies := map[uint64]ledgerFixtureBody{3: {3, primary}, 7: {7, fragment}}
	entries := []map[string]any{
		ledgerEntry(2, "GET", "https://www.redditstatic.com/shreddit/app.js", "Script", "", ledgerFixtureBody{}, 200),
		ledgerEntry(3, "GET", recordsFixtureThreadURL, "Document", "", bodies[3], 200),
		ledgerEntry(7, "POST", "https://www.reddit.com/svc/shreddit/more-comments/r/programming/t3_p000001", "Fetch", "cursor=cursor_17", bodies[7], 200),
	}
	entries[0]["body"] = map[string]any{"state": "skipped", "reason": "static_type", "bytes": 0}
	server := newRecordsBridge(t, recordsLiveHandler(t, "allow", bodies, entries))

	result, data, err := runRecords(t, recordsOptions{Max: 5}, recordsFixtureThreadURL)
	if err != nil {
		t.Fatalf("live read failed: %v\n%s", err, data)
	}
	c := result.Meta.Coverage
	if result.Meta.Source != "url" || c.Records != 104 || c.Pending != 57 || result.Meta.Frontier.Total != 58 || result.Status != "partial" {
		t.Fatalf("live result: %s", data)
	}
	names := fakeBrowserRequestNames(server.Requests())
	index := func(name string) int {
		for i, item := range names {
			if item == name {
				return i
			}
		}
		return -1
	}
	if index("browser.ledger.open") < 0 || index("browser.cdp/Page.navigate") < 0 || index("browser.ledger.open") > index("browser.cdp/Page.navigate") {
		t.Fatalf("the ledger must open before navigation: %v", names)
	}
	for _, name := range names {
		if strings.HasPrefix(name, "browser.cdp/Input.") || strings.HasPrefix(name, "browser.cdp/Runtime.evaluate") || name == "browser.fetch" || name == "browser.eval" {
			t.Fatalf("live read dispatched input or scripts: %v", names)
		}
	}
	if index("browser.ledger.close") < 0 || index("browser.cdp/Page.close") < 0 {
		t.Fatalf("a read without --keep-tab must close its ledger and tab: %v", names)
	}
	var full map[string]any
	if err := json.Unmarshal(data, &full); err != nil {
		t.Fatal(err)
	}
	meta := full["meta"].(map[string]any)
	if meta["policy"] != "reddit@1" || meta["next"] != nil || full["next"] != nil {
		t.Fatalf("meta: %v next: %v", meta["policy"], full["next"])
	}
	if ledger, _ := meta["ledger"].(map[string]any); ledger["id"] != "lg_fixture" {
		t.Fatalf("ledger meta: %v", meta["ledger"])
	}
	if rate, _ := meta["rate"].(map[string]any); rate["headers_present"] != true || rate["remaining"] != float64(97) {
		t.Fatalf("rate meta: %v", meta["rate"])
	}
	if bytes.Contains(data, []byte("deadbeef")) || bytes.Contains(data, []byte("authorization")) {
		t.Fatal("request headers leaked into the result")
	}
}

func TestRecordsLiveKeepTabLeavesTheLedgerForALaterRead(t *testing.T) {
	recordsTestScope(t)
	primary, err := os.ReadFile(recordsFixturePath("reddit_thread_ssr.html"))
	if err != nil {
		t.Fatal(err)
	}
	bodies := map[uint64]ledgerFixtureBody{3: {3, primary}}
	entries := []map[string]any{ledgerEntry(3, "GET", recordsFixtureThreadURL, "Document", "", bodies[3], 200)}
	server := newRecordsBridge(t, recordsLiveHandler(t, "allow", bodies, entries))
	result, data, err := runRecords(t, recordsOptions{Max: 1, KeepTab: true}, recordsFixtureThreadURL)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	var full map[string]any
	_ = json.Unmarshal(data, &full)
	next, _ := full["next"].(string)
	tab, _ := full["tab"].(string)
	if tab == "" || !strings.HasPrefix(next, "rep page "+tab) {
		t.Fatalf("kept tab must point the agent at the page: %s", data)
	}
	for _, name := range fakeBrowserRequestNames(server.Requests()) {
		if name == "browser.ledger.close" || name == "browser.cdp/Page.close" || name == "browser.close" {
			t.Fatalf("--keep-tab closed something: %v", name)
		}
	}
	store, err := coreStore()
	if err != nil {
		t.Fatal(err)
	}
	kept, err := store.Tab(tab)
	if err != nil || kept.Ledger != "lg_fixture" || kept.Closed {
		t.Fatalf("tab registry: %+v %v", kept, err)
	}
	// Reading the kept tab resumes the same ledger instead of navigating again.
	result, data, err = runRecords(t, recordsOptions{Max: 1}, tab)
	if err != nil || result.Meta.Source != "tab" || result.Meta.Coverage.Records != 100 {
		t.Fatalf("tab reread: %v %s", err, data)
	}
	creates, navigates := 0, 0
	for _, name := range fakeBrowserRequestNames(server.Requests()) {
		switch name {
		case "browser.create":
			creates++
		case "browser.cdp/Page.navigate":
			navigates++
		}
	}
	if creates != 1 || navigates != 1 {
		t.Fatalf("tab reread created or navigated again: creates=%d navigates=%d", creates, navigates)
	}
}

func TestRecordsLiveAndTransportsHonorCooldown(t *testing.T) {
	recordsTestScope(t)
	gov, err := recordsGovernor()
	if err != nil {
		t.Fatal(err)
	}
	if err := gov.SetCooldown("reddit.com", time.Now().Add(5*time.Minute), "fixture"); err != nil {
		t.Fatal(err)
	}
	server := newRecordsBridge(t, recordsLiveHandler(t, "allow", nil, nil))
	_, _, err = runRecords(t, recordsOptions{}, recordsFixtureThreadURL)
	var problem *agentstate.Error
	if !errors.As(err, &problem) || problem.Code != "cooldown" || problem.Retry != "retryable" {
		t.Fatalf("cooldown refusal: %v", err)
	}
	if len(server.Requests()) != 0 {
		t.Fatalf("cooldown refusal must not contact the browser: %v", fakeBrowserRequestNames(server.Requests()))
	}
	command := &cobra.Command{Use: "fetch"}
	command.SetContext(context.Background())
	err = coreRunTransport(command, []fanoutVariant{{Index: 0, Method: "GET", URL: "https://old.reddit.com/r/test.json"}}, "https://old.reddit.com", nil, nil, 30*time.Second, false, "fetch")
	if !errors.As(err, &problem) || problem.Code != "cooldown" {
		t.Fatalf("transport cooldown refusal: %v", err)
	}
	selected, err := scope.Current()
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := readCoreScope(selected)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Cooldowns) != 1 || inventory.Cooldowns[0]["site"] != "reddit.com" || inventory.Cooldowns[0]["reason"] != "fixture" {
		t.Fatalf("scope cooldowns: %v", inventory.Cooldowns)
	}
}

// Explicit API work is not refused by a site classification. Validation still
// runs before browser access, and the shared server cooldown remains enforced.
func TestFetchDoesNotRefuseBySiteClassification(t *testing.T) {
	recordsTestScope(t)
	command := &cobra.Command{Use: "fetch"}
	command.SetContext(context.Background())
	for _, target := range []string{"https://x.com/i/api/graphql/abc/TweetDetail", "https://www.reddit.com/svc/shreddit/more-comments/r/a/t3_b"} {
		err := coreFetch(command, target, "invalid method", nil, "", false)
		var problem *agentstate.Error
		if !errors.As(err, &problem) || problem.Code != "invalid_argument" {
			t.Fatalf("explicit fetch unexpectedly blocked before request validation: %v", err)
		}
	}
}
