package scene

import (
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/repplus/rep-cli/internal/records"
)

// Keys carries the policy inputs the builder needs. It stands in for
// *sitepolicy.Policy, which is being written concurrently; the integrator maps
// a policy onto Keys so this package never imports sitepolicy. A zero Keys
// makes Build infer the site from the snapshot URL.
type Keys struct {
	// Site selects the segmentation rules: "reddit", "x", or anything else
	// for a generic tag+attribute container.
	Site string
	// RecordTag is the container element: shreddit-comment, article.
	RecordTag string
	// RecordKeyAttr is the attribute carrying the key (thingid). Empty on X,
	// where the key is the status id of the permalink anchor wrapping <time>.
	RecordKeyAttr string
}

var (
	RedditKeys = Keys{Site: "reddit", RecordTag: "shreddit-comment", RecordKeyAttr: "thingid"}
	XKeys      = Keys{Site: "x", RecordTag: "article"}
)

// KeysForURL picks the policy keys from the page host; unknown hosts get a
// zero Keys and build a graph of orphan controls only.
func KeysForURL(raw string) Keys {
	u, err := url.Parse(raw)
	if err != nil {
		return Keys{}
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "reddit.com" || strings.HasSuffix(host, ".reddit.com"):
		return RedditKeys
	case host == "x.com" || strings.HasSuffix(host, ".x.com") || host == "twitter.com" || strings.HasSuffix(host, ".twitter.com"):
		return XKeys
	}
	return Keys{}
}

type Input struct {
	Snapshot Snapshot
	Keys     Keys
	// Network records (from `rep records`) give reading order and the
	// virtualized or collapsed records the DOM does not show. May be nil.
	Network []records.Record
}

type Record struct {
	R      string
	Key    string
	Kind   string // comment|post|tweet|quote|item
	Parent string // R of the enclosing record, "" at the top
	Depth  int
	Order  int
	Box    Box
	Author string
	Time   string
	// Summary is the first 120 bytes of the record's own text.
	Summary string
	// Metric is a compact engagement token for the projected line (s41,
	// likes=120). Not in the original contract; added so the line format
	// `r_t1_abc d1 @alice 4d s41 '...'` has a source for s41.
	Metric   string
	Controls []Control
	Src      string // network|dom|both
	NetOnly  bool
}

type Control struct {
	H       string
	Role    string
	Name    string
	NameSrc string
	Owner   string // R of the owning record, "" for orphans
	Rel     string // contains|continues
	Box     Box
	Vis     string // in_viewport|offscreen|hidden
	Backend int
}

type Graph struct {
	Frame6   string
	URL      string
	Viewport Box
	Records  []Record
	Orphans  []Control
	Stats    map[string]int
}

const (
	summaryBytes = 120
	nameBytes    = 120
	// Fallback for the continues test when a page has fewer than two record
	// gaps to take a median of: about one line of UI.
	defaultGap = 48.0
)

var (
	interactiveRoles = map[string]bool{"button": true, "link": true, "menuitem": true, "tab": true, "checkbox": true, "switch": true, "option": true, "radio": true, "combobox": true}
	statusHref       = regexp.MustCompile(`/status/([0-9]+)`)
	userHref         = regexp.MustCompile(`^/(?:user|u)/([^/?#]+)`)
	handleText       = regexp.MustCompile(`^@[A-Za-z0-9_]{1,15}$`)
	leadingCount     = regexp.MustCompile(`^([0-9][0-9.,]*[KkMm]?)\s+(?i:likes?)\b`)
	plainInt         = regexp.MustCompile(`^-?[0-9]+$`)
	spaces           = regexp.MustCompile(`\s+`)
)

type node struct {
	tag, role string
	attrs     map[string]string
	box       *Box
	style     []string
	text      string
	parent    int
	end       int // last index of the subtree; nodes are in pre-order
	backend   int
	container int // index into the container list, -1 otherwise
}

type tree struct {
	nodes    []node
	viewport Box
}

type container struct {
	node   int
	key    string
	kind   string
	ns     string
	parent int // container index or -1
	depth  int
}

