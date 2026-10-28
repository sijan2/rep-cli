package commands

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

func TestScriptDetectionUsesPathOrObservedType(t *testing.T) {
	for _, tc := range []struct {
		url, resource, contentType string
		want                       bool
	}{
		{"https://example.test/a.js?v=1", "", "", true},
		{"https://example.test/a.mjs#module", "", "", true},
		{"https://example.test/a.cjs?module=1", "", "", true},
		{"https://example.test/page?next=app.js", "", "text/html", false},
		{"https://example.test/app.mjs.backup", "", "", false},
		{"https://example.test/config", "script", "", true},
		{"https://example.test/code", "", "application/javascript", true},
	} {
		r := store.Request{URL: tc.url, ResourceType: tc.resource, Response: &store.Response{Headers: store.HeaderMap{"Content-Type": {tc.contentType}}}}
		if got := isJavaScript(&r); got != tc.want {
			t.Errorf("%s: script=%v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestScriptInventoryPreservesEvidenceAndCountsEveryObservedPage(t *testing.T) {
	body := []byte("export const greeting = 'hello';")
	r := store.Request{ID: "script-one", URL: "https://example.test/app.js", Domain: "example.test", PageURL: "https://example.test/one",
		Response: &store.Response{Status: 200, Body: base64.StdEncoding.EncodeToString(body)}, ResponseEncoding: "base64",
		ResponseBodyCapture: &store.BodyCapture{State: "complete", CapturedBytes: int64(len(body))},
	}
	second := r
	second.ID, second.PageURL = "script-two", "https://example.test/two"
	out := categorizeJS([]store.Request{r, second})
	if out.Summary.TotalScripts != 1 || out.Summary.UniquePages != 2 || out.Summary.ObservedRequests != 2 || out.Summary.DuplicateRequests != 1 {
		t.Fatalf("wrong script/page counts: %+v", out.Summary)
	}
	script := out.FirstPartyJS[0]
	if script.RequestID != r.ID || script.Size != len(body) || script.BodyCapture.State != "complete" || script.PayloadValidation != "validated" || script.BodyCapture.SHA256 == "" {
		t.Fatalf("lost script evidence: %+v", script)
	}
	r.Response.Body = "corrupt base64!"
	bad := categorizeJS([]store.Request{r}).FirstPartyJS[0]
	if bad.PayloadValidation != "invalid" || bad.BodyCapture.State == "complete" {
		t.Fatal("corrupt script evidence accepted")
	}
	r.Response.Body = base64.StdEncoding.EncodeToString(body)
	observed := int64(len(body) + 1)
	r.ResponseBodyCapture.ObservedBytes = &observed
	bad = categorizeJS([]store.Request{r}).FirstPartyJS[0]
	if bad.PayloadValidation != "invalid" || bad.BodyCapture.State == "complete" {
		t.Fatal("contradictory script capture metadata accepted")
	}
}

func TestEmptyJSAndReconReturnStructuredCoverage(t *testing.T) {
	summaryTestScope(t, "inventory", "empty")
	oldMode, oldSavedJS, oldSavedRecon := outputMode, jsSaved, reconSaved
	outputMode, jsSaved, reconSaved = "json", "", ""
	t.Cleanup(func() { outputMode, jsSaved, reconSaved = oldMode, oldSavedJS, oldSavedRecon })
	for _, tc := range []struct {
		name string
		run  func(*cobra.Command, []string) error
		args []string
	}{
		{"js", runJS, nil}, {"recon", runRecon, []string{"example.test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			cmd := &cobra.Command{Use: tc.name}
			cmd.SetOut(&out)
			if err := tc.run(cmd, tc.args); err != nil {
				t.Fatal(err)
			}
			var result struct {
				Provenance observedCaptureProvenance `json:"provenance"`
				Coverage   CaptureCoverageReport     `json:"coverage"`
			}
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Provenance.SourceStatus != "no_capture" || result.Coverage.Capture.State != "no_capture" {
				t.Fatalf("empty inventory claimed coverage: %s", out.String())
			}
		})
	}
}
