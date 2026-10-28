package commands

import (
	"strings"
	"testing"
)

func TestSliceSectionStopsAtSameOrHigherRank(t *testing.T) {
	doc := strings.Join([]string{
		"intro prose",
		"## Summary",
		"summary body",
		"### Detail",
		"nested body",
		"## Background",
		"background body",
	}, "\n")

	got, ok := sliceSection(doc, "Summary")
	if !ok {
		t.Fatal("expected Summary to match")
	}
	if !strings.Contains(got, "summary body") {
		t.Errorf("section lost its own body: %q", got)
	}
	// A deeper heading belongs to the section; a same-rank heading ends it.
	if !strings.Contains(got, "nested body") {
		t.Errorf("section dropped nested subsection: %q", got)
	}
	if strings.Contains(got, "background body") {
		t.Errorf("section bled into the next same-rank heading: %q", got)
	}
}

func TestSliceSectionMatchesCaseInsensitiveSubstring(t *testing.T) {
	doc := "# Key Takeaways:\nfirst\n# Next\nsecond"
	got, ok := sliceSection(doc, "key takeaways")
	if !ok {
		t.Fatal("expected case-insensitive substring match")
	}
	if !strings.Contains(got, "first") || strings.Contains(got, "second") {
		t.Errorf("unexpected section bounds: %q", got)
	}
}

func TestSliceSectionReportsNoMatch(t *testing.T) {
	if _, ok := sliceSection("## Only\nbody", "absent"); ok {
		t.Error("expected no match for an absent heading")
	}
}

func TestEvalResultStringUnwrapsNestedCDPShape(t *testing.T) {
	result := map[string]interface{}{
		"result": map[string]interface{}{
			"result": map[string]interface{}{
				"type":  "string",
				"value": "payload",
			},
		},
	}
	got, ok := evalResultString(result)
	if !ok || got != "payload" {
		t.Fatalf("got (%q, %v), want (\"payload\", true)", got, ok)
	}

	for name, bad := range map[string]map[string]interface{}{
		"missing outer": {},
		"missing inner": {"result": map[string]interface{}{}},
		"non-string":    {"result": map[string]interface{}{"result": map[string]interface{}{"value": 7}}},
	} {
		if _, ok := evalResultString(bad); ok {
			t.Errorf("%s: expected failure, got success", name)
		}
	}
}

func TestBrowserTabIDOfAcceptsJSONNumbers(t *testing.T) {
	// Bridge JSON decodes numbers as float64; tests and Go callers may use int.
	for name, value := range map[string]interface{}{
		"float64": float64(42),
		"int":     int(42),
		"int64":   int64(42),
	} {
		got, ok := browserTabIDOf(map[string]interface{}{"tab_id": value})
		if !ok || got != 42 {
			t.Errorf("%s: got (%d, %v), want (42, true)", name, got, ok)
		}
	}
	if _, ok := browserTabIDOf(map[string]interface{}{}); ok {
		t.Error("expected failure when tab_id is absent")
	}
	if _, ok := browserTabIDOf(map[string]interface{}{"tab_id": "42"}); ok {
		t.Error("expected failure when tab_id is a string")
	}
}
