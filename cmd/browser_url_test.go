package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadBrowserURLArgumentFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signed-url")
	if err := os.WriteFile(path, []byte("  https://example.test/file?signature=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readBrowserURLArgument("@" + path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.test/file?signature=secret" {
		t.Fatalf("URL = %q", got)
	}
	if !browserURLArgumentIsSensitive("  @" + path) {
		t.Fatal("@file URL should be sensitive by default")
	}
	if browserURLArgumentIsSensitive(got) {
		t.Fatal("direct URL operands must remain backward compatible")
	}
	if _, err := readBrowserURLArgument("   "); err == nil {
		t.Fatal("expected empty URL error")
	}
}

func TestRedactSensitiveBrowserResultKeepsOnlySafeFetchMetadata(t *testing.T) {
	result := map[string]interface{}{
		"session_id":    "fetch-safe",
		"future_url":    "https://future.example.test/?token=top-secret",
		"requested_url": "https://files.example.test/archive.ipa?signature=top-secret",
		"final_url":     "https://cdn.example.test/archive.ipa?token=top-secret",
		"navigation":    map[string]interface{}{"errorText": "redirected through top-secret"},
		"request": map[string]interface{}{
			"method":        "GET",
			"url":           "https://files.example.test/archive.ipa?signature=top-secret",
			"future_header": "top-secret",
		},
		"response": map[string]interface{}{
			"ok":          true,
			"status":      float64(200),
			"status_text": "top-secret",
			"url":         "https://cdn.example.test/archive.ipa?token=top-secret",
			"headers":     map[string]interface{}{"set-cookie": "secret=value"},
			"body":        "private-response",
			"body_bytes":  float64(1234),
		},
		"captured_requests": []browserCapturedRequest{
			{Sequence: 1, ID: "h_0123456789abcdef", Method: "GET", Status: 200},
			{Sequence: 2, ID: "https://files.example.test/?token=top-secret", Method: "GET-TOP-SECRET", Status: 200},
		},
		"terminal_outcome": &browserTerminalOutcome{
			Kind: "redirect_chain", SourceRequestID: "top-secret", TerminalRequestID: "h_0123456789abcdef",
		},
	}
	redactSensitiveBrowserResult(result, "fetch")

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"files.example.test", "cdn.example.test", "top-secret", "set-cookie", "private-response", "archive.ipa"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("redacted result leaked %q: %s", forbidden, encoded)
		}
	}
	response := result["response"].(map[string]interface{})
	if response["status"] != float64(200) || response["body_bytes"] != float64(1234) || response["url_redacted"] != true || response["headers_omitted"] != true || response["body_omitted"] != true {
		t.Fatalf("safe response metadata mismatch: %#v", response)
	}
	if result["sensitive_output_redacted"] != true {
		t.Fatalf("missing redaction marker: %#v", result)
	}
	requests := result["captured_requests"].([]browserCapturedRequest)
	if len(requests) != 1 || requests[0].ID != "h_0123456789abcdef" {
		t.Fatalf("unsafe capture handles were not removed: %#v", requests)
	}
	if _, ok := result["terminal_outcome"]; ok {
		t.Fatalf("unsafe terminal outcome was retained: %#v", result["terminal_outcome"])
	}
}
