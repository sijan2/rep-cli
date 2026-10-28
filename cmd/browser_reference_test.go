package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/repplus/rep-cli/internal/scope"
)

func TestSavedReferenceSurvivesClosedBrowserWithGaplessUTF8Reads(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	fastTestScope(t)
	text := strings.Repeat("界é ", 100)
	reference, _ := json.Marshal(map[string]any{"content": map[string]any{"text": text, "status": "ready", "complete": true, "source": "rendered_dom"}})
	id := "r_0123456789abcdef0123456789abcdef"
	dir, err := referencePath(id)
	if err != nil {
		t.Fatal(err)
	}
	archive := referenceArchive{Version: 1, ID: id, Workspace: "fast-test", Task: "action", Tab: 7,
		Snapshot: browserfast.Result{Status: "ready", URL: "https://example.test/", ObservedAt: "2026-10-07T04:00:00Z", Reference: reference}}
	encoded, _ := json.Marshal(archive)
	if _, err := referenceFile(filepath.Join(dir, "snapshot.json"), encoded); err != nil {
		t.Fatal(err)
	}
	deps := jevDOMDependencies{browser: func(context.Context, string) (jevdom.Browser, error) {
		t.Fatal("saved reference tried to connect to the browser")
		return nil, nil
	}}
	var collected string
	for offset := 0; offset < len(text); {
		command := newBrowserReferenceCommand(deps)
		addDecisionBrowserFlag(command)
		var stdout bytes.Buffer
		command.SetOut(&stdout)
		command.SetArgs([]string{"--saved", id, "--section", "content", "--offset", strconv.Itoa(offset), "--max-text", "256"})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		var result fastOutput
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(result.Text) || result.Text == "" || result.BridgeCalls != 0 || result.ObservedAt != archive.Snapshot.ObservedAt {
			t.Fatalf("saved read lost provenance, UTF-8 or progress: %s", stdout.String())
		}
		collected += result.Text
		offset += len(result.Text)
		if (result.Next != "") != (offset < len(text)) {
			t.Fatalf("wrong continuation at %d: %s", offset, result.Next)
		}
	}
	if collected != text {
		t.Fatal("saved pagination skipped or duplicated content")
	}
	info, err := os.Stat(filepath.Join(dir, "snapshot.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("snapshot is not private: %v, %v", info, err)
	}
	_, err = scope.Configure(scope.Options{Workspace: "fast-test", Task: "another", WorkspaceSet: true, TaskSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadReference(id); err == nil {
		t.Fatal("another task read the reference")
	}
}

func TestSavedReferenceOversizedRecordPointsToExactArtifact(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	fastTestScope(t)
	reference, _ := json.Marshal(map[string]any{"content": map[string]any{}, "styles": []any{map[string]any{"declarations": strings.Repeat("x", 20000)}, map[string]any{"declarations": "width: 20px"}}})
	archive := referenceArchive{ID: "r_0123456789abcdef0123456789abcdef", Snapshot: browserfast.Result{Status: "ready", Reference: reference}}
	command := newBrowserReferenceCommand(jevDOMDependencies{})
	result, err := referenceProjection(command, archive, "/private/task/reference", "styles", 0, fastFlags{maxText: 4096, limit: 40})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Reference) > 16384 || !strings.Contains(string(result.Reference), "record_exceeds_inline_budget") || !strings.Contains(string(result.Reference), "snapshot.json") || !strings.Contains(result.Next, "--offset 1") {
		t.Fatalf("oversized row was silently truncated or blocked progress: %s; %s", result.Reference, result.Next)
	}
	if _, err := referenceProjection(command, archive, "", "unknown", 0, fastFlags{}); err == nil {
		t.Fatal("unknown saved section accepted")
	}
}

func TestReferenceOverviewPreviewHasTruthfulHardBudget(t *testing.T) {
	fastTestScope(t)
	long := strings.Repeat("界", 6000)
	fonts := []any{}
	for i := range 24 {
		fonts = append(fonts, long+strconv.Itoa(i))
	}
	rows := func(count int, value func(int) any) []any {
		result := make([]any, 0, count)
		for i := range count {
			result = append(result, value(i))
		}
		return result
	}
	reference, _ := json.Marshal(map[string]any{
		"content": map[string]any{
			"text":     "Useful body",
			"headings": rows(8, func(i int) any { return map[string]any{"level": 2, "text": "Heading " + strconv.Itoa(i) + long} }),
		},
		"fonts": map[string]any{"computed_stacks": fonts},
		"layout": map[string]any{
			"viewport":      map[string]any{"width": long, "height": 900, "device_pixel_ratio": 2},
			"media_queries": rows(8, func(i int) any { return map[string]any{"text": "media " + strconv.Itoa(i) + long, "active": i%2 == 0} }),
			"css_math": rows(8, func(i int) any {
				return map[string]any{"source": long, "selector": long, "declarations": long, "elements": []any{long, long, long, long, long}}
			}),
		},
		"performance": map[string]any{
			"navigation": map[string]any{"type": long, "ttfb_ms": 10},
			"paint":      map[string]any{"first-paint": long, "first-contentful-paint": 20},
		},
		"assets": rows(8, func(i int) any {
			return map[string]any{"kind": "image", "url": "https://example.test/" + long + strconv.Itoa(i), "natural_width": 100, "picture_sources": []any{1, 2}}
		}),
		"elements": rows(8, func(i int) any {
			return map[string]any{"selector": long, "text": long, "rect": map[string]any{"x": 1, "width": 100},
				"computed": map[string]any{"font-family": long, "font-size": long, "line-height": long, "display": long, "gap": long, "padding": long}}
		}),
	})
	archive := referenceArchive{ID: "r_0123456789abcdef0123456789abcdef", Snapshot: browserfast.Result{Status: "ready", Reference: reference}}
	result, err := referenceProjection(newBrowserReferenceCommand(jevDOMDependencies{}), archive, "/private/task/reference", "overview", 0, fastFlags{maxText: 4096, limit: 40})
	if err != nil {
		t.Fatal(err)
	}
	var projection map[string]any
	if err := json.Unmarshal(result.Reference, &projection); err != nil {
		t.Fatal(err)
	}
	preview := referenceMap(projection["preview"])
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > referencePreviewBudgetBytes {
		t.Fatalf("preview exceeded its hard budget: %d bytes", len(encoded))
	}
	coverage := referenceMap(preview["preview_coverage"])
	if int(coverage["budget_bytes"].(float64)) != referencePreviewBudgetBytes || int(coverage["encoded_bytes"].(float64)) != len(encoded) {
		t.Fatalf("preview budget metadata is not truthful: size=%d coverage=%+v", len(encoded), coverage)
	}
	baseClipped := referenceMap(coverage["base_values_clipped"])
	if baseClipped["viewport"] != true || baseClipped["navigation"] != true || baseClipped["paint"] != true {
		t.Fatalf("base-field clipping was not disclosed: %+v", baseClipped)
	}
	groups := referenceMap(coverage["groups"])
	for _, name := range []string{"font_stacks", "media_queries", "css_math", "assets", "headings", "element_examples"} {
		meta := referenceMap(groups[name])
		total := int(meta["total"].(float64))
		returned := int(meta["returned"].(float64))
		omitted := int(meta["omitted"].(float64))
		if total == 0 || returned == 0 || returned+omitted != total {
			t.Fatalf("group %s lost representative or omission accounting: %+v", name, meta)
		}
	}
	if strings.Contains(string(encoded), long) {
		t.Fatal("preview retained an unbounded source value")
	}
	counts := referenceMap(projection["counts"])
	reads := referenceMap(projection["read_sections"])
	if int(counts["fonts"].(float64)) != len(fonts) || reads["fonts"] == nil || reads["assets"] == nil || reads["layout"] == nil {
		t.Fatalf("bounded preview hid full saved evidence: counts=%+v reads=%+v", counts, reads)
	}
	if !bytes.Contains(archive.Snapshot.Reference, []byte(long)) {
		t.Fatal("full snapshot evidence was modified while creating the preview")
	}
}

func TestReferenceOverviewPreviewBoundsPathologicalFontStacksIndependently(t *testing.T) {
	fastTestScope(t)
	long := strings.Repeat("font-family-", 2000)
	fonts := make([]any, 64)
	for i := range fonts {
		fonts[i] = long + strconv.Itoa(i)
	}
	reference, _ := json.Marshal(map[string]any{
		"content": map[string]any{"text": "body", "headings": []any{map[string]any{"level": 1, "text": "Useful heading"}}},
		"fonts":   map[string]any{"computed_stacks": fonts},
	})
	archive := referenceArchive{ID: "r_0123456789abcdef0123456789abcdef", Snapshot: browserfast.Result{Status: "ready", Reference: reference}}
	result, err := referenceProjection(newBrowserReferenceCommand(jevDOMDependencies{}), archive, "/private/task/reference", "overview", 0, fastFlags{maxText: 4096, limit: 40})
	if err != nil {
		t.Fatal(err)
	}
	var projection map[string]any
	if err := json.Unmarshal(result.Reference, &projection); err != nil {
		t.Fatal(err)
	}
	preview := referenceMap(projection["preview"])
	encoded, _ := json.Marshal(preview)
	if len(encoded) > referencePreviewBudgetBytes || !strings.Contains(string(encoded), "Useful heading") {
		t.Fatalf("font stacks consumed the bounded useful preview: %d bytes: %s", len(encoded), encoded)
	}
	meta := referenceMap(referenceMap(referenceMap(preview["preview_coverage"])["groups"])["font_stacks"])
	returned := int(meta["returned"].(float64))
	if int(meta["total"].(float64)) != len(fonts) || returned == 0 || int(meta["omitted"].(float64)) != len(fonts)-returned || int(meta["records_clipped"].(float64)) != returned {
		t.Fatalf("font-stack coverage is inaccurate: %+v", meta)
	}
}

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

func TestReferencePendingPreservesFocusedReadAndImagePreference(t *testing.T) {
	fastTestScope(t)
	command := newBrowserReferenceCommand(jevDOMDependencies{})
	addDecisionBrowserFlag(command)
	selector := `div[data-label="Alice's notes"]`
	_ = command.Flags().Set("max-text", "512")
	_ = command.Flags().Set("limit", "7")
	next := referenceResume(command, 7, selector, false)
	if !strings.Contains(next, "--tab 7") || !strings.Contains(next, "--selector "+shellQuote(selector)) || !strings.Contains(next, "--screenshot=false") || !strings.Contains(next, "--timeout 10s") || !strings.Contains(next, "--max-text 512 --limit 7") {
		t.Fatalf("pending continuation lost the requested scope: %s", next)
	}
}