func newTree(s Snapshot) *tree {
	n := s.Len()
	t := &tree{nodes: make([]node, n), viewport: s.Viewport}
	c := s.Nodes
	for i := 0; i < n; i++ {
		nd := node{tag: c.Tag[i], role: c.Role[i], text: c.Text[i], parent: c.Parent[i], end: i, backend: c.Backend[i], container: -1, style: c.Style[i]}
		if len(c.Attrs[i]) > 0 {
			nd.attrs = make(map[string]string, len(c.Attrs[i]))
			for _, kv := range c.Attrs[i] {
				nd.attrs[kv[0]] = kv[1]
			}
		}
		if b := c.Box[i]; b != nil {
			nd.box = &Box{X: b[0], Y: b[1], W: b[2], H: b[3]}
		}
		t.nodes[i] = nd
	}
	for i := n - 1; i > 0; i-- {
		if p := t.nodes[i].parent; t.nodes[i].end > t.nodes[p].end {
			t.nodes[p].end = t.nodes[i].end
		}
	}
	return t
}

func (t *tree) attr(i int, name string) string { return t.nodes[i].attrs[name] }

// hidden is true when the node cannot be perceived: no layout, hidden styles,
// or an empty box. Keyed nodes survive reduction in this state on purpose.
func (t *tree) hidden(i int) bool {
	nd := &t.nodes[i]
	if nd.box == nil || nd.box.Empty() {
		return true
	}
	if st := nd.style; st != nil {
		if st[0] == "none" || st[1] == "hidden" || st[1] == "collapse" {
			return true
		}
		if o, err := strconv.ParseFloat(st[2], 64); err == nil && o == 0 {
			return true
		}
	}
	return false
}

func (t *tree) vis(i int) string {
	if t.hidden(i) {
		return "hidden"
	}
	if t.nodes[i].box.Intersects(t.viewport) {
		return "in_viewport"
	}
	return "offscreen"
}

// walk visits the subtree below i in document order; skip(j) true jumps over
// j's whole subtree, which is how nested records are carved out of their parent.
func (t *tree) walk(i int, skip func(j int) bool, visit func(j int)) {
	for j := i + 1; j <= t.nodes[i].end; j++ {
		if skip != nil && skip(j) {
			j = t.nodes[j].end
			continue
		}
		visit(j)
	}
}

func (t *tree) text(i int, skip func(j int) bool) string {
	var parts []string
	t.walk(i, skip, func(j int) {
		if t.nodes[j].tag == "#text" && t.nodes[j].text != "" {
			parts = append(parts, t.nodes[j].text)
		}
	})
	return collapse(strings.Join(parts, " "))
}

func (t *tree) first(i int, skip func(j int) bool, want func(j int) bool) int {
	found := -1
	t.walk(i, func(j int) bool { return found >= 0 || (skip != nil && skip(j)) }, func(j int) {
		if found < 0 && want(j) {
			found = j
		}
	})
	return found
}

func (t *tree) skipContainers(j int) bool { return t.nodes[j].container >= 0 }

// nearestContainer is the innermost record container that is j or encloses it.
func (t *tree) nearestContainer(j int) int {
	for p := j; p >= 0; p = t.nodes[p].parent {
		if t.nodes[p].container >= 0 {
			return t.nodes[p].container
		}
	}
	return -1
}

func collapse(s string) string { return strings.TrimSpace(spaces.ReplaceAllString(s, " ")) }

// cutBytes truncates on a rune boundary so summaries stay valid UTF-8.
func cutBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	i := max
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}

// partOfControl is true when an ancestor is already a control: the spans and
// icons inside a button inherit its pointer cursor but are not controls.
func (t *tree) partOfControl(i int, isControl map[int]bool) bool {
	for p := t.nodes[i].parent; p >= 0; p = t.nodes[p].parent {
		if isControl[p] {
			return true
		}
	}
	return false
}

// inheritedCursor is the cursor of the nearest laid-out ancestor, so a pointer
// cursor is attributed to the element where it starts.
func (t *tree) inheritedCursor(i int) string {
	for p := t.nodes[i].parent; p >= 0; p = t.nodes[p].parent {
		if st := t.nodes[p].style; st != nil {
			return st[4]
		}
	}
	return ""
}

