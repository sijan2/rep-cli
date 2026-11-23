package records

import "strings"

type Contracts struct {
	ID           float64 `json:"id"`
	Parent       float64 `json:"parent"`
	Author       float64 `json:"author"`
	Time         float64 `json:"time"`
	Text         float64 `json:"text"`
	UnknownShare float64 `json:"unknown_share"`
}

func DefaultContracts() Contracts {
	return Contracts{ID: 0, Parent: 0.01, Author: 0.05, Time: 0.05, Text: 0.10, UnknownShare: 0.10}
}

func Check(t *Thread, c Contracts) Coverage {
	coverage := Coverage{Expected: t.Expected, Ratio: -1, Status: "complete", Code: "complete", FrontierExhausted: true, Dropped: map[string]int{}, NullRates: map[string]float64{}, Breaches: []string{}}
	unknown, dropped := 0, 0
	for key, count := range t.Dropped {
		coverage.Dropped[key] = count
		if strings.HasPrefix(key, "unknown_") || strings.HasPrefix(key, "unparseable_") || key == "missing_id" {
			unknown += count
		}
		if key != "loader_fallback" {
			dropped += count
		}
	}
	missing := map[string]int{}
	eligible := map[string]int{}
	for _, r := range t.Records {
		if (t.Site == "reddit" && r.Kind == "comment") || (t.Site == "x" && r.Kind == "tweet" && t.Thread != nil && r.Parent == t.Thread.ID) {
			coverage.Records++
		}
		for field, value := range map[string]string{"id": r.ID, "parent": r.Parent, "author": r.Author, "time": r.Time, "text": r.Text} {
			if field == "text" && (hasFlag(r, "deleted") || hasFlag(r, "tombstone") || hasFlag(r, "unavailable")) {
				continue
			}
			eligible[field]++
			if strings.TrimSpace(value) == "" || (field == "parent" && (hasFlag(r, "orphan") || hasFlag(r, "cycle"))) {
				missing[field]++
			}
		}
	}
	for _, field := range []struct {
		name    string
		ceiling float64
	}{{"id", c.ID}, {"parent", c.Parent}, {"author", c.Author}, {"time", c.Time}, {"text", c.Text}} {
		rate := 0.0
		if eligible[field.name] > 0 {
			rate = float64(missing[field.name]) / float64(eligible[field.name])
		}
		coverage.NullRates[field.name] = rate
		if rate > field.ceiling || (field.name == "id" && t.Dropped["missing_id"] > 0) {
			coverage.Breaches = append(coverage.Breaches, field.name)
		}
	}
	for _, continuation := range t.Continuations {
		switch continuation.State {
		case "resolved":
		case "skipped":
			if continuation.Reason == "" {
				coverage.Pending++
			}
		case "resolved_short":
			coverage.ResolvedShort++
		default:
			coverage.Pending++
		}
	}
	coverage.FrontierExhausted = coverage.Pending == 0 && coverage.ResolvedShort == 0
	if t.Expected == 0 {
		coverage.Ratio = 1
	} else if t.Expected > 0 {
		coverage.Ratio = float64(coverage.Records) / float64(t.Expected)
	}
	total := len(t.Records) + dropped + len(t.Continuations)
	if t.Thread != nil {
		total++
	}
	unknownShare := 0.0
	if total > 0 {
		unknownShare = float64(unknown) / float64(total)
	}
	switch {
	case t.Class == "gated" || t.Class == "challenge" || t.Class == "auth":
		coverage.Code, coverage.GapReason = t.Class, "not_followed"
	case len(coverage.Breaches) > 0:
		coverage.Code, coverage.GapReason = "drift_fields", "unknown"
	case unknownShare > c.UnknownShare:
		coverage.Code, coverage.GapReason = "drift_unknown", "unknown"
	case coverage.ResolvedShort > 0:
		coverage.Code, coverage.GapReason = "resolved_short", "hint_mismatch"
	case coverage.Pending > 0:
		coverage.Code, coverage.GapReason = "frontier_pending", "not_followed"
	case t.Expected > 0 && coverage.Ratio < 0.9:
		coverage.Code, coverage.GapReason = "server_gap", "unknown"
	}
	if coverage.Code != "complete" {
		coverage.Status = "partial"
	}
	return coverage
}
