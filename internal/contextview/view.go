// Package contextview produces bounded, deterministic metadata views of captured
// traffic. It never emits request headers, bodies, query strings, or session notes.
package contextview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/repplus/rep-cli/internal/store"
)

const (
	DefaultBudget = 8192
	MinBudget     = 1024
	MaxBudget     = 1024 * 1024
)

// Provenance describes the already-selected source. It must not contain notes,
// URLs, credentials, or other raw capture content. Workspace and task are labels.
type Provenance struct {
	Workspace        string `json:"workspace,omitempty"`
	Task             string `json:"task,omitempty"`
	CaptureSessionID string `json:"capture_session_id,omitempty"`
	ExportedAt       string `json:"exported_at,omitempty"`
	SourceStatus     string `json:"source_status"`
	LegacyGlobal     bool   `json:"legacy_global"`
}

// Input must be scoped and filtered by the caller before Build is called.
// SourceID identifies the logical dataset (not its contents or export time), so
// updates to a live capture can be compared with its earlier cursor. ScopeID is
// the full isolation boundary; neither identifier is emitted in plain text.
type Input struct {
	Requests         []store.Request
	ScopeID          string
	SourceID         string
	Source           string
	Sessions         int
	ExcludedRequests int
	Provenance       Provenance
}

type Options struct {
	Budget   int
	Since    string
	CacheDir string
}

type Count struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// Group represents a method/origin/normalized-route aggregate. IDs are existing
// request IDs for deliberate drill-down; no synthetic ID is passed off as one.
type Group struct {
	ID                     string   `json:"id"`
	Method                 string   `json:"method"`
	Host                   string   `json:"host"`
	Origin                 string   `json:"origin"`
	Route                  string   `json:"route"`
	Count                  int      `json:"count"`
	Statuses               []Count  `json:"statuses"`
	Types                  []Count  `json:"types"`
	BodyStates             []Count  `json:"body_states,omitempty"`
	Latest                 int64    `json:"latest"`
	IDs                    []string `json:"ids"`
	OtherIDs               int      `json:"other_ids,omitempty"`
	RequestsWithoutSafeIDs int      `json:"requests_without_safe_ids,omitempty"`
	Change                 string   `json:"change,omitempty"`
}

type Totals struct {
	Requests         int `json:"requests"`
	Groups           int `json:"groups"`
	Hosts            int `json:"hosts"`
	Sessions         int `json:"sessions"`
	ExcludedRequests int `json:"excluded_requests"`
	Added            int `json:"added"`
	Updated          int `json:"updated"`
	Removed          int `json:"removed"`
	Unchanged        int `json:"unchanged"`
}

type Omitted struct {
	Groups   int `json:"groups"`
	Requests int `json:"requests"`
	Removed  int `json:"removed"`
}

type View struct {
	Version             int        `json:"version"`
	Scope               string     `json:"scope"`
	Source              string     `json:"source"`
	SourceID            string     `json:"source_id"`
	Provenance          Provenance `json:"provenance"`
	ProvenanceTruncated bool       `json:"provenance_truncated,omitempty"`
	Mode                string     `json:"mode"`
	Since               string     `json:"since,omitempty"`
	Cursor              string     `json:"cursor"`
	Complete            bool       `json:"complete"`
	NoChange            bool       `json:"no_change"`
	Totals              Totals     `json:"totals"`
	Omitted             Omitted    `json:"omitted"`
	Groups              []Group    `json:"groups"`
	Removed             []string   `json:"removed"`
}

type aggregate struct {
	group  Group
	digest string
}

