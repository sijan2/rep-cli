package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

func TestCookiesExportRequiresExplicitPathBeforeBrowser(t *testing.T) {
	for _, path := range []string{"", "  "} {
		err := coreCookies(nil, "export", []string{"t1", path}, "", "", "", cookieOptions{})
		if err == nil || !strings.Contains(err.Error(), "new file path") {
			t.Fatalf("empty raw export path accepted: %v", err)
		}
	}
}

func TestCookieSourceSupportsLargeJarsAndBoundsInput(t *testing.T) {
	command := &cobra.Command{}
	large := `[{"name":"sid","value":"` + strings.Repeat("x", 300*1024) + `"}]`
	command.SetIn(strings.NewReader(large))
	got, err := cookieSource(command, "@-")
	if err != nil || got != large {
		t.Fatalf("large cookie jar rejected: %v", err)
	}
	command.SetIn(bytes.NewReader(make([]byte, 8*1024*1024+1)))
	if _, err := cookieSource(command, "@-"); err == nil || !strings.Contains(err.Error(), "8 MiB") {
		t.Fatalf("unbounded cookie import: %v", err)
	}
}

func TestCookieListingSurvivesOutputRedaction(t *testing.T) {
	_, err := scope.Configure(scope.Options{Workspace: "cookie-output", Task: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scope.Reset)
	raw := []map[string]any{{"name": "sid", "value": "synthetic-session-value", "session": true, "httpOnly": true, "domain": "example.test", "path": "/"}}
	command := &cobra.Command{}
	var buffer bytes.Buffer
	command.SetContext(context.Background())
	command.SetOut(&buffer)
	if err := emitCore(command, coreResult{Untrusted: map[string]any{"items": cookieListItems(raw)}}); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Untrusted struct {
			Items []map[string]any `json:"items"`
		} `json:"untrusted"`
	}
	if err := json.Unmarshal(buffer.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Untrusted.Items) != 1 {
		t.Fatalf("cookie metadata hidden: %s", buffer.String())
	}
	item := result.Untrusted.Items[0]
	if item["sessionOnly"] != true || item["httpOnly"] != true || item["name"] != "sid" || item["domain"] != "example.test" {
		t.Fatalf("cookie metadata hidden: %s", buffer.String())
	}
	if value, ok := item["value"].(string); !ok || !strings.HasPrefix(value, "[REDACTED sha256:") || strings.Contains(buffer.String(), "synthetic-session-value") {
		t.Fatalf("cookie value did not have a usable fingerprint: %s", buffer.String())
	}
	if raw[0]["session"] != true || raw[0]["value"] != "synthetic-session-value" {
		t.Fatalf("listing changed raw export source: %#v", raw)
	}
}

