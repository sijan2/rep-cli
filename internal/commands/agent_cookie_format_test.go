package commands

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCookieNetscapeRoundTripPreservesBrowserAttributes(t *testing.T) {
	var cookies []map[string]any
	source := `[{"name":"sid","value":"abc","domain":"app.example.test","path":"/api","expires":1980000000.125,"httpOnly":true,"secure":true,"session":false,"sameSite":"None","priority":"High","sourcePort":443,"sourceScheme":"Secure","partitionKey":{"topLevelSite":"https://example.test","hasCrossSiteAncestor":false}},{"name":"empty","value":"","domain":".example.test","path":"/","expires":-1,"session":true,"httpOnly":false,"secure":false}]`
	if err := json.Unmarshal([]byte(source), &cookies); err != nil {
		t.Fatal(err)
	}
	encoded, meta, err := encodeCookieExport(cookies, "netscape")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "#HttpOnly_app.example.test\tFALSE\t/api\tTRUE\t1980000000\tsid\tabc") || meta["rep_metadata_comments"] != true {
		t.Fatalf("not an interoperable Netscape jar: %s, %#v", encoded, meta)
	}
	want, err := cookieImportParams(source, "https://app.example.test/")
	if err != nil {
		t.Fatal(err)
	}
	got, info, err := cookieImportData(string(encoded), "https://app.example.test/")
	if err != nil {
		t.Fatal(err)
	}
	if info.Format != "netscape" || info.IgnoredMetadata != 0 || !reflect.DeepEqual(want, got) {
		t.Fatalf("roundtrip changed cookie identity or attributes:\nwant %#v\ngot %#v\ninfo %#v", want, got, info)
	}
}

func TestCookieNetscapeCoreEditsTakePrecedenceOverMetadata(t *testing.T) {
	var cookies []map[string]any
	_ = json.Unmarshal([]byte(`[{"name":"sid","value":"old","domain":"example.test","path":"/","expires":1980000000.125,"sameSite":"Lax","secure":true,"httpOnly":true}]`), &cookies)
	encoded, _, err := encodeCookieExport(cookies, "netscape")
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(encoded), "\t1980000000\tsid\told\n", "\t1990000000\tsid\tnew\n", 1)
	got, info, err := cookieImportData(changed, "https://example.test/")
	if err != nil {
		t.Fatal(err)
	}
	if info.IgnoredMetadata != 0 || got[0]["value"] != "new" || got[0]["expires"] != float64(1990000000) || got[0]["sameSite"] != "Lax" {
		t.Fatalf("metadata overrode row edits: %#v %#v", got, info)
	}
	changed = strings.Replace(changed, "\tsid\tnew\n", "\trenamed\tnew\n", 1)
	got, info, err = cookieImportData(changed, "https://example.test/")
	if err != nil {
		t.Fatal(err)
	}
	if info.IgnoredMetadata != 1 || got[0]["name"] != "renamed" || got[0]["sameSite"] != nil {
		t.Fatalf("metadata did not bind to its row identity: %#v %#v", got, info)
	}
	malicious := "# Rep-Cookie-Metadata: {\"identity\":[\"example.test\",\"FALSE\",\"/\",\"sid\"],\"attributes\":{\"name\":\"wrong\",\"value\":\"wrong\",\"domain\":\"evil.test\",\"path\":\"/wrong\",\"expires\":3,\"sameSite\":\"Strict\"}}\nexample.test\tFALSE\t/\tFALSE\t0\tsid\tvalue\n"
	got, _, err = cookieImportData(malicious, "https://example.test/")
	if err != nil {
		t.Fatal(err)
	}
	if got[0]["name"] != "sid" || got[0]["value"] != "value" || got[0]["path"] != "/" || got[0]["expires"] != nil || got[0]["domain"] != nil || got[0]["sameSite"] != "Strict" {
		t.Fatalf("metadata overrode a core Netscape field: %#v", got)
	}
}

func TestCookieNetscapeStandardInputEmptyValuesAndErrors(t *testing.T) {
	for _, source := range []string{
		"# Netscape HTTP Cookie File\r\n#HttpOnly_.example.test\tTRUE\t/api\tTRUE\t0\tsession\tvalue\r\nexample.test\tFALSE\t/\tFALSE\t1990000000\tempty\t",
		"# Netscape HTTP Cookie File\n#HttpOnly_.example.test\tTRUE\t/api\tTRUE\t0\tsession\tvalue\nexample.test\tFALSE\t/\tFALSE\t1990000000\tempty\t\n",
	} {
		got, err := cookieImportParams(source, "https://example.test/")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0]["httpOnly"] != true || got[0]["domain"] != ".example.test" || got[0]["expires"] != nil || got[1]["value"] != "" {
			t.Fatalf("standard cookie jar changed: %#v", got)
		}
	}
	for _, source := range []string{"", "# Netscape HTTP Cookie File\n"} {
		got, err := cookieImportParams(source, "https://example.test/")
		if err != nil || len(got) != 0 {
			t.Fatalf("empty Netscape jar: %#v %v", got, err)
		}
	}
	// Chromium can store a nameless cookie from document.cookie = "value".
	for _, source := range []string{`[{"name":"","value":"nameless-value","path":"/"}]`, "example.test\tFALSE\t/\tFALSE\t0\t\tnameless-value\n"} {
		got, err := cookieImportParams(source, "https://example.test/")
		if err != nil || len(got) != 1 || got[0]["name"] != "" || got[0]["value"] != "nameless-value" {
			t.Fatalf("nameless cookie changed: %#v %v", got, err)
		}
	}
	for _, row := range []string{
		"not a cookie row", "example.test\tFALSE\t/\tMAYBE\t0\tsid\tsecret",
		"example.test\tFALSE\t/\tFALSE\tNaN\tsid\tsecret",
		"example.test\tFALSE\t/\tFALSE\t-1\tsid\tsecret",
		"example.test\tFALSE\t/\tFALSE\t0\tsid\tsecret\textra",
		"# Rep-Cookie-Metadata: not-json",
	} {
		got, err := cookieImportParams("example.test\tFALSE\t/\tFALSE\t0\tfirst\tvalid\n"+row, "https://example.test/")
		if got != nil || err == nil {
			t.Fatalf("invalid batch accepted: %#v %v", got, err)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("error copied value: %v", err)
		}
	}
}

