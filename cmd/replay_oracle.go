package cmd

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
)

// A live endpoint embeds per-response nonces, timestamps and trace IDs, so
// comparing a replay against a capture taken at a different moment reports a
// difference every time. The fix is to ask the endpoint what it changes on its
// own: replay the unmutated request N times, learn which fields move by
// themselves, and judge a mutant only on the fields that held still.

// volatilityMask records which parts of a response vary between identical
// requests. Kind selects how Stable comparisons are performed.
type volatilityMask struct {
	Kind             string          `json:"kind"` // json, line, or none
	Samples          int             `json:"samples"`
	VolatilePointers map[string]bool `json:"-"`
	VolatileLines    map[int]bool    `json:"-"`
	VolatileCount    int             `json:"volatile_fields"`
	StableCount      int             `json:"stable_fields"`
	Usable           bool            `json:"usable"`
	Reason           string          `json:"reason,omitempty"`
	StatusStable     bool            `json:"status_stable"`
}

// comparison is the tri-state result of judging one response against baseline.
// "inconclusive" is a real outcome and is never rounded toward either side.
type comparison struct {
	Verdict      string   `json:"verdict"` // differs, equivalent, inconclusive
	Reason       string   `json:"reason,omitempty"`
	StatusDelta  int      `json:"status_delta"`
	ChangedPaths []string `json:"changed_stable_paths,omitempty"`
	ChangedCount int      `json:"changed_stable_count"`
	Method       string   `json:"method"`
}

// buildVolatilityMask derives the mask from >=2 samples of the same request.
func buildVolatilityMask(samples [][]byte, statuses []int) volatilityMask {
	mask := volatilityMask{Samples: len(samples), Kind: "none"}
	if len(samples) < 2 {
		mask.Reason = "fewer than two baseline samples"
		return mask
	}

	mask.StatusStable = true
	for _, s := range statuses[1:] {
		if s != statuses[0] {
			mask.StatusStable = false
			break
		}
	}

	// Prefer structural comparison: it survives key reordering and whitespace.
	flats := make([]map[string]string, 0, len(samples))
	allJSON := true
	for _, body := range samples {
		flat, ok := flattenJSON(body)
		if !ok {
			allJSON = false
			break
		}
		flats = append(flats, flat)
	}
	if allJSON {
		mask.Kind = "json"
		mask.VolatilePointers = map[string]bool{}
		keys := map[string]bool{}
		for _, f := range flats {
			for k := range f {
				keys[k] = true
			}
		}
		for k := range keys {
			first, present := flats[0][k]
			volatile := !present
			for _, f := range flats[1:] {
				v, ok := f[k]
				if !ok || v != first {
					volatile = true
					break
				}
			}
			if volatile {
				mask.VolatilePointers[k] = true
			}
		}
		mask.VolatileCount = len(mask.VolatilePointers)
		mask.StableCount = len(keys) - mask.VolatileCount
		mask.Usable = true
		return mask
	}

	// Fall back to line positions. Only meaningful when the shape is stable;
	// a body whose line count moves between identical requests cannot be
	// compared positionally and must be reported inconclusive instead.
	lines := make([][]string, 0, len(samples))
	for _, body := range samples {
		lines = append(lines, strings.Split(string(body), "\n"))
	}
	for _, l := range lines[1:] {
		if len(l) != len(lines[0]) {
			mask.Reason = "baseline line count is unstable between identical requests"
			return mask
		}
	}
	mask.Kind = "line"
	mask.VolatileLines = map[int]bool{}
	for i := range lines[0] {
		for _, l := range lines[1:] {
			if l[i] != lines[0][i] {
				mask.VolatileLines[i] = true
				break
			}
		}
	}
	mask.VolatileCount = len(mask.VolatileLines)
	mask.StableCount = len(lines[0]) - mask.VolatileCount
	mask.Usable = true
	return mask
}

