// Package visual maps a model's image-relative box onto rendered DOM geometry.
// It reads one DOMSnapshot.captureSnapshot result and the viewport the
// screenshot covered; it has no browser, model, or action surface.
package visual

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// Rect is a viewport-relative rectangle in CSS pixels.
type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

func (r Rect) area() float64 { return math.Max(0, r.Width) * math.Max(0, r.Height) }

func (r Rect) intersect(o Rect) Rect {
	x1, y1 := math.Max(r.X, o.X), math.Max(r.Y, o.Y)
	x2, y2 := math.Min(r.X+r.Width, o.X+o.Width), math.Min(r.Y+r.Height, o.Y+o.Height)
	if x2 <= x1 || y2 <= y1 {
		return Rect{}
	}
	return Rect{X: x1, Y: y1, Width: x2 - x1, Height: y2 - y1}
}

// IoU is the intersection over union of two rectangles, 0 when either is empty.
func IoU(a, b Rect) float64 {
	inter := a.intersect(b).area()
	union := a.area() + b.area() - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}

// Viewport describes the screenshot's CSS-pixel frame on the page.
type Viewport struct {
	Width       float64
	Height      float64
	ScrollX     float64
	ScrollY     float64
	ScrollWidth float64 // document.documentElement.scrollWidth, for the snapshot scale
}

// Element is one laid-out element of the top document.
type Element struct {
	Index         int               `json:"index"`
	BackendNodeID int64             `json:"backend_dom_node_id"`
	Tag           string            `json:"tag"`
	Attributes    map[string]string `json:"attributes,omitempty"`
	Text          string            `json:"text,omitempty"`
	Clickable     bool              `json:"clickable"`
	Box           Rect              `json:"box"`
	parent        int
}

// Scene is the geometry of one document relative to the screenshot viewport.
type Scene struct {
	Elements      []Element
	Scale         float64 // snapshot units per CSS pixel (1 or the device scale)
	Documents     int
	LayoutNodes   int
	TotalElements int
	Skipped       int // elements without area or outside the viewport
	children      map[int][]int
	textByNode    map[int]string
	parentOf      []int
}

var keptAttributes = map[string]bool{"id": true, "role": true, "aria-label": true, "name": true, "type": true, "placeholder": true, "alt": true, "title": true, "aria-labelledby": true, "href": true, "value": true}
var interactiveTags = map[string]bool{"A": true, "BUTTON": true, "INPUT": true, "SELECT": true, "TEXTAREA": true, "SUMMARY": true, "LABEL": true, "OPTION": true}
var interactiveRoles = map[string]bool{"button": true, "link": true, "checkbox": true, "radio": true, "tab": true, "menuitem": true, "menuitemcheckbox": true, "menuitemradio": true, "option": true, "switch": true, "textbox": true, "searchbox": true, "combobox": true, "slider": true, "spinbutton": true, "treeitem": true}
var skippedTags = map[string]bool{"HTML": true, "BODY": true, "HEAD": true, "SCRIPT": true, "STYLE": true, "NOSCRIPT": true, "TEMPLATE": true, "META": true, "LINK": true}

const maxTextBytes = 160

type snapshotDocument struct {
	ScrollOffsetX float64 `json:"scrollOffsetX"`
	ScrollOffsetY float64 `json:"scrollOffsetY"`
	ContentWidth  float64 `json:"contentWidth"`
	Nodes         struct {
		ParentIndex   []int   `json:"parentIndex"`
		NodeType      []int   `json:"nodeType"`
		NodeName      []int   `json:"nodeName"`
		NodeValue     []int   `json:"nodeValue"`
		BackendNodeID []int64 `json:"backendNodeId"`
		Attributes    [][]int `json:"attributes"`
		IsClickable   struct {
			Index []int `json:"index"`
		} `json:"isClickable"`
	} `json:"nodes"`
	Layout struct {
		NodeIndex []int       `json:"nodeIndex"`
		Bounds    [][]float64 `json:"bounds"`
	} `json:"layout"`
}

