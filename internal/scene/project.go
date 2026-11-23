package scene

import (
	"fmt"
	"strconv"
	"strings"
)

// View selects what Project writes. Mode viewport (default) lists records that
// intersect the viewport first; page lists every rendered record in order;
// records also includes net_only records the DOM never rendered. Since skips
// records whose Order is below it, for paging through long threads.
type View struct {
	Mode     string
	MaxBytes int
	Since    int
}

type Projection struct {
	Lines     []string
	Records   []Record
	Truncated bool
}

const (
	DefaultViewBytes = 3072
	MaxLineBytes     = 250
	// Room kept for the trailing "offscreen: N records" line.
	trailerReserve = 40
	shortNameBytes = 24
)

// Project writes one line per record, byte-budgeted, in the agent view format:
//
//	r_t1_abc d1 @alice 4d s41 'first 120 B of text' [reply n_9c1e0a.18801 | up n_9c1e0a.18802]
//
// Quotes are folded into their parent's line; the view ends with the number
// of records it did not show.
func (g *Graph) Project(v View) Projection {
	if v.MaxBytes <= 0 {
		v.MaxBytes = DefaultViewBytes
	}
	if v.Mode == "" {
		v.Mode = "viewport"
	}
	quotes := map[string][]Record{}
	for _, r := range g.Records {
		if r.Kind == "quote" && r.Parent != "" {
			quotes[r.Parent] = append(quotes[r.Parent], r)
		}
	}
	var inView, rest []Record
	for _, r := range g.Records {
		if r.Kind == "quote" || r.Order < v.Since {
			continue
		}
		if r.NetOnly && v.Mode != "records" {
			continue
		}
		if v.Mode == "viewport" && !r.NetOnly && r.Box.Intersects(g.Viewport) {
			inView = append(inView, r)
		} else {
			rest = append(rest, r)
		}
	}
	ordered := append(inView, rest...)

	p := Projection{}
	used := 0
	for i, r := range ordered {
		line := g.line(r, quotes[r.R])
		if used+len(line)+1 > v.MaxBytes-trailerReserve {
			p.Truncated = true
			p.Lines = append(p.Lines, fmt.Sprintf("offscreen: %d records", len(ordered)-i))
			return p
		}
		used += len(line) + 1
		p.Lines = append(p.Lines, line)
		p.Records = append(p.Records, r)
		p.Records = append(p.Records, quotes[r.R]...)
	}
	p.Lines = append(p.Lines, "offscreen: 0 records")
	return p
}

// shortName keeps the action word of an accessible name: X labels read
// "26 Replies. Reply", where the last sentence is the verb.
func shortName(c Control) string {
	name := c.Name
	if i := strings.LastIndex(name, ". "); i >= 0 && i+2 < len(name) {
		name = name[i+2:]
	}
	name = strings.ToLower(strings.TrimRight(name, "."))
	if name == "" {
		name = c.Role
	}
	return cutBytes(name, shortNameBytes)
}

func quoteText(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}

// lineOrder puts actions before navigation: buttons and widgets first, links
// last, each group in document order, so a tight line keeps reply and like
// over the author and timestamp links the JSON form still carries.
func lineOrder(cs []Control) []Control {
	out := make([]Control, 0, len(cs))
	for _, c := range cs {
		if c.Role != "link" {
			out = append(out, c)
		}
	}
	for _, c := range cs {
		if c.Role == "link" {
			out = append(out, c)
		}
	}
	return out
}

// line shrinks in stages until it fits MaxLineBytes: the summary down to 40
// bytes, then the quote folds to its handle and open control, then controls
// go from the end with a +N count while those 40 bytes stay, because the
// words are how the agent picks the record and the JSON form keeps every
// control the line dropped. Prose goes entirely only as a last resort.
func (g *Graph) line(r Record, quotes []Record) string {
	controls := lineOrder(visibleControls(r))
	render := func(summaryMax, quoteMax int, quoteControls bool, keep int) string {
		var b strings.Builder
		b.WriteString(r.R)
		b.WriteString(" d")
		b.WriteString(strconv.Itoa(r.Depth))
		if r.NetOnly {
			b.WriteString(" net_only")
		}
		if r.Author != "" {
			b.WriteString(" @")
			b.WriteString(r.Author)
		}
		if r.Time != "" {
			b.WriteByte(' ')
			b.WriteString(r.Time)
		}
		if r.Metric != "" {
			b.WriteByte(' ')
			b.WriteString(r.Metric)
		}
		if summaryMax > 0 && r.Summary != "" {
			b.WriteByte(' ')
			b.WriteString(quoteText(cutBytes(r.Summary, summaryMax)))
		}
		for _, q := range quotes {
			b.WriteString(" [quote ")
			b.WriteString(q.R)
			if q.Author != "" {
				b.WriteString(" @")
				b.WriteString(q.Author)
			}
			if quoteMax > 0 && q.Summary != "" {
				b.WriteByte(' ')
				b.WriteString(quoteText(cutBytes(q.Summary, quoteMax)))
			}
			for _, c := range visibleControls(q) {
				// Folded, a quote keeps only the control that opens it.
				if !quoteControls && c.NameSrc != "container" {
					continue
				}
				b.WriteByte(' ')
				b.WriteString(shortName(c))
				b.WriteByte(' ')
				b.WriteString(c.H)
			}
			b.WriteByte(']')
		}
		if len(controls) > 0 {
			b.WriteString(" [")
			for i, c := range controls {
				if i == keep {
					fmt.Fprintf(&b, " | +%d", len(controls)-keep)
					break
				}
				if i > 0 {
					b.WriteString(" | ")
				}
				b.WriteString(shortName(c))
				b.WriteByte(' ')
				b.WriteString(c.H)
			}
			b.WriteByte(']')
		}
		return b.String()
	}
	stages := []struct {
		summary, quote int
		quoteControls  bool
	}{{summaryBytes, summaryBytes, true}, {80, 60, true}, {40, 40, true}, {40, 0, false}}
	for _, stage := range stages {
		if s := render(stage.summary, stage.quote, stage.quoteControls, len(controls)); len(s) <= MaxLineBytes {
			return s
		}
	}
	for keep := len(controls) - 1; keep >= 1; keep-- {
		if s := render(40, 0, false, keep); len(s) <= MaxLineBytes {
			return s
		}
	}
	if s := render(0, 0, false, 1); len(s) <= MaxLineBytes {
		return s
	}
	return cutBytes(render(0, 0, false, 0), MaxLineBytes)
}

func visibleControls(r Record) []Control {
	out := make([]Control, 0, len(r.Controls))
	for _, c := range r.Controls {
		if c.Vis != "hidden" {
			out = append(out, c)
		}
	}
	return out
}