// interactiveRole decides whether a node is a control and which role it plays.
// Explicit roles win; tags imply their native role; focusability and a pointer
// cursor are the last resort because they are how custom widgets advertise,
// and they only count outside an existing control.
func (t *tree) interactiveRole(i int, isControl map[int]bool) (string, bool) {
	nd := &t.nodes[i]
	explicit := nd.role
	pick := func(implicit string) string {
		if explicit != "" {
			return explicit
		}
		return implicit
	}
	switch nd.tag {
	case "a":
		if nd.attrs["href"] != "" {
			return pick("link"), true
		}
	case "button", "summary":
		return pick("button"), true
	case "input":
		switch strings.ToLower(nd.attrs["type"]) {
		case "hidden":
			return "", false
		case "checkbox":
			return pick("checkbox"), true
		case "radio":
			return pick("radio"), true
		case "submit", "button", "reset", "image":
			return pick("button"), true
		case "range":
			return pick("slider"), true
		default:
			return pick("textbox"), true
		}
	case "select":
		return pick("combobox"), true
	case "textarea":
		return pick("textbox"), true
	}
	if interactiveRoles[explicit] {
		return explicit, true
	}
	if t.partOfControl(i, isControl) {
		return "", false
	}
	if ti, err := strconv.Atoi(strings.TrimSpace(nd.attrs["tabindex"])); err == nil && ti >= 0 {
		return pick("widget"), true
	}
	if st := nd.style; st != nil && st[4] == "pointer" && t.inheritedCursor(i) != "pointer" {
		return pick("pointer"), true
	}
	return "", false
}

// name follows the accessible-name precedence from the design: aria-label >
// aria-labelledby > text > title > img alt > svg title.
func (t *tree) name(i int, byID map[string]int) (string, string) {
	nd := &t.nodes[i]
	if v := collapse(nd.attrs["aria-label"]); v != "" {
		return cutBytes(v, nameBytes), "aria-label"
	}
	if ids := strings.Fields(nd.attrs["aria-labelledby"]); len(ids) > 0 {
		var parts []string
		for _, id := range ids {
			if j, ok := byID[id]; ok {
				if s := t.text(j, nil); s != "" {
					parts = append(parts, s)
				}
			}
		}
		if v := collapse(strings.Join(parts, " ")); v != "" {
			return cutBytes(v, nameBytes), "aria-labelledby"
		}
	}
	// svg <title> text is excluded here so it only names icon-only controls.
	if v := t.text(i, func(j int) bool { return t.nodes[j].tag == "title" }); v != "" {
		return cutBytes(v, nameBytes), "text"
	}
	if v := collapse(nd.attrs["title"]); v != "" {
		return cutBytes(v, nameBytes), "title"
	}
	if j := t.first(i, nil, func(j int) bool { return t.nodes[j].tag == "img" && collapse(t.nodes[j].attrs["alt"]) != "" }); j >= 0 {
		return cutBytes(collapse(t.nodes[j].attrs["alt"]), nameBytes), "alt"
	}
	if j := t.first(i, nil, func(j int) bool { return t.nodes[j].tag == "title" }); j >= 0 {
		if v := t.text(j, nil); v != "" {
			return cutBytes(v, nameBytes), "svg-title"
		}
	}
	if v := collapse(nd.attrs["placeholder"]); v != "" {
		return cutBytes(v, nameBytes), "placeholder"
	}
	return "", ""
}

func redditKind(key string) string {
	switch {
	case strings.HasPrefix(key, "t1_"):
		return "comment"
	case strings.HasPrefix(key, "t3_"):
		return "post"
	}
	return "item"
}

// statusKey finds the status id of the permalink anchor that wraps a <time>
// below i, ignoring nested quote blocks, which carry their own permalink.
func (t *tree) statusKey(i int, skip func(j int) bool) string {
	j := t.first(i, skip, func(j int) bool {
		nd := &t.nodes[j]
		if nd.tag != "a" || !statusHref.MatchString(nd.attrs["href"]) {
			return false
		}
		return t.first(j, nil, func(k int) bool { return t.nodes[k].tag == "time" }) >= 0
	})
	if j < 0 {
		return ""
	}
	return statusHref.FindStringSubmatch(t.attr(j, "href"))[1]
}

// isQuoteBlock recognises X's quoted-post container: a role=link box with its
// own timestamp, or a nested article.
func (t *tree) isQuoteBlock(j int) bool {
	nd := &t.nodes[j]
	if nd.tag == "article" {
		return true
	}
	if nd.tag == "div" && nd.role == "link" {
		return t.first(j, nil, func(k int) bool { return t.nodes[k].tag == "time" }) >= 0
	}
	return false
}

