package visual

import (
	"encoding/json"
	"testing"
)

// A small device-scaled snapshot: HTML > BODY > DIV#main > A(href) > SPAN "Save".
// The document is scrolled 100 CSS px and reported at 2 snapshot units per CSS px.
func testSnapshot() json.RawMessage {
	wire := map[string]any{
		"strings": []string{"HTML", "BODY", "DIV", "A", "SPAN", "id", "main", "href", "/save", "Save", "#text", "role", "button", "P", "Paragraph text"},
		"documents": []map[string]any{{
			"scrollOffsetX": 0, "scrollOffsetY": 200, "contentWidth": 2000,
			"nodes": map[string]any{
				"parentIndex":   []int{-1, 0, 1, 2, 3, 4, 2, 6},
				"nodeType":      []int{1, 1, 1, 1, 1, 3, 1, 3},
				"nodeName":      []int{0, 1, 2, 3, 4, 10, 13, 10},
				"nodeValue":     []int{-1, -1, -1, -1, -1, 9, -1, 14},
				"backendNodeId": []int64{1, 2, 3, 4, 5, 6, 7, 8},
				"attributes":    [][]int{{}, {}, {5, 6}, {7, 8, 11, 12}, {}, {}, {}, {}},
				"isClickable":   map[string]any{"index": []int{3}},
			},
			"layout": map[string]any{
				"nodeIndex": []int{0, 1, 2, 3, 4, 6},
				// bounds in snapshot units (2 per CSS px), document coordinates
				"bounds": [][]float64{{0, 0, 2000, 6000}, {0, 0, 2000, 6000}, {100, 300, 1800, 1000}, {200, 400, 400, 100}, {210, 410, 300, 80}, {200, 600, 1600, 100}},
			},
		}},
	}
	data, _ := json.Marshal(wire)
	return data
}

