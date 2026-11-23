package commands

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

type recordsTestResult struct {
	Status   string `json:"status"`
	Complete bool   `json:"complete"`
	Error    *struct {
		Code string `json:"code"`
	} `json:"error"`
	Meta struct {
		Source   string `json:"source"`
		Site     string `json:"site"`
		Coverage struct {
			Records  int    `json:"records"`
			Expected int    `json:"expected"`
			Pending  int    `json:"pending"`
			Code     string `json:"code"`
		} `json:"coverage"`
		Frontier struct {
			Total int              `json:"total"`
			Items []map[string]any `json:"items"`
		} `json:"frontier"`
		ModelRequests int            `json:"model_requests"`
		Truncated     bool           `json:"truncated"`
		Artifact      map[string]any `json:"artifact"`
	} `json:"meta"`
	Untrusted struct {
		Thread  *recordsInline  `json:"thread"`
		Records []recordsInline `json:"records"`
	} `json:"untrusted"`
}

func recordsTestScope(t *testing.T) scope.Scope {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("REP_PROFILE", "default")
	selected, err := scope.Configure(scope.Options{Workspace: "records-fixture", Task: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scope.Reset)
	return selected
}

func runRecords(t *testing.T, options recordsOptions, args ...string) (recordsTestResult, []byte, error) {
	t.Helper()
	command := &cobra.Command{Use: "records"}
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetContext(context.Background())
	if options.Max == 0 {
		options.Max = 50
	}
	if options.Timeout == 0 {
		options.Timeout = time.Minute
	}
	if options.Site == "" {
		options.Site = "auto"
	}
	err := coreRecords(command, args, options)
	var result recordsTestResult
	if stdout.Len() > 0 {
		if e := json.Unmarshal(stdout.Bytes(), &result); e != nil {
			t.Fatalf("records output is not one JSON object: %v\n%s", e, stdout.String())
		}
	}
	return result, stdout.Bytes(), err
}

func recordsFixturePath(name string) string { return filepath.Join("..", "records", "testdata", name) }

func TestRecordsFileModeDecodesFixtures(t *testing.T) {
	recordsTestScope(t)
	for _, tc := range []struct {
		name, url                 string
		count, expected, frontier int
	}{
		{"reddit_thread_ssr.html", "", 100, 207, 56},
		{"reddit_thread_ssr_new.html", "", 100, 207, 38},
		{"x_tweetdetail.json", "https://x.com/i/status/1900000000000000003", 36, 635, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, data, err := runRecords(t, recordsOptions{URL: tc.url}, recordsFixturePath(tc.name))
			if err != nil {
				t.Fatal(err)
			}
			if len(data) > recordsInlineBudget || bytes.Count(data, []byte("\n")) != 1 {
				t.Fatalf("inline result must be one bounded line: %d bytes", len(data))
			}
			c := result.Meta.Coverage
			if result.Status != "partial" || result.Complete || c.Records != tc.count || c.Expected != tc.expected || result.Meta.Frontier.Total != tc.frontier || c.Code != "frontier_pending" || result.Meta.ModelRequests != 0 || !result.Meta.Truncated {
				t.Fatalf("fixture result: %s", data)
			}
			for _, r := range result.Untrusted.Records {
				if len(r.Text) > 200 || !utf8.ValidString(r.Text) {
					t.Fatal("text byte cap")
				}
			}
			if len(result.Meta.Frontier.Items) > 20 || len(result.Untrusted.Records) > 50 {
				t.Fatal("inline limits")
			}
			if result.Meta.Source != "file" || result.Meta.Artifact["dir"] == nil {
				t.Fatalf("artifact missing: %v", result.Meta.Artifact)
			}
		})
	}
}

func TestRecordsFileMergeDoesNotResolveFrontier(t *testing.T) {
	recordsTestScope(t)
	result, _, err := runRecords(t, recordsOptions{Max: 1}, recordsFixturePath("reddit_thread_ssr.html"), recordsFixturePath("reddit_more_comments.synthetic.html"))
	if err != nil {
		t.Fatal(err)
	}
	c := result.Meta.Coverage
	// Files carry no request descriptor, so the fragment adds records but can
	// never prove which loader it answered.
	if c.Records != 104 || result.Meta.Frontier.Total != 58 || c.Pending != 58 || len(result.Untrusted.Records) != 1 {
		t.Fatalf("file merge: %+v", result.Meta)
	}
}

