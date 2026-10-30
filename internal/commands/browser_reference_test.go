package commands

import (
	"encoding/json"

	"strings"
	"testing"
)

func TestReferenceOverviewKeepsBodyHeadlineAndResponsiveEvidenceVisible(t *testing.T) {
	body := map[string]any{"id": "body", "tag": "body", "selector": "body", "computed": map[string]any{"font-family": "Body Sans", "font-size": "17px", "line-height": "25px"}}
	headline := map[string]any{"id": "hero", "tag": "h2", "selector": ".hero h2", "text": "Product", "computed": map[string]any{"font-family": "Display Sans", "font-size": "56px", "line-height": "60px"}}
	styles := []any{
		map[string]any{"source": "https://example.test/main.css", "selector": ".hero h2", "elements": []any{"hero"}, "declarations": "font-size: 56px; line-height: 1.07143;", "active": true},
		map[string]any{"source": "https://example.test/main.css", "selector": ".hero h2", "elements": []any{"hero"}, "declarations": "font-size: 48px; line-height: 1.08349;", "active": false, "conditions": []any{map[string]any{"kind": "media", "text": "(max-width: 1068px)", "active": false}}},
	}
	data := map[string]any{"elements": []any{body, headline}, "styles": styles, "fonts": map[string]any{"computed_stacks": []any{strings.Repeat("Oversized Font ", 3000)}}}
	preview := referencePreview(data, referenceSections(data))
	typography := referenceArray(preview["typography"])
	rules := referenceArray(preview["headline_css"])
	encoded, _ := json.Marshal(preview)
	if len(encoded) > referencePreviewBudgetBytes || len(typography) != 2 || len(rules) != 2 {
		t.Fatalf("reference hid the required design facts: %s", encoded)
	}
	if referenceMap(typography[0])["font_size"] != "17px" || referenceMap(typography[1])["line_height"] != "60px" || !strings.Contains(string(encoded), "max-width: 1068px") || referenceMap(rules[1])["active"] != false {
		t.Fatalf("computed typography or inactive responsive condition lost: %s", encoded)
	}
	if referenceMap(typography[0])["json_pointer"] != "/snapshot/reference/elements/0/computed" || referenceMap(typography[1])["json_pointer"] != "/snapshot/reference/elements/1/computed" || referenceMap(rules[1])["json_pointer"] != "/snapshot/reference/styles/1" {
		t.Fatalf("preview evidence does not point to the exact source rows: %s", encoded)
	}
}

func TestReferenceMathConnectsAuthoredCandidateToMeasuredBox(t *testing.T) {
	data := map[string]any{
		"elements": []any{map[string]any{"id": "n1", "tag": "main", "selector": "main", "computed": map[string]any{"width": "900px"}, "rect": map[string]any{"width": float64(948)}, "geometry": map[string]any{"device_pixel_width": float64(1896)}}},
		"layout": map[string]any{"css_math": []any{
			map[string]any{"selector": ":root", "declarations": "--nav-height: calc(52px * var(--scale))", "elements": []any{"n1"}},
			map[string]any{"selector": "main", "source": "https://example.test/main.css", "declarations": "width: min(900px, calc(100vw - 48px))", "elements": []any{"n1"}, "active": false, "conditions": []any{map[string]any{"kind": "media", "text": "(max-width: 700px)", "active": false}}},
		}},
	}
	preview := referencePreview(data, referenceSections(data))
	first := referenceMap(referenceArray(preview["css_math"])[0])
	box := referenceMap(first["element_example"])
	if first["json_pointer"] != "/snapshot/reference/layout/css_math/1" || first["active"] != false || len(referenceArray(first["conditions"])) != 1 || box["id"] != "n1" || box["computed_width"] != "900px" || box["css_pixel_width"] != float64(948) || box["device_pixel_width"] != float64(1896) || box["json_pointer"] != "/snapshot/reference/elements/0" {
		t.Fatalf("candidate math lost its conditions, source, or distinct measured box: %+v", first)
	}
}
