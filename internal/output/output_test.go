package output

import (
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/store"
)

func TestFormatEmptyReason_PrimaryMismatch(t *testing.T) {
	ctx := EmptyResultContext{
		Command:               "list",
		Source:                "live.json",
		TotalCandidates:       246,
		DistinctDomains:       7,
		Filters:               store.FilterOptions{PrimaryOnly: true},
		PrimaryCount:          23,
		PrimariesInCandidates: 0,
		SampleDomains:         []string{"x.com"},
	}
	out := FormatEmptyReason(ctx)

	want := []string{
		"source: live.json (246 requests, 7 domains)",
		"primary=23 (0 match candidates)",
		"result: 0 requests matched",
		"rep list --primary=false",
		"rep primary --clear && rep primary x.com",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("expected output to contain %q, got:\n%s", w, out)
		}
	}
}

func TestFormatEmptyReason_DomainFilter(t *testing.T) {
	ctx := EmptyResultContext{
		Source:          "live.json",
		TotalCandidates: 10,
		Filters:         store.FilterOptions{Domain: "nope.example.com"},
	}
	out := FormatEmptyReason(ctx)
	if !strings.Contains(out, `domain=nope.example.com`) {
		t.Errorf("missing domain in filters summary: %s", out)
	}
	if !strings.Contains(out, "rep list --primary=false") {
		t.Errorf("missing domain-fallback suggestion: %s", out)
	}
}

func TestFormatEmptyReason_NoCandidates(t *testing.T) {
	ctx := EmptyResultContext{
		Source:          "live.json",
		TotalCandidates: 0,
	}
	out := FormatEmptyReason(ctx)
	for _, w := range []string{"rep summary", "enable auto-export"} {
		if !strings.Contains(out, w) {
			t.Errorf("expected suggestion %q, got:\n%s", w, out)
		}
	}
}

func TestFormatPaginationFooter(t *testing.T) {
	cases := []struct {
		name            string
		returned, total int
		offset, limit   int
		wantEmpty       bool
		wantSubstrings  []string
	}{
		{
			name:     "more available",
			returned: 50, total: 200, offset: 0, limit: 50,
			wantSubstrings: []string{"Showing 1-50 of 200", "--offset=50", "--limit=50"},
		},
		{
			name:     "next page",
			returned: 50, total: 200, offset: 50, limit: 50,
			wantSubstrings: []string{"Showing 51-100 of 200", "--offset=100", "--limit=50"},
		},
		{
			name:     "no limit set",
			returned: 42, total: 42, limit: 0,
			wantSubstrings: []string{"Showing 42 requests"},
		},
		{
			name:     "no results",
			returned: 0, total: 0, limit: 0,
			wantEmpty: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := FormatPaginationFooter(tc.returned, tc.total, tc.offset, tc.limit)
			if tc.wantEmpty {
				if out != "" {
					t.Errorf("expected empty, got %q", out)
				}
				return
			}
			for _, s := range tc.wantSubstrings {
				if !strings.Contains(out, s) {
					t.Errorf("missing %q in %q", s, out)
				}
			}
		})
	}
}

func TestTruncateBodyInfo_NoTruncation(t *testing.T) {
	cfg := store.TruncateConfig{MaxBodySize: 500, ShowFullSize: true}
	body, info := TruncateBodyInfo("short body", "text/plain", cfg)
	if body != "short body" {
		t.Errorf("body modified unexpectedly: %q", body)
	}
	if info.Reason != "none" {
		t.Errorf("want reason=none, got %q", info.Reason)
	}
	if info.Total != len("short body") {
		t.Errorf("total wrong: %d", info.Total)
	}
}

func TestTruncateBodyInfo_SizeCap(t *testing.T) {
	cfg := store.TruncateConfig{MaxBodySize: 10, ShowFullSize: true}
	long := strings.Repeat("x", 1000)
	body, info := TruncateBodyInfo(long, "text/plain", cfg)
	if info.Reason != "size-cap" {
		t.Errorf("want size-cap, got %q", info.Reason)
	}
	if info.Total != 1000 {
		t.Errorf("want total=1000, got %d", info.Total)
	}
	if !strings.Contains(body, "truncated") {
		t.Errorf("expected truncation marker in body")
	}
}