func TestCookieNetscapeMetadataFollowsOnlyNextIdentity(t *testing.T) {
	comment := "# Rep-Cookie-Metadata: {\"identity\":[\"example.test\",\"FALSE\",\"/\",\"one\"],\"attributes\":{\"sameSite\":\"Strict\"}}\n"
	rows, info, err := cookieInputRows(comment + "example.test\tFALSE\t/\tFALSE\t0\ttwo\tvalue\nexample.test\tFALSE\t/\tFALSE\t0\tone\tvalue\n")
	if err != nil {
		t.Fatal(err)
	}
	if info.IgnoredMetadata != 1 || rows[0].(map[string]any)["sameSite"] != nil || rows[1].(map[string]any)["sameSite"] != nil {
		t.Fatalf("metadata moved to a later row: %#v %#v", rows, info)
	}
}

func TestCookiesAllPathsDoesNotBroadenHostOnlyScope(t *testing.T) {
	cookies := []map[string]any{
		{"domain": "app.example.test", "path": "/api", "name": "exact"},
		{"domain": ".example.test", "path": "/signin", "name": "parent"},
		{"domain": "example.test", "path": "/", "name": "parent-host-only"},
		{"domain": "other.example.test", "path": "/", "name": "sibling"},
		{"domain": ".evil.test", "path": "/", "name": "unrelated"},
	}
	got := cookiesForHost(cookies, "app.example.test")
	if len(got) != 2 || got[0]["name"] != "exact" || got[1]["name"] != "parent" {
		t.Fatalf("all-path collection changed host scope: %#v", got)
	}
	if _, _, err := encodeCookieExport(cookies, "unknown"); err == nil {
		t.Fatal("unknown format accepted")
	}
	if err := coreCookies(nil, "export", []string{"t1", "jar"}, "", "", "", cookieOptions{Format: "unknown"}); err == nil {
		t.Fatal("unknown format reached browser")
	}
}

func TestCookieImportAllDomainsKeepsEachCookieOrigin(t *testing.T) {
	source := `[{"name":"foreign-host","value":"one","domain":"localhost","hostOnly":true,"path":"/api","httpOnly":true},{"name":"foreign-domain","value":"two","domain":".other.test","secure":true,"path":"/"},{"name":"parent-host","value":"three","domain":"example.test","hostOnly":true,"path":"/"}]`
	if _, _, err := cookieImportData(source, "https://app.example.test/app"); err == nil || !strings.Contains(err.Error(), "--all-domains") {
		t.Fatalf("multi-site jar had no actionable scope error: %v", err)
	}
	got, _, err := cookieImportData(source, "https://app.example.test/app", true)
	if err != nil {
		t.Fatal(err)
	}
	if got[0]["url"] != "http://localhost/api" || got[0]["domain"] != nil || got[0]["httpOnly"] != true {
		t.Fatalf("foreign host cookie was retagged: %#v", got[0])
	}
	if got[1]["url"] != "https://other.test/" || got[1]["domain"] != ".other.test" || got[1]["secure"] != true {
		t.Fatalf("foreign domain cookie was retagged: %#v", got[1])
	}
	if got[2]["url"] != "http://example.test/" || got[2]["domain"] != nil {
		t.Fatalf("parent host-only cookie was broadened: %#v", got[2])
	}
	got, _, err = cookieImportData(`[{"name":"explicit","value":"v","url":"https://elsewhere.test/api"}]`, "https://app.example.test/", true)
	if err != nil || got[0]["url"] != "https://elsewhere.test/api" {
		t.Fatalf("explicit foreign URL changed: %#v %v", got, err)
	}
}

func TestCookieRestoreUsesCapturedSourceURLFields(t *testing.T) {
	got, err := cookieImportParams(`[{"name":"api","value":"v","domain":"example.test","path":"/api","sourceScheme":"NonSecure","sourcePort":80},{"name":"secure","value":"v","domain":"example.test","path":"/auth","secure":true,"sourceScheme":"Secure","sourcePort":443}]`, "http://example.test:8765/article")
	if err != nil {
		t.Fatal(err)
	}
	if got[0]["url"] != "http://example.test:80/api" || got[1]["url"] != "https://example.test:443/auth" {
		t.Fatalf("restore retagged captured origins: %#v", got)
	}
}

func TestCookieImportIPv6HostPreserved(t *testing.T) {
	for _, domain := range []string{"::1", "[::1]"} {
		source := `[{"name":"sid","value":"v","domain":"` + domain + `","path":"/api","hostOnly":true}]`
		got, _, err := cookieImportData(source, "https://example.test/", true)
		if err != nil || got[0]["url"] != "http://[::1]/api" || got[0]["domain"] != nil {
			t.Fatalf("IPv6 host changed: %#v %v", got, err)
		}
		got, err = cookieImportParams(source, "http://[::1]:8765/")
		if err != nil || got[0]["url"] != "http://[::1]:8765/api" {
			t.Fatalf("same-host IPv6 import: %#v %v", got, err)
		}
	}
}