// Build returns compact JSON whose length plus one newline is at most Budget.
// A cursor acknowledges only aggregates and removals included in this response.
// Consequently, --since with a partial response's cursor drains omitted changes;
// it never silently marks unseen aggregates as read. There are no model calls.
func Build(input Input, options Options) ([]byte, error) {
	budget := options.Budget
	if budget == 0 {
		budget = DefaultBudget
	}
	if budget < MinBudget || budget > MaxBudget {
		return nil, fmt.Errorf("context budget must be between %d and %d bytes", MinBudget, MaxBudget)
	}
	if input.ScopeID == "" || input.SourceID == "" || input.Source == "" {
		return nil, errors.New("context requires explicit scope, source ID, and source")
	}
	if input.Sessions < 0 || input.ExcludedRequests < 0 {
		return nil, errors.New("context counts must not be negative")
	}
	scopeID, sourceID := digestString(input.ScopeID), digestString(input.SourceID)
	cache, err := openCache(options.CacheDir)
	if err != nil {
		return nil, err
	}
	baseline := checkpoint{Version: 1, Scope: scopeID, Source: sourceID, Groups: map[string]string{}}
	if options.Since != "" {
		baseline, err = cache.load(options.Since, scopeID, sourceID)
		if err != nil {
			return nil, err
		}
	}

	current, hosts := aggregateRequests(input.Requests)
	view := View{
		Version: 1, Scope: scopeID, Source: bounded(input.Source, 32), SourceID: sourceID,
		Mode: "full", Since: options.Since, Cursor: "c1_" + strings.Repeat("0", 64),
		Groups: []Group{}, Removed: []string{},
		Totals: Totals{Requests: len(input.Requests), Groups: len(current), Hosts: hosts,
			Sessions: input.Sessions, ExcludedRequests: input.ExcludedRequests},
	}
	view.Provenance, view.ProvenanceTruncated = boundProvenance(input.Provenance)
	if options.Since != "" {
		view.Mode = "delta"
	}
	pending := make([]aggregate, 0, len(current))
	currentByID := make(map[string]aggregate, len(current))
	for _, item := range current {
		currentByID[item.group.ID] = item
		previous, exists := baseline.Groups[item.group.ID]
		if exists && previous == item.digest {
			view.Totals.Unchanged++
			continue
		}
		if exists {
			item.group.Change = "updated"
			view.Totals.Updated++
		} else {
			item.group.Change = "added"
			view.Totals.Added++
		}
		if options.Since == "" {
			item.group.Change = ""
		}
		pending = append(pending, item)
		view.Omitted.Requests += item.group.Count
	}
	var removed []string
	for id := range baseline.Groups {
		if _, exists := currentByID[id]; !exists {
			removed = append(removed, id)
		}
	}
	sort.Strings(removed)
	view.Totals.Removed = len(removed)
	view.Omitted.Groups, view.Omitted.Removed = len(pending), len(removed)
	view.NoChange = len(pending) == 0 && len(removed) == 0
	view.Complete = view.NoChange
	encoded, _ := json.Marshal(view)
	if len(encoded)+1 > budget {
		return nil, errors.New("context budget is too small for source provenance; increase --max-bytes")
	}

	// Interleave additions/updates and removals, and round-robin groups across
	// origin/method/status classes. A chatty endpoint cannot crowd out all breadth.
	pending = diverseOrder(pending)
	for i := 0; i < len(pending) || i < len(removed); i++ {
		if i < len(pending) {
			item := pending[i]
			view.Groups = append(view.Groups, item.group)
			view.Omitted.Groups--
			view.Omitted.Requests -= item.group.Count
			view.Complete = view.Omitted.Groups == 0 && view.Omitted.Removed == 0
			candidate, _ := json.Marshal(view)
			if len(candidate)+1 <= budget {
				baseline.Groups[item.group.ID] = item.digest
			} else {
				view.Groups = view.Groups[:len(view.Groups)-1]
				view.Omitted.Groups++
				view.Omitted.Requests += item.group.Count
			}
		}
		if i < len(removed) {
			view.Removed = append(view.Removed, removed[i])
			view.Omitted.Removed--
			view.Complete = view.Omitted.Groups == 0 && view.Omitted.Removed == 0
			candidate, _ := json.Marshal(view)
			if len(candidate)+1 <= budget {
				delete(baseline.Groups, removed[i])
			} else {
				view.Removed = view.Removed[:len(view.Removed)-1]
				view.Omitted.Removed++
			}
		}
	}
	view.Complete = view.Omitted.Groups == 0 && view.Omitted.Removed == 0
	if !view.NoChange && len(view.Groups) == 0 && len(view.Removed) == 0 {
		return nil, errors.New("context budget fits no pending changes; increase --max-bytes to make progress")
	}
	view.Cursor, err = cache.save(baseline)
	if err != nil {
		return nil, err
	}
	encoded, err = json.Marshal(view)
	if err != nil {
		return nil, fmt.Errorf("encode context: %w", err)
	}
	if len(encoded)+1 > budget {
		return nil, errors.New("context exceeded its output budget")
	}
	return encoded, nil
}

func boundProvenance(p Provenance) (Provenance, bool) {
	result := Provenance{
		Workspace: bounded(p.Workspace, 96), Task: bounded(p.Task, 64),
		CaptureSessionID: bounded(p.CaptureSessionID, 64), ExportedAt: bounded(p.ExportedAt, 32),
		SourceStatus: bounded(p.SourceStatus, 24), LegacyGlobal: p.LegacyGlobal,
	}
	return result, result != p
}

func digestString(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func diverseOrder(items []aggregate) []aggregate {
	buckets := map[string][]aggregate{}
	for _, item := range items {
		var classes []string
		for _, status := range item.group.Statuses {
			class := "pending"
			if len(status.Value) == 3 && status.Value[0] >= '1' && status.Value[0] <= '5' {
				class = status.Value[:1] + "xx"
			}
			if len(classes) == 0 || classes[len(classes)-1] != class {
				classes = append(classes, class)
			}
		}
		key := item.group.Origin + "\x00" + item.group.Method + "\x00" + strings.Join(classes, ",")
		buckets[key] = append(buckets[key], item)
	}
	keys := make([]string, 0, len(buckets))
	for key, bucket := range buckets {
		keys = append(keys, key)
		sort.Slice(bucket, func(i, j int) bool {
			if bucket[i].group.Latest != bucket[j].group.Latest {
				return bucket[i].group.Latest > bucket[j].group.Latest
			}
			return bucket[i].group.ID < bucket[j].group.ID
		})
	}
	sort.Strings(keys)
	result := make([]aggregate, 0, len(items))
	for round := 0; len(result) < len(items); round++ {
		for _, key := range keys {
			if round < len(buckets[key]) {
				result = append(result, buckets[key][round])
			}
		}
	}
	return result
}