// compareAgainstBaseline judges candidate against the baseline reference using
// only the parts the mask proved stable.
func compareAgainstBaseline(mask volatilityMask, reference, candidate []byte, refStatus, candStatus int, complete bool) comparison {
	out := comparison{
		StatusDelta: candStatus - refStatus,
		Method:      mask.Kind,
		Verdict:     "inconclusive",
	}
	if !complete {
		out.Reason = "response body evidence is incomplete"
		return out
	}
	if !mask.Usable {
		out.Method = "none"
		out.Reason = mask.Reason
		if out.Reason == "" {
			out.Reason = "no usable volatility mask"
		}
		return out
	}

	// A status change is a difference on its own, but only when the endpoint
	// proved its status stable across identical requests.
	if out.StatusDelta != 0 {
		if !mask.StatusStable {
			out.Reason = "status varies between identical baseline requests"
			return out
		}
		out.Verdict = "differs"
		out.Reason = fmt.Sprintf("status %d -> %d on an endpoint with stable status", refStatus, candStatus)
		return out
	}

	switch mask.Kind {
	case "json":
		refFlat, okRef := flattenJSON(reference)
		candFlat, okCand := flattenJSON(candidate)
		if !okRef || !okCand {
			out.Reason = "response stopped parsing as JSON; shape changed between baseline and candidate"
			return out
		}
		keys := map[string]bool{}
		for k := range refFlat {
			keys[k] = true
		}
		for k := range candFlat {
			keys[k] = true
		}
		changed := []string{}
		for k := range keys {
			if mask.VolatilePointers[k] {
				continue
			}
			if refFlat[k] != candFlat[k] {
				changed = append(changed, k)
			}
		}
		sort.Strings(changed)
		out.ChangedCount = len(changed)
		if len(changed) > 12 {
			out.ChangedPaths = append(changed[:12:12], fmt.Sprintf("... and %d more", len(changed)-12))
		} else {
			out.ChangedPaths = changed
		}
		if len(changed) == 0 {
			out.Verdict = "equivalent"
			out.Reason = fmt.Sprintf("identical on %d stable fields (%d known-volatile ignored)", mask.StableCount, mask.VolatileCount)
		} else {
			out.Verdict = "differs"
			out.Reason = fmt.Sprintf("%d stable field(s) changed", len(changed))
		}
		return out

	case "line":
		refLines := strings.Split(string(reference), "\n")
		candLines := strings.Split(string(candidate), "\n")
		if len(refLines) != len(candLines) {
			out.Verdict = "differs"
			out.Reason = fmt.Sprintf("line count %d -> %d", len(refLines), len(candLines))
			return out
		}
		changed := []string{}
		for i := range refLines {
			if mask.VolatileLines[i] {
				continue
			}
			if refLines[i] != candLines[i] {
				changed = append(changed, "line "+strconv.Itoa(i+1))
			}
		}
		out.ChangedCount = len(changed)
		if len(changed) > 12 {
			out.ChangedPaths = append(changed[:12:12], fmt.Sprintf("... and %d more", len(changed)-12))
		} else {
			out.ChangedPaths = changed
		}
		if len(changed) == 0 {
			out.Verdict = "equivalent"
			out.Reason = fmt.Sprintf("identical on %d stable lines (%d known-volatile ignored)", mask.StableCount, mask.VolatileCount)
		} else {
			out.Verdict = "differs"
			out.Reason = fmt.Sprintf("%d stable line(s) changed", len(changed))
		}
		return out
	}

	out.Reason = "unsupported comparison kind"
	return out
}

// flattenJSON renders a JSON document as pointer -> scalar-rendering pairs so
// two documents can be compared field by field regardless of key order.
func flattenJSON(body []byte) (map[string]string, bool) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil, false
	}
	var root interface{}
	if err := sonic.Unmarshal(body, &root); err != nil {
		return nil, false
	}
	out := map[string]string{}
	var walk func(prefix string, node interface{})
	walk = func(prefix string, node interface{}) {
		switch v := node.(type) {
		case map[string]interface{}:
			if len(v) == 0 {
				out[prefix] = "{}"
				return
			}
			for key, child := range v {
				walk(prefix+"/"+escapePointer(key), child)
			}
		case []interface{}:
			if len(v) == 0 {
				out[prefix] = "[]"
				return
			}
			for i, child := range v {
				walk(prefix+"/"+strconv.Itoa(i), child)
			}
		case nil:
			out[prefix] = "null"
		default:
			out[prefix] = fmt.Sprintf("%v", v)
		}
	}
	walk("", root)
	return out, true
}

func escapePointer(key string) string {
	key = strings.ReplaceAll(key, "~", "~0")
	return strings.ReplaceAll(key, "/", "~1")
}

// authzVerdict interprets a set of per-principal comparisons for an
// authorization question. It deliberately refuses to call anything a finding
// without a named subject and an observed access difference.
type authzVerdict struct {
	Verdict   string   `json:"verdict"` // possible_authz_issue, enforced, inconclusive
	Reason    string   `json:"reason"`
	Subjects  []string `json:"subjects_compared"`
	Reviewing []string `json:"needs_review,omitempty"`
}

func interpretAuthz(owner string, results map[string]comparison, statuses map[string]int) authzVerdict {
	subjects := make([]string, 0, len(results))
	for name := range results {
		subjects = append(subjects, name)
	}
	sort.Strings(subjects)
	out := authzVerdict{Subjects: subjects}

	inconclusive := []string{}
	equivalent := []string{}
	for _, name := range subjects {
		switch results[name].Verdict {
		case "inconclusive":
			inconclusive = append(inconclusive, name)
		case "equivalent":
			equivalent = append(equivalent, name)
		}
	}
	if len(inconclusive) > 0 {
		out.Verdict = "inconclusive"
		out.Reviewing = inconclusive
		out.Reason = "at least one principal produced evidence that could not be compared"
		return out
	}
	if len(equivalent) > 0 {
		out.Verdict = "possible_authz_issue"
		out.Reason = fmt.Sprintf(
			"principal(s) %s received a response equivalent to %s on all stable fields; "+
				"confirm the response actually contains %s's protected resource before reporting",
			strings.Join(equivalent, ", "), owner, owner)
		out.Reviewing = equivalent
		return out
	}
	out.Verdict = "enforced"
	out.Reason = "every other principal received a materially different response"
	return out
}
