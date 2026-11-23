package records

import "math"

type Thread struct {
	Page
	byID map[string]int
}

type Delta struct {
	Added      int `json:"added"`
	Updated    int `json:"updated"`
	Duplicates int `json:"duplicates"`
}

func NewThread(p Page) *Thread {
	t := &Thread{Page: newPage(p.Site, p.Op), byID: map[string]int{}}
	t.Merge(p, nil)
	return t
}

func cloneRecord(r Record) Record {
	r.Flags = append([]string(nil), r.Flags...)
	metrics := map[string]int64{}
	for k, v := range r.Metrics {
		metrics[k] = v
	}
	r.Metrics = metrics
	if r.WireDepth != nil {
		depth := *r.WireDepth
		r.WireDepth = &depth
	}
	return r
}

func fillRecord(dst *Record, src Record) bool {
	changed := false
	if dst.Src == (Source{}) && src.Src != (Source{}) {
		dst.Src, changed = src.Src, true
	}
	for _, pair := range []struct {
		dst *string
		src string
	}{
		{&dst.R, src.R}, {&dst.Parent, src.Parent}, {&dst.Kind, src.Kind}, {&dst.Author, src.Author}, {&dst.Time, src.Time}, {&dst.Text, src.Text}, {&dst.URL, src.URL}, {&dst.DOMKey, src.DOMKey},
		{&dst.Src.RequestID, src.Src.RequestID}, {&dst.Src.Op, src.Src.Op}, {&dst.Src.SHA256, src.Src.SHA256},
	} {
		if *pair.dst == "" && pair.src != "" {
			*pair.dst, changed = pair.src, true
		}
	}
	if dst.WireDepth == nil && src.WireDepth != nil {
		depth := *src.WireDepth
		dst.WireDepth, changed = &depth, true
	}
	if dst.Metrics == nil {
		dst.Metrics = map[string]int64{}
	}
	for key, value := range src.Metrics {
		if _, exists := dst.Metrics[key]; !exists {
			dst.Metrics[key], changed = value, true
		}
	}
	for _, flag := range src.Flags {
		if !hasFlag(*dst, flag) {
			addFlag(dst, flag)
			changed = true
		}
	}
	return changed
}

func sameContinuation(a, b Continuation) bool {
	return a.Kind == b.Kind && a.Parent == b.Parent && a.Key == b.Key
}

func (t *Thread) Merge(p Page, from *Continuation) Delta {
	if t.byID == nil {
		t.byID = map[string]int{}
		for i, r := range t.Records {
			t.byID[r.ID] = i
		}
	}
	if t.Dropped == nil {
		t.Dropped = map[string]int{}
	}
	if t.Site == "" {
		t.Site, t.Op = p.Site, p.Op
	}
	if p.Thread != nil {
		if t.Thread == nil {
			r := cloneRecord(*p.Thread)
			t.Thread = &r
		} else if t.Thread.ID == p.Thread.ID {
			fillRecord(t.Thread, *p.Thread)
		}
	}
	if t.Expected < 0 && p.Expected >= 0 {
		t.Expected = p.Expected
	}
	if p.Class != "" && p.Class != "ok" {
		t.Class = p.Class
	}
	t.Terminal = t.Terminal || p.Terminal
	for key, value := range p.Dropped {
		t.Dropped[key] += value
	}
	delta := Delta{}
	var added []string
	for _, r := range p.Records {
		if r.ID == "" {
			t.Dropped["missing_id"]++
			continue
		}
		if t.Thread != nil && r.ID == t.Thread.ID {
			if fillRecord(t.Thread, r) {
				delta.Updated++
			} else {
				delta.Duplicates++
			}
			continue
		}
		if at, ok := t.byID[r.ID]; ok {
			if fillRecord(&t.Records[at], r) {
				delta.Updated++
			} else {
				delta.Duplicates++
			}
		} else {
			t.byID[r.ID] = len(t.Records)
			t.Records = append(t.Records, cloneRecord(r))
			added = append(added, r.ID)
			delta.Added++
		}
	}
	t.deriveDepths()
	var resolved Continuation
	if from != nil {
		resolved = *from
		for _, id := range added {
			if t.under(id, from.Parent) {
				resolved.Yield.Records++
			}
		}
		seen := []Continuation{}
		for _, c := range p.Continuations {
			duplicate := false
			for _, previous := range t.Continuations {
				duplicate = duplicate || sameContinuation(previous, c)
			}
			for _, previous := range seen {
				duplicate = duplicate || sameContinuation(previous, c)
			}
			if !duplicate && !sameContinuation(c, *from) && (c.Kind == "branch" || c.Kind == "top_level") && (c.Parent == from.Parent || t.under(c.Parent, from.Parent)) {
				resolved.Yield.NestedHints += max(0, c.CountHint)
			}
			seen = append(seen, c)
		}
		threshold := from.CountHint - max(2, int(math.Ceil(float64(from.CountHint)*0.1)))
		resolved.State, resolved.Reason = "resolved", ""
		if resolved.Yield.Records+resolved.Yield.NestedHints < threshold {
			resolved.State, resolved.Reason = "resolved_short", "hint_mismatch"
		}
		*from = resolved
		found := false
		for i := range t.Continuations {
			if sameContinuation(t.Continuations[i], resolved) {
				t.Continuations[i] = resolved
				found = true
			}
		}
		if !found {
			t.Continuations = append(t.Continuations, resolved)
		}
	}
	for _, c := range p.Continuations {
		if c.State == "" {
			c.State = "pending"
		}
		found := false
		for _, existing := range t.Continuations {
			if sameContinuation(existing, c) {
				found = true
				break
			}
		}
		if !found {
			t.Continuations = append(t.Continuations, c)
		}
	}
	return delta
}

