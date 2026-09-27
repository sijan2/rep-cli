package jevdom

import (
	"context"
	"errors"
	"time"

	"github.com/repplus/rep-cli/internal/jev"
)

type SelectionTiming struct {
	ObservationMS   float64             `json:"observation_ms"`
	EvaluationMS    float64             `json:"evaluation_ms"`
	ValidationMS    float64             `json:"validation_ms"`
	TotalMS         float64             `json:"total_ms"`
	Requests        int                 `json:"requests"`
	Transport       []jev.RequestTiming `json:"transport,omitempty"`
	ObservationMode string              `json:"observation_mode"`
}

func milliseconds(start time.Time) float64 {
	return float64(time.Since(start)) / float64(time.Millisecond)
}

// SelectBatch resolves independent goals against one coherent observation.
// Request-level usage is attributed once, to the first participating result;
// UsageShared marks results whose provider request also answered another goal.
func (selector Selector) SelectBatch(ctx context.Context, inputs []Options) ([]Result, error) {
	if len(inputs) == 0 || len(inputs) > 32 {
		return nil, errors.New("selection batch requires between 1 and 32 goals")
	}
	started := time.Now()
	options := append([]Options(nil), inputs...)
	for i := range options {
		var err error
		options[i], err = options[i].Validate()
		if err != nil {
			return nil, err
		}
		if i > 0 {
			a, b := options[0], options[i]
			if a.TabID != b.TabID || a.Kind != b.Kind || a.Origin != b.Origin || a.Model != b.Model || a.Strategy != b.Strategy || a.ObservationMode != b.ObservationMode || a.Owner != b.Owner || a.LeaseID != b.LeaseID || a.FrameID != b.FrameID || a.FrameURL != b.FrameURL || a.ScopeBackendDOMNodeID != b.ScopeBackendDOMNodeID {
				return nil, errors.New("batch goals must share browser observation, model, and strategy")
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	now := selector.Now
	if now == nil {
		now = time.Now
	}
	stage := time.Now()
	snapshot, compact, err := selector.observe(ctx, options[0])
	if err != nil {
		return nil, err
	}
	timing := SelectionTiming{ObservationMS: milliseconds(stage), ObservationMode: "legacy"}
	if compact {
		timing.ObservationMode = "compact"
	}
	results := make([]Result, len(options))
	decisions := make([]evaluated, len(options))
	candidates := make([][]Candidate, len(options))
	keys := make([]string, len(options))
	var pending []int
	for i, opt := range options {
		candidates[i] = shortlist(snapshot.Candidates, opt.Goal, opt.Limit)
		coverage := snapshot.Coverage
		coverage.Considered = len(candidates[i])
		coverage.Truncated = coverage.Truncated || len(candidates[i]) < len(snapshot.Candidates)
		results[i] = Result{Status: "no_match", Probabilities: map[string]float64{}, Model: opt.Model, SnapshotFingerprint: snapshot.Fingerprint, Coverage: coverage}
		if len(candidates[i]) == 0 {
			results[i].NeedsReview = coverage.UnavailableFrames > 0 || coverage.OmittedNodes > 0 || coverage.Truncated
			if results[i].NeedsReview {
				results[i].Status = "needs_review"
				results[i].Reason = "Accessibility coverage is incomplete"
			}
			continue
		}
		keys[i] = cacheKey(snapshot.Fingerprint, opt)
		if !opt.NoCache && selector.Cache != nil {
			if saved, ok := selector.Cache.load(keys[i], now()); ok && validDecision(saved, candidates[i], opt.Strategy) {
				decisions[i] = saved
				results[i].CacheHit = true
			}
		}
		if !results[i].CacheHit {
			pending = append(pending, i)
		}
	}
	stage = time.Now()
	if len(pending) > 0 {
		if selector.Evaluator == nil {
			return nil, errors.New("a Jev evaluator is required")
		}
		if options[0].Strategy == "legacy" {
			for _, i := range pending {
				decisions[i], err = evaluateCandidates(ctx, selector.Evaluator, options[i].Goal, candidates[i])
				if err != nil {
					return nil, err
				}
			}
		} else if err = evaluateAdaptive(ctx, selector.Evaluator, options, candidates, pending, decisions, results); err != nil {
			return nil, err
		}
	}
	timing.EvaluationMS = milliseconds(stage)
	anyCandidates := false
	for i := range results {
		if len(candidates[i]) == 0 {
			continue
		}
		anyCandidates = true
		d := decisions[i]
		r := &results[i]
		r.Model, r.Confidence, r.Probabilities, r.Decisions = d.Model, d.Answer.Confidence, d.Answer.Probabilities, d.Decisions
		r.UsageShared = d.UsageShared
		if r.CacheHit {
			usage := d.Usage
			r.OriginalUsage = &usage
		} else {
			r.Usage = d.Usage
			r.Timing.Requests = d.Requests
			r.Timing.Transport = d.Transport
		}
	}
	stage = time.Now()
	fresh := true
	// For an all-cache-hit batch the coherent initial observation is already a
	// synchronous validation of every cached decision's full fingerprint. No
	// provider wait separates it from return. Execution still validates its own
	// binding immediately before acting. New inference always needs a refresh.
	if anyCandidates && len(pending) > 0 {
		fresh, err = selector.Validate(ctx, options[0], Binding{Generation: snapshot.Generation, SnapshotFingerprint: snapshot.Fingerprint, ObservationMode: timing.ObservationMode})
		fresh = fresh && err == nil
		timing.ValidationMS = milliseconds(stage)
	}
	for i, opt := range options {
		r := &results[i]
		requests, transport := r.Timing.Requests, r.Timing.Transport
		r.Timing = timing
		r.Timing.Requests = requests
		r.Timing.Transport = transport
		if len(candidates[i]) == 0 {
			r.Timing.TotalMS = milliseconds(started)
			continue
		}
		if !fresh {
			r.Status, r.NeedsReview, r.Reason = "stale", true, "The page changed or its current observation could not be verified"
			if !opt.NoCache && selector.Cache != nil {
				selector.Cache.remove(keys[i])
			}
			r.Timing.TotalMS = milliseconds(started)
			continue
		}
		decision := decisions[i]
		if !r.CacheHit && !opt.NoCache && selector.Cache != nil {
			selector.Cache.store(keys[i], decision, now())
		}
		r.NeedsReview = decision.Answer.Confidence < opt.Confidence || r.Coverage.Truncated || r.Coverage.UnavailableFrames > 0 || r.Coverage.OmittedNodes > 0 || r.Coverage.TextTruncated > 0
		for _, d := range decision.Decisions {
			if d.Stage != "group" {
				continue
			}
			if d.Answer.Confidence < opt.Confidence {
				r.NeedsReview = true
			}
			if d.Answer.Choice == "none" && decision.Answer.Choice != "none" {
				for _, id := range d.CandidateIDs {
					if id == decision.Answer.Choice {
						r.NeedsReview = true
					}
				}
			}
		}
		if decision.Answer.Choice != "none" {
			for _, candidate := range candidates[i] {
				if candidate.ID == decision.Answer.Choice {
					selected := candidate
					r.Selected = &selected
					break
				}
			}
			if r.Selected == nil {
				return nil, errors.New("Jev selected an unobserved candidate")
			}
			r.Status = "selected"
			c := r.Selected
			r.Binding = &Binding{Generation: snapshot.Generation, FrameID: c.FrameID, SessionID: c.SessionID, DocumentGeneration: c.DocumentGeneration, FrameURL: c.FrameURL, BackendDOMNodeID: c.BackendDOMNodeID, SnapshotFingerprint: snapshot.Fingerprint, ObservationMode: timing.ObservationMode, Kind: opt.Kind, Origin: opt.Origin, ScopeFrameID: opt.FrameID, ScopeFrameURL: opt.FrameURL, ScopeBackendDOMNodeID: opt.ScopeBackendDOMNodeID}
		}
		if r.NeedsReview {
			r.Status = "needs_review"
			r.Reason = "Confidence or accessibility coverage requires review"
		}
		r.Timing.TotalMS = milliseconds(started)
	}
	return results, nil
}

func validDecision(value evaluated, candidates []Candidate, strategy string) bool {
	if strategy == "legacy" {
		return validEvaluated(value, candidates)
	}
	return validAdaptive(value, candidates)
}