func TestTruncateBodyInfo_BinaryLabel(t *testing.T) {
	cfg := store.TruncateConfig{MaxBodySize: 500, BinaryAsLabel: true}
	body, info := TruncateBodyInfo("\x89PNG\r\n\x1a\n...", "image/png", cfg)
	if info.Reason != "binary-label" {
		t.Errorf("want binary-label, got %q", info.Reason)
	}
	if !strings.HasPrefix(body, "[BINARY:") {
		t.Errorf("expected BINARY label, got %q", body)
	}
}

func TestFormatRequest_MetaOmitsBody(t *testing.T) {
	req := &store.Request{
		ID:                    "h_test",
		Method:                "POST",
		URL:                   "https://example.com/api",
		Body:                  "secret=abc&token=xyz",
		CaptureSource:         "cdp",
		TabID:                 42,
		ErrorText:             "net::ERR_ABORTED",
		ResponseBodyError:     "body unavailable",
		ResponseBodyTruncated: true,
		Timestamp:             1234,
		Response: &store.Response{
			Status: 200,
			Body:   "response-body-content",
		},
	}
	out := FormatRequest(req, store.OutputMeta)
	if out.Body != "" {
		t.Errorf("meta mode leaked request body: %q", out.Body)
	}
	if out.Response == nil {
		t.Fatalf("response dropped unexpectedly")
	}
	if out.Response.Body != "" {
		t.Errorf("meta mode leaked response body: %q", out.Response.Body)
	}
	// Headers remain.
	if out.Response.Status != 200 {
		t.Errorf("status dropped in meta mode")
	}
	if out.CaptureSource != "cdp" || out.TabID != 42 || out.Timestamp != 1234 {
		t.Errorf("capture provenance dropped: %+v", out)
	}
	if out.ErrorText == "" || out.ResponseBodyError == "" || !out.ResponseBodyTruncated {
		t.Errorf("capture diagnostics dropped: %+v", out)
	}
}

func TestFormatRequest_MetaRedactsSecretsAndDropsVerboseHeaders(t *testing.T) {
	req := &store.Request{
		Headers: store.HeaderMap{
			"Cookie":                 {"session=super-secret"},
			"Authorization":          {"Bearer token-value"},
			"Content-Type":           {"application/json"},
			"Sec-CH-UA-Full-Version": {"very verbose"},
		},
		Response: &store.Response{Headers: store.HeaderMap{
			"Set-Cookie":              {"session=replacement"},
			"Content-Type":            {"application/json"},
			"Content-Security-Policy": {"very large policy"},
		}},
	}
	out := FormatRequest(req, store.OutputMeta)
	if got := out.Headers["Cookie"][0]; !strings.Contains(got, "[REDACTED") || strings.Contains(got, "super-secret") {
		t.Fatalf("cookie was not redacted: %q", got)
	}
	if _, exists := out.Headers["Sec-CH-UA-Full-Version"]; exists {
		t.Fatal("verbose client hint should not be in meta output")
	}
	if got := out.Response.Headers["Set-Cookie"][0]; !strings.Contains(got, "[REDACTED") {
		t.Fatalf("set-cookie was not redacted: %q", got)
	}
	if _, exists := out.Response.Headers["Content-Security-Policy"]; exists {
		t.Fatal("CSP should not be in meta output")
	}
}

func TestFormatRequest_FullKeepsBody(t *testing.T) {
	req := &store.Request{
		ID:   "h_test",
		Body: "req-body",
		Response: &store.Response{
			Status: 200,
			Body:   "resp-body",
		},
	}
	out := FormatRequest(req, store.OutputFull)
	if out.Body != "req-body" {
		t.Errorf("full mode lost request body: %q", out.Body)
	}
	if out.Response.Body != "resp-body" {
		t.Errorf("full mode lost response body: %q", out.Response.Body)
	}
}

func TestFormatSourceLine(t *testing.T) {
	if got := FormatSourceLine("live.json"); got != "source: live.json" {
		t.Errorf("got %q", got)
	}
	if got := FormatSourceLine(""); got != "source: live.json" {
		t.Errorf("empty should default to live.json, got %q", got)
	}
	if got := FormatSourceLine("saved/abc123"); got != "source: saved/abc123" {
		t.Errorf("got %q", got)
	}
}