// Parse converts the top document of a DOMSnapshot result into viewport
// geometry. Snapshot units may be device pixels on some Chromium builds, so the
// scale is derived from the document's content width when it is available.
func Parse(snapshot json.RawMessage, viewport Viewport) (Scene, error) {
	var wire struct {
		Documents []snapshotDocument `json:"documents"`
		Strings   []string           `json:"strings"`
	}
	if err := json.Unmarshal(snapshot, &wire); err != nil || len(wire.Documents) == 0 {
		return Scene{}, errors.New("browser returned no DOM snapshot document")
	}
	doc := wire.Documents[0]
	nodes := doc.Nodes
	count := len(nodes.BackendNodeID)
	if count == 0 || len(nodes.NodeName) != count || len(nodes.NodeType) != count || len(nodes.ParentIndex) != count || len(nodes.Attributes) != count || len(doc.Layout.NodeIndex) != len(doc.Layout.Bounds) {
		return Scene{}, errors.New("browser returned an inconsistent DOM snapshot")
	}
	str := func(index int) string {
		if index < 0 || index >= len(wire.Strings) {
			return ""
		}
		return wire.Strings[index]
	}
	scene := Scene{Scale: 1, Documents: len(wire.Documents), LayoutNodes: len(doc.Layout.NodeIndex), children: map[int][]int{}, textByNode: map[int]string{}, parentOf: nodes.ParentIndex}
	for index := 0; index < count; index++ {
		if parent := nodes.ParentIndex[index]; parent >= 0 && parent < count {
			scene.children[parent] = append(scene.children[parent], index)
		}
	}
	if viewport.ScrollWidth > 0 && doc.ContentWidth > 0 {
		if ratio := doc.ContentWidth / viewport.ScrollWidth; ratio >= 0.5 && ratio <= 4 && math.Abs(ratio-math.Round(ratio)) < 0.05 {
			scene.Scale = math.Round(ratio)
		}
	}
	clickable := map[int]bool{}
	for _, index := range nodes.IsClickable.Index {
		clickable[index] = true
	}
	// Own text: direct text-node children, bounded.
	ownText := make([]string, count)
	for index := 0; index < count; index++ {
		if nodes.NodeType[index] != 3 {
			continue
		}
		parent := nodes.ParentIndex[index]
		if parent < 0 || parent >= count || len(ownText[parent]) >= maxTextBytes {
			continue
		}
		value := strings.Join(strings.Fields(str(nodes.NodeValue[index])), " ")
		if value == "" {
			continue
		}
		if ownText[parent] != "" {
			ownText[parent] += " "
		}
		ownText[parent] += value
		if len(ownText[parent]) > maxTextBytes {
			ownText[parent] = ownText[parent][:maxTextBytes]
		}
	}
	for index, value := range ownText {
		if value != "" {
			scene.textByNode[index] = value
		}
	}
	byNode := map[int]*Element{}
	for layoutIndex, nodeIndex := range doc.Layout.NodeIndex {
		if nodeIndex < 0 || nodeIndex >= count || nodes.NodeType[nodeIndex] != 1 {
			continue
		}
		tag := strings.ToUpper(str(nodes.NodeName[nodeIndex]))
		if skippedTags[tag] {
			continue
		}
		scene.TotalElements++
		bounds := doc.Layout.Bounds[layoutIndex]
		if len(bounds) != 4 {
			scene.Skipped++
			continue
		}
		box := Rect{X: (bounds[0] - doc.ScrollOffsetX) / scene.Scale, Y: (bounds[1] - doc.ScrollOffsetY) / scene.Scale, Width: bounds[2] / scene.Scale, Height: bounds[3] / scene.Scale}
		frame := Rect{Width: viewport.Width, Height: viewport.Height}
		if box.Width < 1 || box.Height < 1 || box.intersect(frame).area() <= 0 {
			scene.Skipped++
			continue
		}
		if existing, seen := byNode[nodeIndex]; seen {
			// A node with several layout boxes (inline wraps) keeps the union.
			x1, y1 := math.Min(existing.Box.X, box.X), math.Min(existing.Box.Y, box.Y)
			x2 := math.Max(existing.Box.X+existing.Box.Width, box.X+box.Width)
			y2 := math.Max(existing.Box.Y+existing.Box.Height, box.Y+box.Height)
			existing.Box = Rect{X: x1, Y: y1, Width: x2 - x1, Height: y2 - y1}
			continue
		}
		attributes := map[string]string{}
		pairs := nodes.Attributes[nodeIndex]
		for i := 0; i+1 < len(pairs); i += 2 {
			name := strings.ToLower(str(pairs[i]))
			if !keptAttributes[name] {
				continue
			}
			value := strings.TrimSpace(str(pairs[i+1]))
			if len(value) > 200 {
				value = value[:200]
			}
			if value != "" {
				attributes[name] = value
			}
		}
		element := &Element{Index: nodeIndex, BackendNodeID: nodes.BackendNodeID[nodeIndex], Tag: tag, Attributes: attributes, Text: ownText[nodeIndex], Clickable: clickable[nodeIndex], Box: box, parent: nodes.ParentIndex[nodeIndex]}
		if len(element.Attributes) == 0 {
			element.Attributes = nil
		}
		byNode[nodeIndex] = element
	}
	scene.Elements = make([]Element, 0, len(byNode))
	for _, element := range byNode {
		scene.Elements = append(scene.Elements, *element)
	}
	sort.Slice(scene.Elements, func(i, j int) bool { return scene.Elements[i].Index < scene.Elements[j].Index })
	return scene, nil
}

