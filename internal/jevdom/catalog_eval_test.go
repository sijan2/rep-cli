package jevdom

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/jev"
)

type catalogTrial struct {
	Case         string              `json:"case"`
	Source       string              `json:"source"`
	Encoding     string              `json:"encoding"`
	RequestBytes int                 `json:"request_bytes_without_model"`
	Expected     []int64             `json:"expected_backend_ids"`
	Selected     []int64             `json:"selected_backend_ids"`
	Correct      []bool              `json:"selection_correct"`
	Confidence   []float64           `json:"confidence"`
	NeedsReview  []bool              `json:"needs_review"`
	Usage        jev.Usage           `json:"usage"`
	DurationMS   float64             `json:"provider_duration_ms"`
	Transport    []jev.RequestTiming `json:"transport"`
	Error        string              `json:"error,omitempty"`
}

// This deliberately compares the previous inline batch prompt to the shared
// catalog over identical frozen observations. It is opt-in and billed, just
// like TestFrozenObservationLiveEvaluation. Browser execution is not measured.
func TestSharedCatalogLiveEvaluation(t *testing.T) {
	if os.Getenv("REP_JEV_EVAL_LIVE") != "1" {
		t.Skip("live provider evaluation requires explicit opt-in")
	}
	model := os.Getenv("REP_JEV_EVAL_MODEL")
	if !validModel.MatchString(model) || model == "jev-latest" {
		t.Fatal("REP_JEV_EVAL_MODEL must name a pinned model")
	}
	config, err := jev.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.Model = model
	client := jev.NewClient(config)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	corpus := frozenCorpus(t)
	cases := []struct {
		name, source string
		page         fakePage
		goals        []string
		expected     []int64
	}{
		{"products-before", corpus[0].Source, corpus[0].Page, []string{corpus[0].Goal, corpus[2].Goal, corpus[3].Goal}, []int64{12, 16, 0}},
		{"products-after", corpus[1].Source, corpus[1].Page, []string{corpus[0].Goal, corpus[2].Goal, corpus[3].Goal}, []int64{16, 12, 0}},
		{"duplicate-groups", corpus[4].Source, corpus[4].Page, []string{"Continue in Billing", "Continue in Shipping", "Download invoice"}, []int64{3, 5, 0}},
		{"long-label-review", corpus[5].Source, corpus[5].Page, []string{corpus[5].Goal, "Download invoice", corpus[5].Goal}, []int64{1, 0, 1}},
		{"103-controls", corpus[6].Source, corpus[6].Page, []string{"Find Item 0", "Find Item 50", "Find Item 102"}, []int64{1, 51, 103}},
		{"103-controls-repeat", corpus[6].Source, corpus[6].Page, []string{"Find Item 0", "Find Item 50", "Find Item 102"}, []int64{1, 51, 103}},
	}
	report := struct {
		Model                 string         `json:"model"`
		MeasuresModelAccuracy bool           `json:"measures_model_accuracy"`
		MeasuresExecution     bool           `json:"measures_execution"`
		Trials                []catalogTrial `json:"trials"`
	}{Model: model, MeasuresModelAccuracy: true}
	for index, item := range cases {
		snapshot, captureErr := Capture(ctx, browserFor(item.page), 12, "controls", "")
		if captureErr != nil {
			t.Fatal(captureErr)
		}
		var work []choiceWork
		for i, goal := range item.goals {
			work = append(work, choiceWork{owner: i, goal: goal, model: model, stage: "final", candidates: snapshot.Candidates})
		}
		encodings := []string{"inline", "shared_catalog"}
		if index%2 != 0 {
			slices.Reverse(encodings)
		}
		for _, encoding := range encodings {
			state, questions, names := inlineWorkRequest(work)
			var ids map[string]string
			if encoding == "shared_catalog" {
				state, questions, names, ids = workRequest(work, jev.DefaultBudget())
				if ids == nil {
					t.Fatal("comparison fixture unexpectedly requires inline fallback")
				}
			}
			trial := catalogTrial{Case: item.name, Source: item.source, Encoding: encoding, RequestBytes: requestSize(state, questions), Expected: item.expected}
			started := time.Now()
			response, callErr := client.Evaluate(ctx, state, questions)
			trial.DurationMS = milliseconds(started)
			trial.Usage, trial.Transport = response.Usage, response.Timing
			if callErr != nil {
				trial.Error = callErr.Error()
			} else {
				for i, name := range names {
					answer, valid := canonicalAnswer(response.Answers[name], questions[name], ids)
					valid = valid && validAnswer(answer, candidateIDs(snapshot.Candidates))
					var selected int64
					for _, candidate := range snapshot.Candidates {
						if candidate.ID == answer.Choice {
							selected = candidate.BackendDOMNodeID
						}
					}
					correct := valid && selected == item.expected[i]
					review := !valid || answer.Confidence < 0.8 || snapshot.Coverage.TextTruncated > 0
					trial.Selected = append(trial.Selected, selected)
					trial.Correct = append(trial.Correct, correct)
					trial.Confidence = append(trial.Confidence, answer.Confidence)
					trial.NeedsReview = append(trial.NeedsReview, review)
					if !correct {
						t.Errorf("%s/%s goal %d: selected=%d expected=%d confidence=%.2f valid=%t", item.name, encoding, i, selected, item.expected[i], answer.Confidence, valid)
					}
				}
			}
			report.Trials = append(report.Trials, trial)
			t.Logf("%s/%s bytes=%d input=%d output=%d %.1fms confidence=%v", item.name, encoding, trial.RequestBytes, trial.Usage.InputTokens, trial.Usage.OutputTokens, trial.DurationMS, trial.Confidence)
			if trial.Error != "" {
				t.Error(trial.Error)
			}
		}
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("REP_JEV_EVAL_OUTPUT"); path != "" {
		if err = os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
