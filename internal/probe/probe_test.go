package probe

import (
	"errors"
	"strings"
	"testing"
)

func probeTestSpec() ProbeSpec {
	return ProbeSpec{Method: "GET", URL: "https://example.test/users/123?owner=42&view=full", RequestRef: "h_capture_one",
		Hypothesis: "Another principal may read a private profile because the owner reference is client supplied.", OracleKind: ProbeAuthzCrossPrincipal}
}

func TestProbeEndpointFamilies(t *testing.T) {
	for _, tc := range []struct {
		name, method, url, want string
		invalid                 bool
	}{
		{"numeric", "get", "https://EXAMPLE.test:443/users/123?view=full&owner=42#fragment", "GET https://example.test/users/{id}?owner&view", false},
		{"uuid", "GET", "https://example.test/users/123e4567-e89b-12d3-a456-426614174000?owner=17&view=brief", "GET https://example.test/users/{id}?owner&view", false},
		{"hex", "GET", "https://example.test/users/abcdef0123456789", "GET https://example.test/users/{id}", false},
		{"opaque", "GET", "https://example.test/users/usr_aB12Cde34Fgh56", "GET https://example.test/users/{id}", false},
		{"version preserved", "POST", "https://example.test/v1/users", "POST https://example.test/v1/users", false},
		{"percent normalized", "GET", "https://example.test/%75sers/%31%32%33", "GET https://example.test/users/{id}", false},
		{"encoded slash retained", "GET", "https://example.test/users/a%2Fb", "GET https://example.test/users/a%2Fb", false},
		{"ipv6", "GET", "http://[::1]:80", "GET http://[::1]/", false},
		{"nondefault port", "GET", "https://example.test:8443/", "GET https://example.test:8443/", false},
		{"trailing slash", "GET", "https://example.test/users/", "GET https://example.test/users/", false},
		{"credentials excluded", "GET", "https://user:secret@example.test/?token=secret", "GET https://example.test/?token", false},
		{"relative", "GET", "/users/1", "", true},
		{"non HTTP", "GET", "file:///tmp/request", "", true},
		{"no method", "", "https://example.test/", "", true},
		{"invalid method", "GET\nPOST", "https://example.test/", "", true},
		{"bad query", "GET", "https://example.test/?a=%QQ", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ProbeEndpointFamily(tc.method, tc.url)
			if tc.invalid {
				if !errors.Is(err, ProbeErrInvalid) {
					t.Fatalf("expected invalid argument, got %q, %v", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestProbeContentAddress(t *testing.T) {
	baseSpec := probeTestSpec()
	base, err := ProbeNew(baseSpec)
	if err != nil {
		t.Fatal(err)
	}
	if len(base.ID) != len("probe_")+64 || base.State != ProbeUntested || base.Precondition == "" {
		t.Fatalf("invalid initial probe: %+v", base)
	}
	for _, tc := range []struct {
		name string
		edit func(*ProbeSpec)
		same bool
	}{
		{"recapture", func(s *ProbeSpec) { s.RequestRef = "h_capture_two" }, true},
		{"reworded hypothesis", func(s *ProbeSpec) { s.Hypothesis = "The same experiment phrased another way." }, true},
		{"different precondition wording", func(s *ProbeSpec) { s.Precondition = "Stable baseline required." }, true},
		{"same endpoint family", func(s *ProbeSpec) { s.URL = "https://EXAMPLE.test:443/users/999?view=brief&owner=500#section" }, true},
		{"default shape spelled out", func(s *ProbeSpec) { s.MutationShape = "  cross_principal  " }, true},
		{"different method", func(s *ProbeSpec) { s.Method = "DELETE" }, false},
		{"different origin", func(s *ProbeSpec) { s.URL = "https://other.test/users/123?owner=42&view=full" }, false},
		{"different query input", func(s *ProbeSpec) { s.URL += "&role=user" }, false},
		{"different mutation", func(s *ProbeSpec) { s.MutationShape = "query.owner=other" }, false},
		{"different oracle", func(s *ProbeSpec) { s.OracleKind = ProbeObjectReference }, false},
		{"explicit principals", func(s *ProbeSpec) { s.IdentityShape = []string{"alice", "bob"} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := baseSpec
			tc.edit(&spec)
			got, err := ProbeNew(spec)
			if err != nil || (got.ID == base.ID) != tc.same {
				t.Fatalf("same=%v, IDs %s and %s, error %v", tc.same, base.ID, got.ID, err)
			}
		})
	}
	a := baseSpec
	a.IdentityShape = []string{" bob ", "alice", "bob", "ANON"}
	a.MutationShape = "change  query.owner"
	b := baseSpec
	b.IdentityShape = []string{"anon", "alice", "bob"}
	b.MutationShape = "change query.owner"
	left, err := ProbeNew(a)
	if err != nil {
		t.Fatal(err)
	}
	right, err := ProbeNew(b)
	if err != nil || left.ID != right.ID {
		t.Fatalf("principal ordering/duplicates changed identity: %s != %s (%v)", left.ID, right.ID, err)
	}
}

func TestProbeRejectsInvalidProposals(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ProbeSpec)
	}{
		{"missing hypothesis", func(s *ProbeSpec) { s.Hypothesis = " " }},
		{"multiline hypothesis", func(s *ProbeSpec) { s.Hypothesis = "first\nsecond" }},
		{"large hypothesis", func(s *ProbeSpec) { s.Hypothesis = strings.Repeat("x", 2049) }},
		{"unknown oracle", func(s *ProbeSpec) { s.OracleKind = "status_200" }},
		{"body in ref", func(s *ProbeSpec) { s.RequestRef = `{"secret":"body"}` }},
		{"empty principal", func(s *ProbeSpec) { s.IdentityShape = []string{""} }},
		{"comma in principal", func(s *ProbeSpec) { s.IdentityShape = []string{"alice,bob"} }},
		{"control in principal", func(s *ProbeSpec) { s.IdentityShape = []string{"alice\x00"} }},
		{"large shape", func(s *ProbeSpec) { s.MutationShape = strings.Repeat("x", 2049) }},
		{"large precondition", func(s *ProbeSpec) { s.Precondition = strings.Repeat("x", 2049) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := probeTestSpec()
			tc.edit(&spec)
			if _, err := ProbeNew(spec); !errors.Is(err, ProbeErrInvalid) {
				t.Fatalf("got %v, want invalid argument", err)
			}
		})
	}
}

func TestProbeRecordValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec ProbeRecordSpec
		want ProbeState
	}{
		{"pass", ProbeRecordSpec{Verdict: ProbePass, Evidence: []string{"run_123/op_456"}}, ProbeVerified},
		{"fail", ProbeRecordSpec{Verdict: ProbeFail, Evidence: []string{"op_456"}}, ProbeRefuted},
		{"inconclusive", ProbeRecordSpec{Verdict: ProbeVerdictInconclusive, Evidence: []string{"run:123"}}, ProbeInconclusive},
		{"attempted", ProbeRecordSpec{State: ProbeAttempted, Evidence: []string{"operation_123"}}, ProbeAttempted},
		{"blocked", ProbeRecordSpec{State: ProbeBlocked, Evidence: []string{"run_123"}, Note: "Only one principal exists."}, ProbeBlocked},
		{"missing verdict", ProbeRecordSpec{Evidence: []string{"run_123"}}, ""},
		{"invented verdict", ProbeRecordSpec{Verdict: "success", Evidence: []string{"run_123"}}, ""},
		{"ambiguous outcome", ProbeRecordSpec{Verdict: ProbePass, State: ProbeBlocked, Evidence: []string{"run_123"}}, ""},
		{"no evidence", ProbeRecordSpec{Verdict: ProbePass}, ""},
		{"inline evidence", ProbeRecordSpec{Verdict: ProbePass, Evidence: []string{`{"status":200}`}}, ""},
		{"HTTP body", ProbeRecordSpec{Verdict: ProbePass, Evidence: []string{"HTTP 200 OK"}}, ""},
		{"request is not a run", ProbeRecordSpec{Verdict: ProbePass, Evidence: []string{"h_capture_one"}}, ""},
		{"path is not a run", ProbeRecordSpec{Verdict: ProbePass, Evidence: []string{"/tmp/body.txt"}}, ""},
		{"untested reset", ProbeRecordSpec{State: ProbeUntested, Evidence: []string{"run_123"}}, ""},
		{"blocked needs reason", ProbeRecordSpec{State: ProbeBlocked, Evidence: []string{"run_123"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := probeValidateRecord(tc.spec)
			if tc.want == "" {
				if !errors.Is(err, ProbeErrInvalid) {
					t.Fatalf("accepted invalid record: %+v, %v", got, err)
				}
			} else if err != nil || got.State != tc.want {
				t.Fatalf("got %s, %v; want %s", got.State, err, tc.want)
			}
		})
	}
}