// Interactive reports whether an element is a control by tag, role or click
// handler evidence from the snapshot.
func (e Element) Interactive() bool {
	if e.Clickable || interactiveTags[e.Tag] {
		return true
	}
	return interactiveRoles[strings.ToLower(e.Attributes["role"])]
}

// Match is one element compared with the model's box.
type Match struct {
	Element  Element `json:"element"`
	IoU      float64 `json:"iou"`
	Coverage float64 `json:"coverage"` // share of the model box inside the element
}

// Rank orders elements by overlap with box. Coverage breaks IoU ties so that a
// slightly smaller element that the box clearly indicates wins over a container.
func (s Scene) Rank(box Rect, limit int) []Match {
	matches := []Match{}
	for _, element := range s.Elements {
		inter := element.Box.intersect(box).area()
		if inter <= 0 {
			continue
		}
		coverage := 0.0
		if box.area() > 0 {
			coverage = inter / box.area()
		}
		matches = append(matches, Match{Element: element, IoU: IoU(element.Box, box), Coverage: coverage})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if math.Abs(matches[i].IoU-matches[j].IoU) > 1e-9 {
			return matches[i].IoU > matches[j].IoU
		}
		if math.Abs(matches[i].Coverage-matches[j].Coverage) > 1e-9 {
			return matches[i].Coverage > matches[j].Coverage
		}
		return matches[i].Element.Box.area() < matches[j].Element.Box.area()
	})
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

// maxControlAreaRatio bounds how much larger than the model box an interactive
// ancestor may be before it is a layout container rather than the control. A
// wide button around a short label is about 6 to 10 times the label's box; a
// page header or sidebar is 20 times or more.
const maxControlAreaRatio = 12.0

// Control returns the interactive element that best explains the box: the
// element itself when interactive, otherwise whichever of the best-overlapping
// interactive descendant (a button inside a matched wrapper, a link inside a
// matched list item) and the nearest interactive ancestor overlaps the box
// more. Large containers are never controls; nil means none was plausible.
func (s Scene) Control(element Element, box Rect) *Element {
	index := map[int]Element{}
	for _, candidate := range s.Elements {
		index[candidate.Index] = candidate
	}
	if element.Interactive() {
		found := element
		return &found
	}
	below, above := s.controlBelow(index, element, box), s.controlAbove(index, element, box)
	switch {
	case below == nil:
		return above
	case above == nil:
		return below
	case IoU(below.Box, box) >= IoU(above.Box, box):
		return below
	}
	return above
}

func (s Scene) controlBelow(index map[int]Element, element Element, box Rect) *Element {
	var best *Element
	bestScore := 0.0
	pending := append([]int{}, s.children[element.Index]...)
	for steps := 0; len(pending) > 0 && steps < 2048; steps++ {
		childIndex := pending[0]
		pending = pending[1:]
		pending = append(pending, s.children[childIndex]...)
		child, laidOut := index[childIndex]
		if !laidOut || !child.Interactive() {
			continue
		}
		if score := IoU(child.Box, box); score > bestScore {
			found := child
			best, bestScore = &found, score
		}
	}
	return best
}

func (s Scene) controlAbove(index map[int]Element, element Element, box Rect) *Element {
	limit := math.Max(box.area(), 1) * maxControlAreaRatio
	current := element
	for depth := 0; depth < 64; depth++ {
		if current.Interactive() {
			if current.Box.area() > limit && current.Index != element.Index {
				return nil
			}
			found := current
			return &found
		}
		parent := current.parent
		if parent < 0 || parent >= len(s.parentOf) {
			return nil
		}
		next, laidOut := index[parent]
		if !laidOut {
			// Climb through a wrapper without geometry; keep the child's box for
			// the overlap test so an invisible ancestor cannot hide a real control.
			next = Element{Index: parent, parent: s.parentOf[parent], Box: current.Box, Attributes: nil}
		} else if next.Box.intersect(box).area() <= 0 {
			return nil
		}
		current = next
	}
	return nil
}

// Label names an element for a reader: its own text, else the first text found
// in its descendants in document order, bounded. Interactive containers such as
// a clickable div around an icon and a span usually have no text of their own.
func (s Scene) Label(element Element, limit int) string {
	if limit <= 0 {
		limit = 80
	}
	if element.Text != "" {
		return clipRunes(element.Text, limit)
	}
	var parts []string
	size := 0
	pending := append([]int{}, s.children[element.Index]...)
	for steps := 0; len(pending) > 0 && steps < 512 && size < limit; steps++ {
		index := pending[0]
		pending = pending[1:]
		if text := s.textByNode[index]; text != "" {
			parts = append(parts, text)
			size += len(text) + 1
		}
		pending = append(pending, s.children[index]...)
	}
	return clipRunes(strings.Join(parts, " "), limit)
}

func clipRunes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
