package jevdom

import (
	"context"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/jev"
)

func TestRoundedDistributionSurvivesAliasTranslationAndCacheValidation(t *testing.T) {
	snapshot, err := Capture(context.Background(), browserFor(page(2)), 12, "controls", "")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{"e0": snapshot.Candidates[0].ID, "e1": snapshot.Candidates[1].ID}
	question := jev.Question{Criteria: map[string]string{"e0": "First", "e1": "Second", "none": "None"}}
	for _, none := range []float64{0.01, 0.03} {
		reported := jev.ChoiceAnswer{Type: "choice", Choice: "e0", Confidence: 0.83, Probabilities: map[string]float64{"e0": 0.98, "e1": 0, "none": none}}
		answer, valid := canonicalAnswer(reported, question, ids)
		if !valid || !validAnswer(answer, candidateIDs(snapshot.Candidates)) {
			t.Fatal("DOM validation rejected an otherwise valid rounded distribution")
		}
		value := evaluated{Model: "jev-1.13.0", Answer: answer, Requests: 1, Decisions: []Decision{decisionFor("final", snapshot.Candidates, answer)}}
		if !validAdaptive(value, snapshot.Candidates) || !validEvaluated(value, snapshot.Candidates) {
			t.Fatal("decision trace validation rejected reported rounding")
		}
		cache := NewCache(t.TempDir())
		key, now := cacheKey(snapshot.Fingerprint, options()), time.Now()
		cache.store(key, value, now)
		saved, ok := cache.load(key, now)
		if !ok || !validAdaptive(saved, snapshot.Candidates) || saved.Answer.Confidence != reported.Confidence || saved.Answer.Probabilities[ids["e0"]] != 0.98 || saved.Answer.Probabilities["none"] != none {
			t.Fatal("cache changed or rejected the provider's reported probabilities or confidence")
		}
	}
	for _, none := range []float64{0, 0.04} {
		answer := jev.ChoiceAnswer{Type: "choice", Choice: ids["e0"], Confidence: 0.83, Probabilities: map[string]float64{ids["e0"]: 0.98, ids["e1"]: 0, "none": none}}
		if validAnswer(answer, candidateIDs(snapshot.Candidates)) {
			t.Fatal("DOM validation accepted two percentage points of missing or excess mass")
		}
	}
}
