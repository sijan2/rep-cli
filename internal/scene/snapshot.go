// Package scene builds the G2 scene graph: records (comments, tweets, quotes)
// with the controls they own, from one reduced DOMSnapshot produced by the
// extension's browser.geometry RPC. It is pure: no I/O, no CDP, so every
// decision can be replayed against saved fixtures.
package scene

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Box is a CSS-pixel rectangle in document coordinates (scroll included), the
// same frame DOMSnapshot reports layout bounds in.
type Box struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Empty reports a box that cannot be seen or hit.
func (b Box) Empty() bool { return b.W <= 0 || b.H <= 0 }

func (b Box) Right() float64  { return b.X + b.W }
func (b Box) Bottom() float64 { return b.Y + b.H }

func (b Box) Intersects(o Box) bool {
	return b.X < o.Right() && b.Right() > o.X && b.Y < o.Bottom() && b.Bottom() > o.Y
}

type Size struct {
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Columns is the index-aligned node table of a reduced snapshot. A nil Attrs,
// Box or Style entry is the wire's null: no attributes kept, no layout.
type Columns struct {
	Parent  []int         `json:"parent"`
	Backend []int         `json:"backend"`
	Tag     []string      `json:"tag"`
	Role    []string      `json:"role"`
	Attrs   [][][2]string `json:"attrs"`
	Box     [][]float64   `json:"box"`
	Paint   []int         `json:"paint"`
	Style   [][]string    `json:"style"`
	Text    []string      `json:"text"`
}

// Snapshot is the browser.geometry result. Nodes are in document (pre-order)
// order with parents before children; shadow trees are already flattened.
type Snapshot struct {
	FrameID     string  `json:"frame_id"`
	LoaderID    string  `json:"loader_id"`
	URL         string  `json:"url"`
	Scale       float64 `json:"scale"`
	Viewport    Box     `json:"viewport"`
	Content     Size    `json:"content"`
	Nodes       Columns `json:"nodes"`
	DroppedText int     `json:"dropped_text,omitempty"`
}

// Decode parses a reduced snapshot and checks the columnar invariants the
// builder relies on.
func Decode(data []byte) (Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return Snapshot{}, fmt.Errorf("scene: decode snapshot: %w", err)
	}
	if err := s.Validate(); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

// Len is the node count.
func (s Snapshot) Len() int { return len(s.Nodes.Parent) }

// Validate rejects snapshots whose columns disagree, because every later
// index lookup would silently read a neighbouring node.
func (s Snapshot) Validate() error {
	n := s.Len()
	c := s.Nodes
	for name, got := range map[string]int{
		"backend": len(c.Backend), "tag": len(c.Tag), "role": len(c.Role), "attrs": len(c.Attrs),
		"box": len(c.Box), "paint": len(c.Paint), "style": len(c.Style), "text": len(c.Text),
	} {
		if got != n {
			return fmt.Errorf("scene: column %s has %d entries, parent has %d", name, got, n)
		}
	}
	for i, p := range c.Parent {
		if p < -1 || p >= i {
			return fmt.Errorf("scene: node %d has parent %d; parents must precede children", i, p)
		}
		if i > 0 && p == -1 {
			return fmt.Errorf("scene: node %d is a second root", i)
		}
		if b := c.Box[i]; b != nil && len(b) != 4 {
			return fmt.Errorf("scene: node %d box has %d values", i, len(b))
		}
		if st := c.Style[i]; st != nil && len(st) < 5 {
			return fmt.Errorf("scene: node %d style has %d values", i, len(st))
		}
	}
	if s.FrameID == "" || s.LoaderID == "" {
		return errors.New("scene: snapshot lacks frame_id or loader_id")
	}
	return nil
}
