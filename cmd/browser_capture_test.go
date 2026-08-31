package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/store"
)

func TestBrowserCapturedRequestDescriptorsAreStableAndSecretFree(t *testing.T) {
	requests := []store.Request{
		{
			ID:      "h_first",
			Method:  "post",
			URL:     "https://example.test/download?token=top-secret",
			Headers: store.HeaderMap{"authorization": {"Bearer top-secret"}},
			Body:    "request-secret",
			Response: &store.Response{
				Status:  201,
				Headers: store.HeaderMap{"set-cookie": {"session=top-secret"}},
				Body:    "snowman: ☃",
			},
			ResponseBodyTruncated:   true,
			IntentionalCancellation: "top-secret",
		},
		{ID: "h_second", Method: "GET"},
	}

	got := browserCapturedRequestDescriptors(requests)
	want := []browserCapturedRequest{
		{Sequence: 1, ID: "h_first", Method: "POST", Status: 201, BodyBytes: len("snowman: ☃"), BodyTruncated: true},
		{Sequence: 2, ID: "h_second", Method: "GET", Status: 0, BodyBytes: 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("descriptors mismatch\n got: %#v\nwant: %#v", got, want)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"example.test", "top-secret", "request-secret", "snowman"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("descriptor leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestEnrichBrowserCaptureResultReportsCompletedDownloadBeforeLaterFormFailure(t *testing.T) {
	livePath := filepath.Join(t.TempDir(), "live.json")
	t.Setenv("REPLIVE_PATH", livePath)
	requests := []store.Request{
		{
			ID: "h_action_redirect", Method: "POST", URL: "https://app.example.test/verify?secret=source",
			Body: "token=top-secret", Timestamp: 1, StartOrdinal: 1, ResponseOrdinal: 2, CompletionOrdinal: 3,
			Response: &store.Response{Status: 200, Body: `{"type":"redirect","status":307,"location":"/download/start?signature=hidden"}`},
		},
		{
			ID: "h_duplicate_failure", Method: "POST", URL: "https://app.example.test/verify?secret=source",
			Body: "token=top-secret", Timestamp: 2, StartOrdinal: 4, ResponseOrdinal: 5, CompletionOrdinal: 6,
			Response: &store.Response{Status: 200, Body: `{"type":"failure","status":400,"data":"CAPTCHA_FAILED top-secret"}`},
		},
		{
			ID: "h_redirect", OriginalID: "download-request", Method: "GET", URL: "https://app.example.test/download/start?signature=hidden", Timestamp: 3,
			StartOrdinal: 7, ResponseOrdinal: 8, CompletionOrdinal: 9,
			Response: &store.Response{Status: 302, Headers: store.HeaderMap{"location": {"https://files.example.test/artifact.ipa?signed=hidden"}}},
		},
		{
			ID: "h_terminal", OriginalID: "download-request:redirect-1", Method: "GET", URL: "https://files.example.test/artifact.ipa?signed=hidden", Timestamp: 4,
			StartOrdinal: 10, ResponseOrdinal: 11, CompletionOrdinal: 12,
			Canceled: true, IntentionalCancellation: "browser-download",
			Response: &store.Response{Status: 200, Headers: store.HeaderMap{"content-disposition": {`attachment; filename="artifact.ipa"`}}},
		},
	}
	payload, err := json.Marshal(browserCaptureSnapshot{SessionID: "capture-chain", Requests: requests})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(livePath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	result := map[string]interface{}{
		"session_id":      "capture-chain",
		"requests":        float64(4),
		"failed_requests": float64(1),
	}
	if err := enrichBrowserCaptureResult(result); err != nil {
		t.Fatal(err)
	}

	outcome, ok := result["terminal_outcome"].(*browserTerminalOutcome)
	if !ok {
		t.Fatalf("missing terminal outcome: %#v", result["terminal_outcome"])
	}
	wantIDs := []string{"h_action_redirect", "h_redirect", "h_terminal"}
	if outcome.Kind != "download_handoff" || outcome.Completed || !outcome.TerminalResponseReceived || !outcome.DownloadHandoffStarted || outcome.TerminalStatus != 200 || outcome.RedirectHops != 2 || !reflect.DeepEqual(outcome.RequestIDs, wantIDs) {
		t.Fatalf("unexpected terminal outcome: %#v", outcome)
	}
	if outcome.LaterFormFailures != 1 || !reflect.DeepEqual(outcome.LaterFormFailureIDs, []string{"h_duplicate_failure"}) {
		t.Fatalf("later form failure was not separated: %#v", outcome)
	}
	if result["failed_requests"] != 0 || result["ignored_cancellations"] != 1 {
		t.Fatalf("intentional download cancellation was counted as failure: %#v", result)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"app.example.test", "files.example.test", "top-secret", "signature", "CAPTCHA_FAILED", "artifact.ipa"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("safe outcome leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestBrowserTerminalOutcomePreservesPOSTAcross307Redirect(t *testing.T) {
	requests := []store.Request{
		{
			ID: "h_source", Method: "POST", URL: "https://app.example.test/action",
			StartOrdinal: 1, ResponseOrdinal: 2, CompletionOrdinal: 3,
			Response: &store.Response{Status: 200, Body: `{"type":"redirect","status":307,"location":"/download"}`},
		},
		{
			ID: "h_first", OriginalID: "redirect-root", Method: "POST", URL: "https://app.example.test/download",
			StartOrdinal: 4, ResponseOrdinal: 5, CompletionOrdinal: 6,
			Response: &store.Response{Status: 307, Headers: store.HeaderMap{"location": {"https://files.example.test/final"}}},
		},
		{
			ID: "h_terminal", OriginalID: "redirect-root:redirect-1", Method: "POST", URL: "https://files.example.test/final",
			StartOrdinal: 7, ResponseOrdinal: 8, CompletionOrdinal: 9,
			Response: &store.Response{Status: 200},
		},
	}
	outcome := browserTerminalRedirectOutcome(requests)
	if outcome == nil || outcome.Kind != "redirect_chain" || !outcome.Completed || !outcome.TerminalResponseReceived || outcome.DownloadHandoffStarted || outcome.TerminalRequestID != "h_terminal" {
		t.Fatalf("307 POST redirect chain was not preserved: %#v", outcome)
	}
}

func TestBrowserTerminalOutcomeDoesNotCompleteFailedOrPending2xx(t *testing.T) {
	base := []store.Request{
		{
			ID: "h_source", Method: "POST", URL: "https://app.example.test/action",
			StartOrdinal: 1, ResponseOrdinal: 2, CompletionOrdinal: 3,
			Response: &store.Response{Status: 200, Body: `{"type":"redirect","status":307,"location":"/final"}`},
		},
		{
			ID: "h_terminal", Method: "GET", URL: "https://app.example.test/final",
			StartOrdinal: 4, ResponseOrdinal: 5, CompletionOrdinal: 6,
			ErrorText: "net::ERR_CONNECTION_RESET", Response: &store.Response{Status: 200},
		},
	}
	outcome := browserTerminalRedirectOutcome(base)
	if outcome == nil || outcome.Completed || !outcome.TerminalResponseReceived {
		t.Fatalf("failed 2xx was reported complete: %#v", outcome)
	}

	base[1].ErrorText = ""
	base[1].CompletionOrdinal = 0
	outcome = browserTerminalRedirectOutcome(base)
	if outcome == nil || outcome.Completed || !outcome.TerminalResponseReceived {
		t.Fatalf("pending 2xx was reported complete: %#v", outcome)
	}
}

func TestBrowserTerminalOutcomeRequiresRedirectRequestAfterSourceCompletion(t *testing.T) {
	requests := []store.Request{
		{
			ID: "h_source", Method: "POST", URL: "https://app.example.test/verify",
			StartOrdinal: 1, ResponseOrdinal: 3, CompletionOrdinal: 4,
			Response: &store.Response{Status: 200, Body: `{"type":"redirect","status":307,"location":"/download"}`},
		},
		{
			ID: "h_stale_same_url", Method: "GET", URL: "https://app.example.test/download",
			StartOrdinal: 2, ResponseOrdinal: 5, CompletionOrdinal: 6,
			Response: &store.Response{Status: 200},
		},
		{
			ID: "h_real_terminal", Method: "GET", URL: "https://app.example.test/download",
			StartOrdinal: 7, ResponseOrdinal: 8, CompletionOrdinal: 9,
			Response: &store.Response{Status: 200},
		},
	}
	outcome := browserTerminalRedirectOutcome(requests)
	if outcome == nil || outcome.TerminalRequestID != "h_real_terminal" || !reflect.DeepEqual(outcome.RequestIDs, []string{"h_source", "h_real_terminal"}) {
		t.Fatalf("redirect chain selected a pre-completion request: %#v", outcome)
	}

	requests[0].CompletionOrdinal = 0
	if outcome := browserTerminalRedirectOutcome(requests); outcome != nil {
		t.Fatalf("legacy capture without causal ordinals should fail closed: %#v", outcome)
	}
}

func TestEnrichBrowserCaptureResultIgnoresHeadersOnlyAbortButKeepsRealFailure(t *testing.T) {
	livePath := filepath.Join(t.TempDir(), "live.json")
	t.Setenv("REPLIVE_PATH", livePath)
	requests := []store.Request{
		{
			ID: "h_headers", Method: "GET", URL: "https://files.example.test/large?signed=hidden", Timestamp: 1,
			ErrorText: "net::ERR_ABORTED", Response: &store.Response{Status: 200},
		},
		{
			ID: "h_real_failure", Method: "GET", URL: "https://files.example.test/broken", Timestamp: 2,
			ErrorText: "net::ERR_CONNECTION_RESET", Response: &store.Response{Status: 0},
		},
	}
	payload, err := json.Marshal(browserCaptureSnapshot{SessionID: "capture-headers", Requests: requests})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(livePath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	result := map[string]interface{}{
		"session_id":      "capture-headers",
		"requests":        float64(2),
		"failed_requests": float64(2),
		"request":         map[string]interface{}{"url": requests[0].URL},
		"response":        map[string]interface{}{"body_omitted": true},
	}
	if err := enrichBrowserCaptureResult(result); err != nil {
		t.Fatal(err)
	}
	if result["failed_requests"] != 1 || result["ignored_cancellations"] != 1 {
		t.Fatalf("headers-only reconciliation mismatch: %#v", result)
	}
}

func TestReconcileBrowserFailureCountDoesNotDoubleSubtractExtensionCancellation(t *testing.T) {
	requests := []store.Request{
		{
			ID: "h_headers", Method: "GET", URL: "https://files.example.test/large", Timestamp: 1,
			Canceled: true, IntentionalCancellation: "headers-only", Response: &store.Response{Status: 200},
		},
		{
			ID: "h_real_failure", Method: "GET", URL: "https://files.example.test/broken", Timestamp: 2,
			ErrorText: "net::ERR_CONNECTION_RESET", Response: &store.Response{Status: 0},
		},
	}
	result := map[string]interface{}{
		"failed_requests":       float64(1),
		"ignored_cancellations": float64(1),
	}
	reconcileBrowserFailureCount(result, requests)
	if result["failed_requests"] != float64(1) || result["ignored_cancellations"] != float64(1) {
		t.Fatalf("extension cancellation was double-subtracted: %#v", result)
	}
}

func TestReconcileBrowserFailureCountDoesNotInferDownloadFromFileExtension(t *testing.T) {
	requests := []store.Request{{
		ID: "h_archive", Method: "GET", URL: "https://files.example.test/archive.ipa",
		ErrorText: "net::ERR_ABORTED", Response: &store.Response{Status: 200},
	}}
	result := map[string]interface{}{"failed_requests": float64(1)}
	reconcileBrowserFailureCount(result, requests)
	if result["failed_requests"] != float64(1) {
		t.Fatalf("unmarked .ipa abort was incorrectly ignored: %#v", result)
	}
}

func TestReconcileBrowserFailureCountCapsLegacyHeadersOnlyFallback(t *testing.T) {
	url := "https://files.example.test/large"
	requests := []store.Request{
		{ID: "h_first", Method: "GET", URL: url, ErrorText: "net::ERR_ABORTED", Response: &store.Response{Status: 200}},
		{ID: "h_poll", Method: "GET", URL: url, ErrorText: "net::ERR_ABORTED", Response: &store.Response{Status: 200}},
	}
	result := map[string]interface{}{
		"failed_requests": float64(2),
		"request":         map[string]interface{}{"url": url},
		"response":        map[string]interface{}{"body_omitted": true},
	}
	reconcileBrowserFailureCount(result, requests)
	if result["failed_requests"] != 1 || result["ignored_cancellations"] != 1 {
		t.Fatalf("legacy headers-only fallback should ignore one request: %#v", result)
	}
}

func TestEnrichBrowserCaptureResultReadsMatchingSealedCapture(t *testing.T) {
	livePath := filepath.Join(t.TempDir(), "live.json")
	t.Setenv("REPLIVE_PATH", livePath)
	payload := `{
  "version": "2.0",
  "session_id": "capture-1",
  "requests": [
    {"id":"h_one","method":"GET","url":"https://example.test/signed?secret=yes","timestamp":1,"response":{"status":200,"body":"abc"}},
    {"id":"h_two","method":"POST","url":"https://example.test/action","timestamp":2,"response":{"status":204}}
  ]
}`
	if err := os.WriteFile(livePath, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	result := map[string]interface{}{"session_id": "capture-1", "requests": float64(2)}
	if err := enrichBrowserCaptureResult(result); err != nil {
		t.Fatal(err)
	}
	descriptors, ok := result["captured_requests"].([]browserCapturedRequest)
	if !ok || len(descriptors) != 2 {
		t.Fatalf("missing captured request descriptors: %#v", result["captured_requests"])
	}
	if descriptors[0].ID != "h_one" || descriptors[0].Status != 200 || descriptors[0].BodyBytes != 3 {
		t.Fatalf("unexpected first descriptor: %#v", descriptors[0])
	}
}

func TestEnrichBrowserCaptureResultRejectsStaleCapture(t *testing.T) {
	livePath := filepath.Join(t.TempDir(), "live.json")
	t.Setenv("REPLIVE_PATH", livePath)
	if err := os.WriteFile(livePath, []byte(`{"session_id":"old","requests":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	err := enrichBrowserCaptureResult(map[string]interface{}{"session_id": "new", "requests": float64(0)})
	if err == nil || !strings.Contains(err.Error(), "session mismatch") {
		t.Fatalf("expected safe session mismatch, got %v", err)
	}
}