func (t *tree) findContainers(keys Keys, stats map[string]int) []container {
	var out []container
	add := func(c container) {
		c.parent = -1
		if p := t.nearestContainer(t.nodes[c.node].parent); p >= 0 {
			c.parent, c.depth = p, out[p].depth+1
		}
		t.nodes[c.node].container = len(out)
		out = append(out, c)
	}
	switch {
	case keys.Site == "x":
		for i := range t.nodes {
			nd := &t.nodes[i]
			if nd.tag != "article" || nd.attrs["data-testid"] != "tweet" || t.nearestContainer(nd.parent) >= 0 {
				continue
			}
			// Quote blocks are found first so the outer key search can skip them.
			var quotes []int
			t.walk(i, func(j int) bool {
				if t.isQuoteBlock(j) {
					quotes = append(quotes, j)
					return true
				}
				return false
			}, func(int) {})
			inQuote := func(j int) bool {
				for _, q := range quotes {
					if j == q || (j > q && j <= t.nodes[q].end) {
						return true
					}
				}
				return false
			}
			key := t.statusKey(i, inQuote)
			if key == "" {
				stats["unkeyed_containers"]++
				continue
			}
			add(container{node: i, key: key, kind: "tweet", ns: "tw"})
			for n, q := range quotes {
				qkey := t.statusKey(q, nil)
				if qkey == "" {
					if j := t.first(q, nil, func(j int) bool { return t.nodes[j].tag == "a" && statusHref.MatchString(t.attr(j, "href")) }); j >= 0 {
						qkey = statusHref.FindStringSubmatch(t.attr(j, "href"))[1]
					}
				}
				if qkey == "" {
					// Without a permalink the quote is still addressable
					// through its parent.
					qkey = key + ".q" + strconv.Itoa(n+1)
				}
				add(container{node: q, key: qkey, kind: "quote", ns: "tw"})
			}
		}
	case keys.RecordTag != "" && keys.RecordKeyAttr != "":
		ns := keys.Site
		if keys.Site == "reddit" {
			ns = ""
		} else if ns == "" {
			ns = "dom"
		}
		for i := range t.nodes {
			nd := &t.nodes[i]
			if nd.tag != keys.RecordTag || nd.attrs[keys.RecordKeyAttr] == "" {
				continue
			}
			key := nd.attrs[keys.RecordKeyAttr]
			kind := "item"
			if keys.Site == "reddit" {
				kind = redditKind(key)
			}
			add(container{node: i, key: key, kind: kind, ns: ns})
		}
	}
	return out
}

// summarize reads author, time, metric and body text from a record's own
// subtree, i.e. without nested records and without the words on its controls.
func (t *tree) summarize(c container, site string, isControl map[int]bool) (author, when, metric, summary string) {
	skip := func(j int) bool { return t.skipContainers(j) }
	own := func(j int) bool { return isControl[j] || t.nodes[j].tag == "time" || t.skipContainers(j) }

	if a := t.attr(c.node, "author"); a != "" {
		author = a
	} else if site == "reddit" {
		if j := t.first(c.node, skip, func(j int) bool { return t.nodes[j].tag == "a" && userHref.MatchString(t.attr(j, "href")) }); j >= 0 {
			author = userHref.FindStringSubmatch(t.attr(j, "href"))[1]
		}
	} else {
		if j := t.first(c.node, skip, func(j int) bool { return t.nodes[j].tag == "#text" && handleText.MatchString(t.nodes[j].text) }); j >= 0 {
			author = strings.TrimPrefix(t.nodes[j].text, "@")
		}
	}

	if j := t.first(c.node, skip, func(j int) bool { return t.nodes[j].tag == "time" }); j >= 0 {
		when = strings.TrimSuffix(t.text(j, nil), " ago")
	}

	switch site {
	case "reddit":
		if s := t.attr(c.node, "score"); plainInt.MatchString(s) {
			metric = "s" + s
		} else if j := t.first(c.node, skip, func(j int) bool {
			return t.nodes[j].tag == "#text" && plainInt.MatchString(t.nodes[j].text) && t.nodes[t.nodes[j].parent].tag == "faceplate-number"
		}); j >= 0 {
			metric = "s" + t.nodes[j].text
		}
	case "x":
		if j := t.first(c.node, skip, func(j int) bool {
			tid := t.attr(j, "data-testid")
			return (tid == "like" || tid == "unlike") && leadingCount.MatchString(t.attr(j, "aria-label"))
		}); j >= 0 {
			metric = "likes=" + leadingCount.FindStringSubmatch(t.attr(j, "aria-label"))[1]
		}
	}

	body := -1
	switch site {
	case "reddit":
		body = t.first(c.node, skip, func(j int) bool { return t.attr(j, "slot") == "comment" })
	case "x":
		body = t.first(c.node, skip, func(j int) bool { return t.attr(j, "data-testid") == "tweetText" })
	}
	if body >= 0 {
		summary = t.text(body, skip)
	} else {
		summary = t.text(c.node, own)
	}
	summary = cutBytes(summary, summaryBytes)
	return
}