func TestParseConvertsSnapshotUnitsAndScroll(t *testing.T) {
	scene, err := Parse(testSnapshot(), Viewport{Width: 1000, Height: 800, ScrollX: 0, ScrollY: 100, ScrollWidth: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if scene.Scale != 2 || scene.Documents != 1 {
		t.Fatalf("scale not derived from content width: %+v", scene)
	}
	var anchor *Element
	for i := range scene.Elements {
		if scene.Elements[i].Tag == "A" {
			anchor = &scene.Elements[i]
		}
		if scene.Elements[i].Tag == "HTML" || scene.Elements[i].Tag == "BODY" {
			t.Fatal("document containers must be skipped")
		}
	}
	if anchor == nil {
		t.Fatal("anchor missing")
	}
	// (200-0)/2 = 100 css x; (400-200)/2 = 100 css y; 400/2 x 100/2
	if anchor.Box != (Rect{X: 100, Y: 100, Width: 200, Height: 50}) {
		t.Fatalf("anchor box wrong: %+v", anchor.Box)
	}
	if !anchor.Clickable || anchor.Attributes["href"] != "/save" || anchor.Attributes["role"] != "button" {
		t.Fatalf("anchor evidence lost: %+v", anchor)
	}
	for _, element := range scene.Elements {
		if element.Tag == "SPAN" && element.Text != "Save" {
			t.Fatalf("own text lost: %+v", element)
		}
	}
}

func TestRankPrefersTightestOverlapAndControlClimbs(t *testing.T) {
	scene, err := Parse(testSnapshot(), Viewport{Width: 1000, Height: 800, ScrollY: 100, ScrollWidth: 1000})
	if err != nil {
		t.Fatal(err)
	}
	// Model box roughly over the SPAN text (105..255 x 105..145 css).
	box := Rect{X: 105, Y: 105, Width: 150, Height: 40}
	matches := scene.Rank(box, 3)
	if len(matches) == 0 || matches[0].Element.Tag != "SPAN" {
		t.Fatalf("expected the span first: %+v", matches)
	}
	control := scene.Control(matches[0].Element, box)
	if control == nil || control.Tag != "A" || control.BackendNodeID != 4 {
		t.Fatalf("expected the anchor as control: %+v", control)
	}
	// A box far away from everything ranks nothing.
	if len(scene.Rank(Rect{X: 900, Y: 700, Width: 50, Height: 50}, 3)) != 0 {
		t.Fatal("no element overlaps that region")
	}
	// A non-interactive paragraph has no control ancestor inside the box.
	for _, element := range scene.Elements {
		if element.Tag == "P" {
			if scene.Control(element, element.Box) != nil {
				t.Fatal("paragraph has no interactive ancestor overlapping itself except containers")
			}
		}
	}
}

func TestParseRejectsInconsistentSnapshots(t *testing.T) {
	if _, err := Parse(json.RawMessage(`{"documents":[],"strings":[]}`), Viewport{Width: 10, Height: 10}); err == nil {
		t.Fatal("empty snapshot accepted")
	}
	if _, err := Parse(json.RawMessage(`{"documents":[{"nodes":{"backendNodeId":[1],"nodeName":[],"nodeType":[1],"parentIndex":[-1],"attributes":[[]]},"layout":{"nodeIndex":[],"bounds":[]}}],"strings":[]}`), Viewport{Width: 10, Height: 10}); err == nil {
		t.Fatal("inconsistent arrays accepted")
	}
}

func TestLabelFallsBackToDescendantText(t *testing.T) {
	scene, err := Parse(testSnapshot(), Viewport{Width: 1000, Height: 800, ScrollY: 100, ScrollWidth: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, element := range scene.Elements {
		switch element.Tag {
		case "A":
			if scene.Label(element, 80) != "Save" {
				t.Fatalf("anchor should be named by its span text: %q", scene.Label(element, 80))
			}
		case "SPAN":
			if scene.Label(element, 2) != "Sa" {
				t.Fatalf("label must clip: %q", scene.Label(element, 2))
			}
		}
	}
}

func TestControlStopsAtLargeContainersAndClimbsThroughWrappers(t *testing.T) {
	// BODY > DIV(clickable, huge) > SPAN(no layout) > A(href) > SPAN "Save"
	wire := map[string]any{
		"strings": []string{"HTML", "BODY", "DIV", "SPAN", "A", "href", "/save", "Save", "#text"},
		"documents": []map[string]any{{
			"scrollOffsetX": 0, "scrollOffsetY": 0, "contentWidth": 1000,
			"nodes": map[string]any{
				"parentIndex":   []int{-1, 0, 1, 2, 3, 4, 5},
				"nodeType":      []int{1, 1, 1, 1, 1, 1, 3},
				"nodeName":      []int{0, 1, 2, 3, 4, 3, 8},
				"nodeValue":     []int{-1, -1, -1, -1, -1, -1, 7},
				"backendNodeId": []int64{1, 2, 3, 4, 5, 6, 7},
				"attributes":    [][]int{{}, {}, {}, {}, {5, 6}, {}, {}},
				"isClickable":   map[string]any{"index": []int{2, 4}},
			},
			"layout": map[string]any{
				"nodeIndex": []int{0, 1, 2, 4, 5},
				"bounds":    [][]float64{{0, 0, 1000, 800}, {0, 0, 1000, 800}, {0, 0, 1000, 800}, {100, 100, 200, 50}, {105, 105, 150, 40}},
			},
		}},
	}
	data, _ := json.Marshal(wire)
	scene, err := Parse(json.RawMessage(data), Viewport{Width: 1000, Height: 800, ScrollWidth: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var inner Element
	for _, element := range scene.Elements {
		if element.Tag == "SPAN" {
			inner = element
		}
	}
	box := Rect{X: 105, Y: 105, Width: 150, Height: 40}
	control := scene.Control(inner, box)
	if control == nil || control.Tag != "A" {
		t.Fatalf("expected the anchor through the unlaid-out wrapper: %+v", control)
	}
	// Without the anchor, the clickable page-sized div is a container, not a control.
	var para Element
	for _, element := range scene.Elements {
		if element.Tag == "DIV" {
			para = element
		}
	}
	plain := Element{Index: inner.Index, parent: para.Index, Box: inner.Box, Tag: "SPAN"}
	if scene.Control(plain, box) != nil {
		t.Fatal("a clickable container eight times larger than the box is not the control")
	}
}

func TestControlFindsLinkInsideLocatedListItem(t *testing.T) {
	// BODY > UL > LI(box matched by the model) > A(href) "Done"
	wire := map[string]any{
		"strings": []string{"HTML", "BODY", "UL", "LI", "A", "href", "/done", "Done", "#text"},
		"documents": []map[string]any{{
			"scrollOffsetX": 0, "scrollOffsetY": 0, "contentWidth": 1000,
			"nodes": map[string]any{
				"parentIndex":   []int{-1, 0, 1, 2, 3, 4},
				"nodeType":      []int{1, 1, 1, 1, 1, 3},
				"nodeName":      []int{0, 1, 2, 3, 4, 8},
				"nodeValue":     []int{-1, -1, -1, -1, -1, 7},
				"backendNodeId": []int64{1, 2, 3, 4, 5, 6},
				"attributes":    [][]int{{}, {}, {}, {}, {5, 6}, {}},
				"isClickable":   map[string]any{"index": []int{4}},
			},
			"layout": map[string]any{
				"nodeIndex": []int{0, 1, 2, 3, 4},
				"bounds":    [][]float64{{0, 0, 1000, 800}, {0, 0, 1000, 800}, {0, 0, 240, 600}, {0, 80, 240, 40}, {8, 84, 100, 32}},
			},
		}},
	}
	data, _ := json.Marshal(wire)
	scene, err := Parse(json.RawMessage(data), Viewport{Width: 1000, Height: 800, ScrollWidth: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var item Element
	for _, element := range scene.Elements {
		if element.Tag == "LI" {
			item = element
		}
	}
	control := scene.Control(item, Rect{X: 0, Y: 80, Width: 240, Height: 40})
	if control == nil || control.Tag != "A" || scene.Label(*control, 80) != "Done" {
		t.Fatalf("expected the link inside the list item: %+v", control)
	}
}

func TestControlPrefersTighterDescendantOverClickableAncestor(t *testing.T) {
	// BODY > HEADER(clickable, wide) > DIV(matched wrapper) > BUTTON "Search"
	wire := map[string]any{
		"strings": []string{"HTML", "BODY", "HEADER", "DIV", "BUTTON", "Search", "#text"},
		"documents": []map[string]any{{
			"scrollOffsetX": 0, "scrollOffsetY": 0, "contentWidth": 1000,
			"nodes": map[string]any{
				"parentIndex":   []int{-1, 0, 1, 2, 3, 4},
				"nodeType":      []int{1, 1, 1, 1, 1, 3},
				"nodeName":      []int{0, 1, 2, 3, 4, 6},
				"nodeValue":     []int{-1, -1, -1, -1, -1, 5},
				"backendNodeId": []int64{1, 2, 3, 4, 5, 6},
				"attributes":    [][]int{{}, {}, {}, {}, {}, {}},
				"isClickable":   map[string]any{"index": []int{2, 4}},
			},
			"layout": map[string]any{
				"nodeIndex": []int{0, 1, 2, 3, 4},
				"bounds":    [][]float64{{0, 0, 1000, 800}, {0, 0, 1000, 800}, {0, 0, 1000, 64}, {300, 16, 320, 32}, {304, 18, 300, 28}},
			},
		}},
	}
	data, _ := json.Marshal(wire)
	scene, err := Parse(json.RawMessage(data), Viewport{Width: 1000, Height: 800, ScrollWidth: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var wrapper Element
	for _, element := range scene.Elements {
		if element.Tag == "DIV" {
			wrapper = element
		}
	}
	control := scene.Control(wrapper, Rect{X: 300, Y: 16, Width: 320, Height: 32})
	if control == nil || control.Tag != "BUTTON" {
		t.Fatalf("expected the button inside the wrapper, not the clickable header: %+v", control)
	}
}