func TestCookiesRefusesSiblingBeforeResolvingBrowser(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	owner, err := scope.Configure(scope.Options{Workspace: "cookie-fixture", Task: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scope.Reset)
	index, err := coreStore()
	if err != nil {
		t.Fatal(err)
	}
	tab, err := index.Register(agentstate.Browser{Name: "headless", Profile: "synthetic-profile", Epoch: "synthetic-epoch"}, 1, owner.Identity(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := scope.Configure(scope.Options{Workspace: "cookie-fixture", Task: "sibling"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := index.Read()
	if err != nil {
		t.Fatal(err)
	}
	// nil command makes any attempt to establish a browser context fail the test.
	err = coreCookies(nil, "delete", []string{tab.Handle, "sid"}, "", "", "", cookieOptions{})
	problem, ok := err.(*agentstate.Error)
	if !ok || problem.Code != "owned_by_other_task" {
		t.Fatalf("wrong ownership result: %v", err)
	}
	after, err := index.Read()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed cookie operation changed registry: %v", err)
	}
	remembered, err := index.Remembered(sibling.Identity())
	if err != nil || remembered != nil {
		t.Fatalf("failed cookie operation acquired profile holder: %+v %v", remembered, err)
	}
}

func TestCookieImportRoundTripAttributes(t *testing.T) {
	source := `{"cookies":[{"name":"sid","value":"session-value","domain":"app.example.test","path":"/app","expires":-1,"size":16,"httpOnly":true,"secure":true,"session":true,"sameSite":"Lax","priority":"High","sourceScheme":"Secure","sourcePort":443,"partitionKey":{"topLevelSite":"https://example.test","hasCrossSiteAncestor":false}},{"name":"domain","value":"value","domain":".example.test","path":"/","expires":1980000000,"httpOnly":false,"secure":true,"sameSite":"None"}]}`
	got, err := cookieImportParams(source, "https://app.example.test/app")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := got[0]["domain"]; exists {
		t.Fatal("host-only cookie was broadened to domain cookie")
	}
	if _, exists := got[0]["expires"]; exists {
		t.Fatal("session cookie became persistent")
	}
	if _, exists := got[0]["size"]; exists {
		t.Fatal("read-only CDP fields were imported")
	}
	if got[0]["url"] != "https://app.example.test:443/app" || got[0]["value"] != "session-value" || got[0]["httpOnly"] != true || got[0]["secure"] != true || got[0]["path"] != "/app" {
		t.Fatalf("session changed: %#v", got[0])
	}
	wantPartition := map[string]any{"topLevelSite": "https://example.test", "hasCrossSiteAncestor": false}
	if !reflect.DeepEqual(got[0]["partitionKey"], wantPartition) {
		t.Fatalf("partition changed: %#v", got[0])
	}
	if got[1]["domain"] != ".example.test" || got[1]["expires"] != float64(1980000000) {
		t.Fatalf("persistent cookie changed: %#v", got[1])
	}
}

func TestCookieImportCommonExportFormats(t *testing.T) {
	for _, tc := range []struct {
		extra  string
		domain any
		expiry any
		site   any
	}{
		{`"domain":"example.test","hostOnly":false,"expirationDate":1980000000,"sameSite":"no_restriction"`, ".example.test", float64(1980000000), "None"},
		{`"domain":".example.test","hostOnly":true,"expires":-1,"sameSite":"unspecified"`, nil, nil, nil},
		{`"domain":"example.test","expires":0,"sameSite":"strict"`, nil, float64(0), "Strict"},
		{`"domain":"example.test","expires":1980000000,"session":true,"sameSite":"lax"`, nil, nil, "Lax"},
	} {
		got, err := cookieImportParams(`[{"name":"sid","value":"",`+tc.extra+`}]`, "https://example.test/")
		if err != nil {
			t.Fatal(err)
		}
		if got[0]["domain"] != tc.domain || got[0]["expires"] != tc.expiry || got[0]["sameSite"] != tc.site {
			t.Fatalf("%s => %#v", tc.extra, got)
		}
	}
	for _, source := range []string{`[]`, `{"cookies":[]}`} {
		got, err := cookieImportParams(source, "https://example.test/")
		if err != nil || len(got) != 0 {
			t.Fatalf("empty roundtrip: %#v %v", got, err)
		}
	}
}

func TestCookieImportRejectsEntireInvalidBatch(t *testing.T) {
	invalid := []string{
		`null`, `"not-cookie"`, `{}`, `{"name":"sid","value":{"redacted":true}}`,
		`{"name":"bad;name","value":"secret-value"}`, `{"name":"sid","value":"secret-value\n"}`,
		`{"name":"sid","value":"secret-value","domain":"evil.test"}`,
		`{"name":"sid","value":"secret-value","domain":"example.test","hostOnly":true}`,
		`{"name":"sid","value":"secret-value","url":"https://evil.test/"}`,
		`{"name":"sid","value":"secret-value","secure":"true"}`,
		`{"name":"sid","value":"secret-value","httpOnly":1}`,
		`{"name":"sid","value":"secret-value","expires":"later"}`,
		`{"name":"sid","value":"secret-value","expires":-2}`,
		`{"name":"sid","value":"secret-value","sourcePort":65536}`,
		`{"name":"sid","value":"secret-value","sourcePort":443.5}`,
		`{"name":"sid","value":"secret-value","path":"relative"}`,
		`{"name":"sid","value":"secret-value","sameSite":"unknown"}`,
		`{"name":"sid","value":"secret-value","sameSite":true}`,
		`{"name":"sid","value":"secret-value","priority":"unknown"}`,
		`{"name":"sid","value":"secret-value","partitionKey":"opaque"}`,
		`{"name":"sid","value":"secret-value","partitionKeyOpaque":true}`,
		`{"name":"sid","value":"secret-value","partitionKey":{"topLevelSite":"file:///tmp"}}`,
		`{"name":"sid","value":"secret-value","partitionKey":{"topLevelSite":"https://example.test","hasCrossSiteAncestor":"true"}}`,
	}
	for _, row := range invalid {
		got, err := cookieImportParams(`[{"name":"first","value":"valid"},`+row+`]`, "https://app.example.test/")
		if err == nil || got != nil {
			t.Fatalf("partial or invalid batch accepted: %s => %#v %v", row, got, err)
		}
		if strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("diagnostic printed cookie value: %v", err)
		}
	}
	for _, page := range []string{"", ":broken", "file:///tmp", "https://user:pass@example.test/"} {
		if _, err := cookieImportParams(`[]`, page); err == nil {
			t.Fatalf("invalid page accepted: %q", page)
		}
	}
}

func TestCookieDomainMatching(t *testing.T) {
	for _, tc := range []struct {
		host, domain string
		matches      bool
	}{
		{"app.example.test", ".example.test", true}, {"EXAMPLE.TEST", "example.test", true},
		{"badexample.test", ".example.test", false}, {"example.test.evil.test", "example.test", false}, {"example.test", "", false},
	} {
		if got := cookieDomainMatches(tc.host, tc.domain); got != tc.matches {
			t.Fatalf("%+v: %t", tc, got)
		}
	}
	var key any
	_ = json.Unmarshal([]byte(`{"topLevelSite":"https://example.test","hasCrossSiteAncestor":true}`), &key)
	if err := validateCookiePartition(key); err != nil {
		t.Fatal(err)
	}
}