// medianGap is the typical whitespace between sibling records, the yardstick
// for deciding that an orphan control right under a record continues it.
func medianGap(recs []Record) float64 {
	var gaps []float64
	for i := 1; i < len(recs); i++ {
		a, b := recs[i-1], recs[i]
		if a.NetOnly || b.NetOnly || a.Parent != b.Parent || a.Box.Empty() || b.Box.Empty() {
			continue
		}
		if g := b.Box.Y - a.Box.Bottom(); g > 0 {
			gaps = append(gaps, g)
		}
	}
	if len(gaps) == 0 {
		return defaultGap
	}
	sort.Float64s(gaps)
	if n := len(gaps); n%2 == 1 {
		return gaps[n/2]
	} else {
		return (gaps[n/2-1] + gaps[n/2]) / 2
	}
}

// continuesRecord finds the record an orphan sits directly below: the nearest
// record above it that overlaps it horizontally, within the gap threshold.
func continuesRecord(ctl Control, recs []Record, threshold float64) int {
	best, bestGap := -1, threshold+1
	for i, r := range recs {
		if r.NetOnly || r.Box.Empty() {
			continue
		}
		gap := ctl.Box.Y - r.Box.Bottom()
		if gap < 0 || gap > threshold || gap >= bestGap {
			continue
		}
		if ctl.Box.X >= r.Box.Right() || ctl.Box.Right() <= r.Box.X {
			continue
		}
		best, bestGap = i, gap
	}
	return best
}

// Build segments the snapshot into records and assigns every control an owner.
func Build(in Input) (*Graph, error) {
	s := in.Snapshot
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if s.Len() == 0 {
		return nil, errors.New("scene: empty snapshot")
	}
	keys := in.Keys
	if keys == (Keys{}) {
		keys = KeysForURL(s.URL)
	}
	t := newTree(s)
	frame6 := Frame6(s.FrameID, s.LoaderID)
	g := &Graph{Frame6: frame6, URL: s.URL, Viewport: s.Viewport, Stats: map[string]int{"nodes": s.Len()}}

	containers := t.findContainers(keys, g.Stats)
	recs := make([]Record, len(containers))
	for i, c := range containers {
		r := Record{R: RecordHandle(c.ns, c.key), Key: c.key, Kind: c.kind, Depth: c.depth, Order: i, Src: "dom"}
		if c.parent >= 0 {
			r.Parent = RecordHandle(containers[c.parent].ns, containers[c.parent].key)
		}
		if b := t.nodes[c.node].box; b != nil {
			r.Box = *b
		}
		recs[i] = r
	}

	byID := map[string]int{}
	for i := range t.nodes {
		if id := t.attr(i, "id"); id != "" {
			if _, dup := byID[id]; !dup {
				byID[id] = i
			}
		}
	}

	isControl := map[int]bool{}
	var orphans []Control
	for i := range t.nodes {
		role, ok := t.interactiveRole(i, isControl)
		if !ok {
			continue
		}
		isControl[i] = true
		ctl := Control{H: NodeHandle(frame6, t.nodes[i].backend), Role: role, Backend: t.nodes[i].backend, Vis: t.vis(i)}
		if b := t.nodes[i].box; b != nil {
			ctl.Box = *b
		}
		if t.nodes[i].container >= 0 {
			// The record's own box is clickable (X quote blocks): one word is
			// more useful than its whole text as a name.
			ctl.Name, ctl.NameSrc = "open", "container"
		} else {
			ctl.Name, ctl.NameSrc = t.name(i, byID)
		}
		if ctl.Name != "" {
			g.Stats["named"]++
		}
		if ci := t.nearestContainer(i); ci >= 0 {
			ctl.Owner, ctl.Rel = recs[ci].R, "contains"
			recs[ci].Controls = append(recs[ci].Controls, ctl)
		} else {
			orphans = append(orphans, ctl)
		}
		g.Stats["controls"]++
		g.Stats[ctl.Vis]++
	}

	for i, c := range containers {
		recs[i].Author, recs[i].Time, recs[i].Metric, recs[i].Summary = t.summarize(c, keys.Site, isControl)
	}

	threshold := 1.5 * medianGap(recs)
	for _, ctl := range orphans {
		if ctl.Vis == "hidden" {
			g.Orphans = append(g.Orphans, ctl)
			continue
		}
		if ri := continuesRecord(ctl, recs, threshold); ri >= 0 {
			ctl.Owner, ctl.Rel = recs[ri].R, "continues"
			recs[ri].Controls = append(recs[ri].Controls, ctl)
			g.Stats["continues"]++
			continue
		}
		g.Orphans = append(g.Orphans, ctl)
	}

	if len(in.Network) > 0 {
		recs = joinNetwork(recs, in.Network, keys)
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Order < recs[j].Order })
	g.Records = recs

	for _, r := range g.Records {
		g.Stats["records"]++
		if r.Kind == "quote" {
			g.Stats["quotes"]++
		}
		if r.NetOnly {
			g.Stats["net_only"]++
		}
		g.Stats["owned"] += len(r.Controls)
	}
	g.Stats["orphans"] = len(g.Orphans)
	return g, nil
}

