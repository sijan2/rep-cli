package surface

import "testing"

// The defect this package exists to fix: a browser attaches cookies to every
// same-origin request, so the old scorer rated fonts and script bundles as
// tamper-worthy. Static traffic must score zero no matter what it carries.
func TestStaticTrafficNeverScores(t *testing.T) {
	cases := []struct {
		name string
		obs  Observation
	}{
		{"woff2 by extension", Observation{Method: "GET", URL: "https://x.test/f/a.woff2", Domain: "x.test", HasCookie: true, IsPrimary: true}},
		{"css by content type", Observation{Method: "GET", URL: "https://x.test/s", Domain: "x.test", ContentType: "text/css", HasCookie: true, IsPrimary: true}},
		{"script by resource type", Observation{Method: "GET", URL: "https://x.test/s", Domain: "x.test", ResourceType: "script", HasCookie: true, IsPrimary: true}},
		{"content-hashed bundle", Observation{Method: "GET", URL: "https://x.test/_next/main-2d8e3876ec53ebc0.js", Domain: "x.test", HasCookie: true, IsPrimary: true}},
		{"image", Observation{Method: "GET", URL: "https://x.test/i/logo.png", Domain: "x.test", HasCookie: true, IsPrimary: true}},
	}
	for _, tc := range cases {
		families := Build([]Observation{tc.obs})
		if len(families) != 1 {
			t.Fatalf("%s: expected one family", tc.name)
		}
		if families[0].Kind != KindStatic {
			t.Errorf("%s: classified %s, want static", tc.name, families[0].Kind)
		}
		if families[0].Score != 0 {
			t.Errorf("%s: scored %.1f, want 0", tc.name, families[0].Score)
		}
	}
}

