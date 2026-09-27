package jevdom

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/jev"
)

func TestBatchCatalogSharesDescriptionsAndPreservesCanonicalDecisions(t *testing.T) {
	snapshot, err := Capture(context.Background(), browserFor(page(103)), 12, "controls", "")
	if err != nil {
		t.Fatal(err)
	}
	var work []choiceWork
	for owner, goal := range []string{"Item 0", "Item 50", "Item 102"} {
		work = append(work, choiceWork{owner: owner, goal: goal, model: "jev-1.13.0", stage: "final", candidates: snapshot.Candidates})
	}
	state, questions, names, ids := workRequest(work, jev.DefaultBudget())
	inlineState, inlineQuestions, _ := inlineWorkRequest(work)
	if len(ids) != 103 || requestSize(state, questions) >= requestSize(inlineState, inlineQuestions) {
		t.Fatal("repeated candidate catalog was not shared")
	}
	data, _ := json.Marshal(state)
	if strings.Count(string(data), `"name":"Item 102"`) != 1 || strings.Contains(string(data), "backend_dom_node_id") || strings.Contains(string(data), "frame_url") || strings.Contains(string(data), snapshot.Candidates[0].ID) {
		t.Fatal("catalog duplicates descriptions or exposes local handles")
	}
	for _, name := range names {
		if len(questions[name].Criteria) != 104 || !strings.Contains(questions[name].Instructions, "state.goals."+name) {
			t.Fatal("question lost its independent goal or offered choices")
		}
	}
	decisions, results := make([]evaluated, 3), make([]Result, 3)
	e := &packingEvaluator{t: t, match: func(goal string, c visibleCandidate) bool { return goal == c.Name }}
	if err = evaluateWork(context.Background(), e, work, decisions, results); err != nil {
		t.Fatal(err)
	}
	for owner, candidateIndex := range []int{0, 50, 102} {
		if decisions[owner].Answer.Choice != snapshot.Candidates[candidateIndex].ID || !validAdaptive(decisions[owner], snapshot.Candidates) {
			t.Fatal("catalog aliases escaped canonical decision validation")
		}
	}
}

func TestSharedCatalogCannotReturnAnotherQuestionsOfferedAlias(t *testing.T) {
	question := jev.Question{Criteria: map[string]string{"none": "None", "e0": "first"}}
	ids := map[string]string{"e0": "observed-a", "e1": "observed-b"}
	answer := jev.ChoiceAnswer{Type: "choice", Choice: "e1", Confidence: 1, Probabilities: map[string]float64{"none": 0, "e1": 1}}
	if _, valid := canonicalAnswer(answer, question, ids); valid {
		t.Fatal("accepted another question's alias through the global catalog")
	}
}

func TestCatalogKeepsSingleQuestionUnchangedAndFallsBackWhenStateTooLarge(t *testing.T) {
	// Two questions share 100 candidates and each adds 100 of its own. The shared
	// catalog is smaller than inline questions, so only the per-question context
	// budget can reject it: its 300-description state does not fit one question.
	build := func(nameBytes int) []choiceWork {
		describe := func(id string) Candidate {
			return Candidate{ID: id, Role: "button", Name: strings.Repeat("n", nameBytes), Context: strings.Repeat("c", 70)}
		}
		var shared []Candidate
		for i := range 100 {
			shared = append(shared, describe(fmt.Sprintf("shared-%d", i)))
		}
		var work []choiceWork
		for group := range 2 {
			candidates := append([]Candidate(nil), shared...)
			for i := range 100 {
				candidates = append(candidates, describe(fmt.Sprintf("unique-%d-%d", group, i)))
			}
			work = append(work, choiceWork{owner: group, goal: "Find a matching button", model: "jev-1.13.0", candidates: candidates})
		}
		return work
	}
	work := build(150)
	singleState, singleQuestions, _, singleIDs := workRequest(work[:1], jev.DefaultBudget())
	originalState, originalQuestions, _ := inlineWorkRequest(work[:1])
	one, _ := json.Marshal([]any{singleState, singleQuestions})
	two, _ := json.Marshal([]any{originalState, originalQuestions})
	if string(one) != string(two) || singleIDs != nil {
		t.Fatal("single-question contract changed")
	}
	inlineState, inlineQuestions, _ := inlineWorkRequest(work)
	if err := jev.ValidateRequestForModel(work[0].model, inlineState, inlineQuestions); err != nil {
		t.Fatal("fixture must fit independent inline questions:", err)
	}
	state, questions, _, ids := workRequest(work, jev.DefaultBudget())
	if ids != nil || jev.ValidateRequestForModel(work[0].model, state, questions) != nil {
		t.Fatal("shared state context overflow did not fall back to bounded inline questions")
	}
	// The same structure with shorter descriptions fits, so the budget, rather
	// than the size comparison, decided the fallback above.
	if _, _, _, ids := workRequest(build(20), jev.DefaultBudget()); ids == nil {
		t.Fatal("fitting shared catalog was not used")
	}
	// A tighter budget, as used after a provider overflow, falls back sooner.
	if _, _, _, ids := workRequest(build(20), jev.Budget{QuestionTokens: 4000, RequestTokens: 60000}); ids != nil {
		t.Fatal("tightened budget did not apply to the shared catalog")
	}
}

func TestCacheKeySeparatesExplicitOriginScopes(t *testing.T) {
	first, second := options(), options()
	first.Origin, second.Origin = "https://a.example", "https://b.example"
	if cacheKey("identical-projection", first) == cacheKey("identical-projection", second) {
		t.Fatal("origin-specific scopes reused the same cache entry")
	}
}