// joinNetwork aligns DOM records with the network decoder's by handle.
// Network order becomes reading order; records the DOM never rendered are
// kept as net_only so nothing the page loaded is silently dropped.
func joinNetwork(recs []Record, network []records.Record, keys Keys) []Record {
	ns := "dom"
	switch keys.Site {
	case "reddit":
		ns = ""
	case "x":
		ns = "tw"
	}
	byR := map[string]int{}
	byKey := map[string]int{}
	for i, r := range recs {
		byR[r.R] = i
		byKey[r.Key] = i
	}
	matched := map[int]bool{}
	var added []Record
	for order, nr := range network {
		key := nr.DOMKey
		if key == "" {
			key = nr.ID
		}
		r := nr.R
		if r == "" {
			r = RecordHandle(ns, key)
		}
		idx, ok := byR[r]
		if !ok {
			idx, ok = byKey[key]
		}
		if ok {
			rec := &recs[idx]
			rec.Src, rec.Order = "both", order
			matched[idx] = true
			if rec.Author == "" {
				rec.Author = nr.Author
			}
			if rec.Time == "" {
				rec.Time = nr.Time
			}
			if rec.Summary == "" {
				rec.Summary = cutBytes(collapse(nr.Text), summaryBytes)
			}
			if rec.Metric == "" {
				rec.Metric = networkMetric(nr, keys.Site)
			}
			continue
		}
		parent := ""
		if nr.Parent != "" {
			if pi, ok := byKey[nr.Parent]; ok {
				parent = recs[pi].R
			} else {
				parent = RecordHandle(ns, nr.Parent)
			}
		}
		added = append(added, Record{
			R: r, Key: key, Kind: nr.Kind, Parent: parent, Depth: nr.Depth, Order: order,
			Author: nr.Author, Time: nr.Time, Summary: cutBytes(collapse(nr.Text), summaryBytes),
			Metric: networkMetric(nr, keys.Site), Src: "network", NetOnly: true,
		})
	}
	for i := range recs {
		if !matched[i] {
			recs[i].Order += len(network)
		}
	}
	return append(recs, added...)
}

func networkMetric(nr records.Record, site string) string {
	switch site {
	case "reddit":
		if v, ok := nr.Metrics["score"]; ok {
			return "s" + strconv.FormatInt(v, 10)
		}
	case "x":
		if v, ok := nr.Metrics["likes"]; ok {
			return "likes=" + strconv.FormatInt(v, 10)
		}
	}
	return ""
}
