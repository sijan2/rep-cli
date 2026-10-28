package cmd

import (
	"strings"
	"testing"
)

func bodies(ss ...string) [][]byte {
	out := make([][]byte, 0, len(ss))
	for _, s := range ss {
		out = append(out, []byte(s))
	}
	return out
}

func TestMaskSeparatesVolatileFromStableJSON(t *testing.T) {
	// Same logical response twice; only the trace id moved.
	mask := buildVolatilityMask(bodies(
		`{"id":1,"owner":"alice","trace":"aaa"}`,
		`{"id":1,"owner":"alice","trace":"bbb"}`,
	), []int{200, 200})

	if mask.Kind != "json" || !mask.Usable {
		t.Fatalf("expected a usable json mask, got %+v", mask)
	}
	if !mask.VolatilePointers["/trace"] {
		t.Error("trace should be volatile")
	}
	if mask.VolatilePointers["/id"] || mask.VolatilePointers["/owner"] {
		t.Error("id and owner should be stable")
	}
	if mask.StableCount != 2 || mask.VolatileCount != 1 {
		t.Errorf("got stable=%d volatile=%d, want 2/1", mask.StableCount, mask.VolatileCount)
	}
}

// The central regression: byte-equality reports a difference caused purely by
// endpoint noise. The masked comparison must call that equivalent.
func TestNoiseAloneIsEquivalentNotDifferent(t *testing.T) {
	mask := buildVolatilityMask(bodies(
		`{"secret":"S","trace":"aaa"}`,
		`{"secret":"S","trace":"bbb"}`,
	), []int{200, 200})

	got := compareAgainstBaseline(mask,
		[]byte(`{"secret":"S","trace":"aaa"}`),
		[]byte(`{"secret":"S","trace":"zzz"}`), 200, 200, true)

	if got.Verdict != "equivalent" {
		t.Fatalf("noise-only change judged %q (%s), want equivalent", got.Verdict, got.Reason)
	}
	if got.ChangedCount != 0 {
		t.Errorf("expected no stable changes, got %v", got.ChangedPaths)
	}
}

func TestStableFieldChangeIsReported(t *testing.T) {
	mask := buildVolatilityMask(bodies(
		`{"secret":"S","trace":"aaa"}`,
		`{"secret":"S","trace":"bbb"}`,
	), []int{200, 200})

	got := compareAgainstBaseline(mask,
		[]byte(`{"secret":"S","trace":"aaa"}`),
		[]byte(`{"secret":"LEAKED","trace":"zzz"}`), 200, 200, true)

	if got.Verdict != "differs" {
		t.Fatalf("got %q, want differs", got.Verdict)
	}
	if len(got.ChangedPaths) != 1 || got.ChangedPaths[0] != "/secret" {
		t.Errorf("got changed=%v, want [/secret]", got.ChangedPaths)
	}
}

func TestIncompleteEvidenceIsInconclusiveNotEquivalent(t *testing.T) {
	mask := buildVolatilityMask(bodies(`{"a":1}`, `{"a":1}`), []int{200, 200})
	got := compareAgainstBaseline(mask, []byte(`{"a":1}`), []byte(`{"a":1}`), 200, 200, false)
	if got.Verdict != "inconclusive" {
		t.Fatalf("incomplete body judged %q, want inconclusive", got.Verdict)
	}
}

func TestUnstableStatusSuppressesStatusVerdict(t *testing.T) {
	// An endpoint that flaps between 200 and 503 on identical requests cannot
	// support a status-based conclusion.
	mask := buildVolatilityMask(bodies(`{"a":1}`, `{"a":1}`), []int{200, 503})
	if mask.StatusStable {
		t.Fatal("status should be unstable")
	}
	got := compareAgainstBaseline(mask, []byte(`{"a":1}`), []byte(`{"a":1}`), 200, 403, true)
	if got.Verdict != "inconclusive" {
		t.Fatalf("got %q (%s), want inconclusive", got.Verdict, got.Reason)
	}
}

func TestUnstableLineCountYieldsUnusableMask(t *testing.T) {
	mask := buildVolatilityMask(bodies("a\nb", "a\nb\nc"), []int{200, 200})
	if mask.Usable {
		t.Fatal("a shifting line count must not produce a usable positional mask")
	}
	got := compareAgainstBaseline(mask, []byte("a\nb"), []byte("a\nX"), 200, 200, true)
	if got.Verdict != "inconclusive" {
		t.Errorf("got %q, want inconclusive", got.Verdict)
	}
}

func TestSingleSampleCannotEstablishVolatility(t *testing.T) {
	if buildVolatilityMask(bodies(`{"a":1}`), []int{200}).Usable {
		t.Error("one sample cannot establish what varies on its own")
	}
}

func TestFlattenJSONIgnoresKeyOrderAndEscapesPointers(t *testing.T) {
	a, okA := flattenJSON([]byte(`{"x":1,"y":{"z":2}}`))
	b, okB := flattenJSON([]byte(`{"y":{"z":2},"x":1}`))
	if !okA || !okB {
		t.Fatal("both documents should flatten")
	}
	if a["/x"] != b["/x"] || a["/y/z"] != b["/y/z"] {
		t.Errorf("key order changed the flattening: %v vs %v", a, b)
	}
	esc, ok := flattenJSON([]byte(`{"a/b":1}`))
	if !ok || esc["/a~1b"] != "1" {
		t.Errorf("pointer escaping failed: %v", esc)
	}
	if _, ok := flattenJSON([]byte(`not json`)); ok {
		t.Error("non-JSON must not flatten")
	}
}

func TestAuthzVerdictRequiresConclusiveEvidence(t *testing.T) {
	equivalent := comparison{Verdict: "equivalent"}
	differs := comparison{Verdict: "differs"}
	inconclusive := comparison{Verdict: "inconclusive"}

	// Another principal reaching an equivalent response is a LEAD, hedged.
	got := interpretAuthz("alice", map[string]comparison{"bob": equivalent}, map[string]int{"bob": 200})
	if got.Verdict != "possible_authz_issue" {
		t.Errorf("got %q, want possible_authz_issue", got.Verdict)
	}
	if !strings.Contains(got.Reason, "confirm") {
		t.Errorf("verdict must hedge pending body confirmation, got %q", got.Reason)
	}

	// Everyone else blocked => enforced.
	got = interpretAuthz("alice", map[string]comparison{"bob": differs, "anon": differs},
		map[string]int{"bob": 403, "anon": 401})
	if got.Verdict != "enforced" {
		t.Errorf("got %q, want enforced", got.Verdict)
	}

	// One unusable observation must not be rounded into either conclusion.
	got = interpretAuthz("alice", map[string]comparison{"bob": equivalent, "anon": inconclusive},
		map[string]int{"bob": 200, "anon": 0})
	if got.Verdict != "inconclusive" {
		t.Errorf("got %q, want inconclusive", got.Verdict)
	}
}

func TestIsCredentialHeaderMatchesVendorTokenNames(t *testing.T) {
	for _, name := range []string{"Cookie", "authorization", "X-CSRF-Token", "x-api-key", "X-Shop-Access-Token"} {
		if !isCredentialHeader(name) {
			t.Errorf("%q should be treated as a credential header", name)
		}
	}
	for _, name := range []string{"Accept", "User-Agent", "Content-Type", "Referer"} {
		if isCredentialHeader(name) {
			t.Errorf("%q must not be treated as a credential header", name)
		}
	}
}