func (t *Thread) under(id, parent string) bool {
	if parent == "" {
		return true
	}
	for steps := 0; steps <= len(t.Records); steps++ {
		if id == parent {
			return true
		}
		at, ok := t.byID[id]
		if !ok {
			return false
		}
		id = t.Records[at].Parent
	}
	return false
}

func (t *Thread) deriveDepths() {
	state := make([]uint8, len(t.Records))
	root := ""
	if t.Thread != nil {
		root = t.Thread.ID
	}
	for i := range t.Records {
		r := &t.Records[i]
		kept := r.Flags[:0]
		for _, flag := range r.Flags {
			if flag != "orphan" && flag != "cycle" && flag != "wire_depth_mismatch" {
				kept = append(kept, flag)
			}
		}
		r.Flags = kept
	}
	for start := range t.Records {
		if state[start] == 2 {
			continue
		}
		var path []int
		at := start
		for state[at] == 0 {
			state[at] = 1
			path = append(path, at)
			parent := t.Records[at].Parent
			if root != "" && parent == root {
				break
			}
			next, ok := t.byID[parent]
			if !ok {
				break
			}
			at = next
		}
		if state[at] == 1 && len(path) > 0 && at != path[len(path)-1] {
			cycle := false
			for _, i := range path {
				cycle = cycle || i == at
				if cycle {
					addFlag(&t.Records[i], "cycle")
				}
			}
		}
		for i := len(path) - 1; i >= 0; i-- {
			r := &t.Records[path[i]]
			parent, known := t.byID[r.Parent]
			switch {
			case r.Parent == r.ID:
				addFlag(r, "cycle")
				fallthrough
			case hasFlag(*r, "cycle"):
				r.Depth = 0
			case root != "" && r.Parent == root:
				r.Depth = 0
			case known && state[parent] == 2:
				r.Depth = t.Records[parent].Depth + 1
				if hasFlag(t.Records[parent], "orphan") || hasFlag(t.Records[parent], "cycle") {
					addFlag(r, "orphan")
				}
			default:
				r.Depth = 0
				if r.WireDepth != nil {
					r.Depth = *r.WireDepth
				}
				addFlag(r, "orphan")
			}
			if r.WireDepth != nil && r.Depth != *r.WireDepth {
				addFlag(r, "wire_depth_mismatch")
			}
			state[path[i]] = 2
		}
	}
}

// Ordered visits parents before children without recursive calls. The final
// pass retains disconnected and cyclic components in first-seen order.
func (t *Thread) Ordered() []Record {
	children := map[string][]int{}
	var roots []int
	for i, r := range t.Records {
		children[r.Parent] = append(children[r.Parent], i)
		if _, known := t.byID[r.Parent]; !known || (t.Thread != nil && r.Parent == t.Thread.ID) {
			roots = append(roots, i)
		}
	}
	seen := make([]bool, len(t.Records))
	result := make([]Record, 0, len(t.Records))
	visit := func(start int) {
		stack := []int{start}
		for len(stack) > 0 {
			at := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[at] {
				continue
			}
			seen[at] = true
			result = append(result, cloneRecord(t.Records[at]))
			branch := children[t.Records[at].ID]
			for i := len(branch) - 1; i >= 0; i-- {
				stack = append(stack, branch[i])
			}
		}
	}
	for _, i := range roots {
		visit(i)
	}
	for i := range t.Records {
		visit(i)
	}
	return result
}