func TestRecordsArtifactHoldsEveryRecordAndIsPruned(t *testing.T) {
	selected := recordsTestScope(t)
	root := filepath.Join(selected.DataDir, "records")
	stale := filepath.Join(root, "rd_stale")
	if err := os.MkdirAll(stale, 0700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-recordsRetention - time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	result, _, err := runRecords(t, recordsOptions{Max: 3}, recordsFixturePath("reddit_thread_ssr.html"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired records artifact survived")
	}
	dir := result.Meta.Artifact["dir"].(string)
	lines, err := os.ReadFile(filepath.Join(dir, "records.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(lines, []byte("\n")); n != 100 {
		t.Fatalf("artifact holds %d records, want 100", n)
	}
	info, err := os.Stat(filepath.Join(dir, "manifest.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("manifest permissions: %v %v", err, info)
	}
	if len(result.Untrusted.Records) != 3 {
		t.Fatal("inline preview must honor --max")
	}
}

func TestRecordsPageStringsStayUntrusted(t *testing.T) {
	recordsTestScope(t)
	path := filepath.Join(t.TempDir(), "body.html")
	body := `<shreddit-post id=t3_rootprobe post-title="page-title-probe" author="page-author-probe" comment-count=1></shreddit-post><shreddit-comment thingId=t1_replyprobe postId=t3_rootprobe author="page-author-probe" depth=0><div slot=comment>page-text-probe</div><a slot=more-comments-permalink href="/page-url-probe"><faceplate-tracker noun=continue_thread>page-label-probe</faceplate-tracker></a></shreddit-comment>`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	_, data, err := runRecords(t, recordsOptions{}, path)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	untrusted := result["untrusted"]
	delete(result, "untrusted")
	trusted, _ := json.Marshal(result)
	page, _ := json.Marshal(untrusted)
	for _, value := range []string{"t3_rootprobe", "t1_replyprobe", "page-title-probe", "page-author-probe", "page-text-probe", "page-label-probe"} {
		if bytes.Contains(trusted, []byte(value)) || !bytes.Contains(page, []byte(value)) {
			t.Fatalf("page string boundary: %s", value)
		}
	}
}

func TestRecordsArgumentErrors(t *testing.T) {
	recordsTestScope(t)
	path := filepath.Join(t.TempDir(), "unsupported.json")
	if err := os.WriteFile(path, []byte(`{"unknown":"page-error-probe"}`), 0600); err != nil {
		t.Fatal(err)
	}
	fixture := recordsFixturePath("reddit_thread_ssr.html")
	cases := []struct {
		options recordsOptions
		args    []string
		code    string
	}{
		{recordsOptions{}, []string{path}, "unsupported_site"},
		{recordsOptions{Site: "unknown"}, []string{path}, "invalid_argument"},
		{recordsOptions{}, []string{path + "-missing"}, "input_unreadable"},
		{recordsOptions{Site: "x"}, []string{fixture}, "unsupported_site"},
		{recordsOptions{Max: -1}, []string{fixture}, "invalid_argument"},
		{recordsOptions{Timeout: time.Second}, []string{fixture}, "invalid_argument"},
		{recordsOptions{KeepTab: true}, []string{fixture}, "invalid_argument"},
		{recordsOptions{}, []string{fixture, "h_00000000000000000000000000000000"}, "invalid_argument"},
		{recordsOptions{}, []string{"s_00000000000000000000000000000000", "s_11111111111111111111111111111111"}, "invalid_argument"},
		{recordsOptions{}, []string{fixture, recordsFixturePath("x_tweetdetail.json")}, "unsupported_site"},
	}
	for _, tc := range cases {
		_, data, err := runRecords(t, tc.options, tc.args...)
		var problem *agentstate.Error
		if !errors.As(err, &problem) || problem.Code != tc.code || bytes.Contains(data, []byte("page-error-probe")) {
			t.Fatalf("%v %v: got %v", tc.options, tc.args, err)
		}
	}
}

func recordsCaptureRequest(id, method, requestURL, requestBody string, body []byte, ordinal int64) store.Request {
	return store.Request{ID: id, Method: method, URL: requestURL, Body: requestBody, ResourceType: "Document", StartOrdinal: ordinal, Timestamp: time.Now().UnixMilli(), ResponseEncoding: "base64",
		Response:            &store.Response{Status: 200, Headers: store.HeaderMap{"content-type": {"text/html"}}, Body: base64.StdEncoding.EncodeToString(body)},
		ResponseBodyCapture: &store.BodyCapture{State: "complete", CapturedBytes: int64(len(body))}}
}

func TestRecordsCaptureModeBindsFragmentsByRequest(t *testing.T) {
	base, err := os.ReadFile(recordsFixturePath("reddit_thread_ssr.html"))
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := os.ReadFile(recordsFixturePath("reddit_more_comments.synthetic.html"))
	if err != nil {
		t.Fatal(err)
	}
	unrelated := recordsCaptureRequest("old-0", "GET", "https://www.reddit.com/r/programming/comments/p000001/title/", "", []byte("not a thread"), 1)
	primary := recordsCaptureRequest("old-1", "GET", "https://www.reddit.com/r/programming/comments/p000001/title/", "", base, 2)
	more := recordsCaptureRequest("old-2", "POST", "https://www.reddit.com/svc/shreddit/more-comments/r/programming/t3_p000001", "cursor=cursor_17", fragment, 3)
	capture, snapshot := netFixture(t, []store.Request{unrelated, primary, more})
	ids := map[string]string{}
	for _, request := range snapshot.Requests {
		ids[request.OriginalID] = request.ID
	}
	// The capture handle selects the primary response and binds the fragment
	// to the loader whose path and cursor its request named.
	result, _, err := runRecords(t, recordsOptions{Max: 0}, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := result.Meta.Coverage
	if result.Meta.Source != "capture" || c.Records != 104 || c.Pending != 57 || result.Meta.Frontier.Total != 58 {
		t.Fatalf("capture mode: %+v", result.Meta)
	}
	resolved := 0
	for _, item := range result.Meta.Frontier.Items {
		if item["state"] == "resolved" || item["state"] == "resolved_short" {
			resolved++
		}
	}
	if resolved != 1 {
		t.Fatalf("expected exactly one resolved loader in the preview, got %d", resolved)
	}
	// Explicit handles decide the merge order themselves.
	result, _, err = runRecords(t, recordsOptions{Max: 0}, ids["old-1"])
	if err != nil || result.Meta.Coverage.Records != 100 || result.Meta.Coverage.Pending != 56 {
		t.Fatalf("single handle: %v %+v", err, result.Meta.Coverage)
	}
	result, _, err = runRecords(t, recordsOptions{Max: 0}, ids["old-1"], ids["old-2"])
	if err != nil || result.Meta.Coverage.Records != 104 || result.Meta.Coverage.Pending != 57 {
		t.Fatalf("handle merge: %v %+v", err, result.Meta.Coverage)
	}
	// A partial body is evidence of nothing.
	primary.ResponseBodyCapture.State = "partial"
	if _, err := recordsCapturedInput(&primary); err == nil {
		t.Fatal("accepted a partial captured body")
	}
	_, _, err = runRecords(t, recordsOptions{}, "s_00000000000000000000000000000000")
	var problem *agentstate.Error
	if !errors.As(err, &problem) {
		t.Fatalf("unknown capture must fail closed: %v", err)
	}
}

func TestRecordsLiveTargetsFailClosedWithoutABridge(t *testing.T) {
	recordsTestScope(t)
	// A tab that rep did not create is refused before any browser work.
	store, err := coreStore()
	if err != nil {
		t.Fatal(err)
	}
	selected, _ := scope.Current()
	adopted, err := store.Register(agentstate.Browser{Name: "dia", Profile: "p", Epoch: "e"}, 5, selected.Identity(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runRecords(t, recordsOptions{}, adopted.Handle)
	var problem *agentstate.Error
	if !errors.As(err, &problem) || problem.Code != "user_tab" {
		t.Fatalf("adopted user tab: %v", err)
	}
	// Policy sites without a decoder, and hosts without a thread policy, are
	// refused with the page read as the alternative.
	for _, target := range []string{"https://news.ycombinator.com/item?id=1", "https://example.test/thread/1", "https://www.reddit.com/r/programming/"} {
		_, _, err := runRecords(t, recordsOptions{}, target)
		if !errors.As(err, &problem) || problem.Code != "unsupported_site" || !strings.Contains(problem.Next, "rep page") {
			t.Fatalf("live target %s: %v", target, err)
		}
	}
	// A thread URL with no live bridge fails as unavailable, never by reading the DOM.
	_, _, err = runRecords(t, recordsOptions{}, "https://www.reddit.com/r/programming/comments/abc/title/")
	if !errors.As(err, &problem) || problem.Code != "browser_unavailable" {
		t.Fatalf("no bridge: %v", err)
	}
}

func TestRecordsHelpAndSurface(t *testing.T) {
	command, _, err := rootCmd.Find([]string{"records"})
	if err != nil || command.Hidden {
		t.Fatal("records must be a visible verb")
	}
	if help := commandContract(command); !strings.Contains(help, "--site") || !strings.Contains(help, "rep records t1") {
		t.Fatalf("records help: %d bytes\n%s", len(help), help)
	}
}

func TestRecordsUsageErrorsThroughEntryPoint(t *testing.T) {
	for _, args := range [][]string{{"records"}, {"records", "x.html", "--max-bytes", "1"}} {
		stdout, stderr, code := runEntryPoint(t, args...)
		if code != 2 || stderr != "" || !strings.Contains(stdout, `"code":"invalid_argument"`) {
			t.Fatalf("usage error contract for %v: %d %q %q", args, code, stdout, stderr)
		}
	}
}