func TestNormalizeCollapsesIdentifiersButKeepsRouteWords(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://x.test/api/doc/1", "/api/doc/{num}"},
		{"https://x.test/api/doc/2", "/api/doc/{num}"},
		{"https://x.test/u/550e8400-e29b-41d4-a716-446655440000/p", "/u/{uuid}/p"},
		{"https://x.test/api/v2/users", "/api/v2/users"}, // version is structure
		{"https://x.test/blog/introducing-autonomous-exposure", "/blog/introducing-autonomous-exposure"},
	}
	for _, tc := range cases {
		got, _ := NormalizeRoute(tc.in)
		if got != tc.want {
			t.Errorf("NormalizeRoute(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Two requests to different object ids must become ONE family, and the family
// must remember that the client chose two different identifiers.
func TestFamilyCollapsesAndCountsDistinctObjectValues(t *testing.T) {
	obs := []Observation{
		{ID: "a", Method: "GET", URL: "https://x.test/api/doc/1", Domain: "x.test", ContentType: "application/json", HasCookie: true, IsPrimary: true, Status: 200},
		{ID: "b", Method: "GET", URL: "https://x.test/api/doc/2", Domain: "x.test", ContentType: "application/json", HasCookie: true, IsPrimary: true, Status: 200},
		{ID: "c", Method: "GET", URL: "https://x.test/api/doc/1", Domain: "x.test", ContentType: "application/json", HasCookie: true, IsPrimary: true, Status: 200},
	}
	families := Build(obs)
	if len(families) != 1 {
		t.Fatalf("got %d families, want 1", len(families))
	}
	f := families[0]
	if f.Count != 3 {
		t.Errorf("count = %d, want 3", f.Count)
	}
	if f.DistinctObjectValues["path[2]"] != 2 {
		t.Errorf("distinct object values = %d, want 2", f.DistinctObjectValues["path[2]"])
	}

	candidates := Candidates(families, 10)
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
	if candidates[0].Oracle != OracleAuthzCrossPrincipal {
		t.Errorf("oracle = %s, want %s", candidates[0].Oracle, OracleAuthzCrossPrincipal)
	}
	if candidates[0].Next == "" {
		t.Error("candidate must carry a runnable next command")
	}
}

// A single observed value is not evidence the slot is client-chosen.
func TestSingleObjectValueDoesNotImplyEnumeration(t *testing.T) {
	families := Build([]Observation{
		{ID: "a", Method: "GET", URL: "https://x.test/api/doc/1", Domain: "x.test", ContentType: "application/json", HasCookie: true, IsPrimary: true},
	})
	for _, s := range families[0].Signals {
		if s.Name == "object_reference" {
			t.Error("object_reference must require more than one observed value")
		}
	}
}

func TestCSRFAbsenceOnlyFlaggedForAuthenticatedMutations(t *testing.T) {
	withToken := Build([]Observation{{
		ID: "a", Method: "POST", URL: "https://x.test/api/pay", Domain: "x.test",
		ContentType: "application/json", HasCookie: true, IsPrimary: true,
		HeaderNames: []string{"X-CSRF-Token"},
	}})
	for _, s := range withToken[0].Signals {
		if s.Name == "no_csrf_token" {
			t.Error("a request carrying a CSRF token must not be flagged")
		}
	}

	without := Build([]Observation{{
		ID: "b", Method: "POST", URL: "https://x.test/api/pay", Domain: "x.test",
		ContentType: "application/json", HasCookie: true, IsPrimary: true,
	}})
	found := false
	for _, s := range without[0].Signals {
		if s.Name == "no_csrf_token" {
			found = true
		}
	}
	if !found {
		t.Error("cookie-authenticated mutation without a CSRF token should be flagged")
	}
}

func TestClientAssertedFieldsDetected(t *testing.T) {
	families := Build([]Observation{{
		ID: "a", Method: "POST", URL: "https://x.test/api/order", Domain: "x.test",
		ContentType: "application/json", HasCookie: true, IsPrimary: true,
		BodyKeys: []string{"sku", "price", "is_admin"},
	}})
	f := families[0]
	if len(f.ClientAsserted) != 2 {
		t.Fatalf("client asserted = %v, want price and is_admin", f.ClientAsserted)
	}
	if Candidates(families, 1)[0].Oracle != OracleMutationSideEffect {
		t.Error("client-asserted values should select the side-effect oracle")
	}
}

// The old scorer produced hundreds of rows at one score, so "top N" became
// alphabetical. Distinct families must receive distinct scores.
func TestScoresDiscriminateBetweenFamilies(t *testing.T) {
	families := Build([]Observation{
		{ID: "a", Method: "GET", URL: "https://x.test/api/doc/1", Domain: "x.test", ContentType: "application/json", HasCookie: true, IsPrimary: true},
		{ID: "b", Method: "GET", URL: "https://x.test/api/doc/2", Domain: "x.test", ContentType: "application/json", HasCookie: true, IsPrimary: true},
		{ID: "c", Method: "GET", URL: "https://x.test/about", Domain: "x.test", ContentType: "text/html", IsPrimary: true},
		{ID: "d", Method: "POST", URL: "https://x.test/api/pay", Domain: "x.test", ContentType: "application/json", HasCookie: true, IsPrimary: true, BodyKeys: []string{"amount"}},
	})
	seen := map[float64]int{}
	for _, f := range families {
		if f.Score > 0 {
			seen[f.Score]++
		}
	}
	if len(seen) < 2 {
		t.Errorf("expected distinct scores across families, got %v", seen)
	}
}

func TestThirdPartyTrafficIsDamped(t *testing.T) {
	primary := Build([]Observation{{
		ID: "a", Method: "GET", URL: "https://x.test/api/me", Domain: "x.test",
		ContentType: "application/json", HasCookie: true, IsPrimary: true,
	}})[0]
	third := Build([]Observation{{
		ID: "b", Method: "GET", URL: "https://ads.test/api/me", Domain: "ads.test",
		ContentType: "application/json", HasCookie: true, IsPrimary: false,
	}})[0]
	if !(third.Score < primary.Score) {
		t.Errorf("third-party score %.2f should be below primary %.2f", third.Score, primary.Score)
	}
}

func TestIsWordyKeepsSlugsOutOfIdentifierBuckets(t *testing.T) {
	if identifierShape("introducing-autonomous-exposure-management") != "" {
		t.Error("a readable slug is route structure, not an object identifier")
	}
	if identifierShape("aGVsbG8gd29ybGQgdGhpcyBpcw") == "" {
		t.Error("an opaque token should be treated as an identifier")
	}
}
