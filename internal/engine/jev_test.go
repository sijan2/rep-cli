package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/jev"
)

type engineTestJevFunc func(context.Context, any, map[string]jev.Question) (jev.Evaluation, error)

func (f engineTestJevFunc) Evaluate(ctx context.Context, state any, questions map[string]jev.Question) (jev.Evaluation, error) {
	return f(ctx, state, questions)
}

func TestEngineJevDelegatesBoundedChoices(t *testing.T) {
	for _, tc := range []struct {
		name       string
		confidence float64
		cite       string
		invalid    bool
		want       EngineVerdict
	}{
		{"confident cited verdict", 0.94, "cite", false, EngineReal},
		{"uncertain verdict", 0.70, "cite", false, EngineNeedsMoreEvidence},
		{"uncited verdict", 0.94, "omit", false, EngineNeedsMoreEvidence},
		{"client rejects response", 0.94, "cite", true, EngineNeedsMoreEvidence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			backend := &EngineJev{
				loadConfig: func() (jev.Config, error) { return jev.Config{Model: "jev-latest", APIKey: "TEST_KEY"}, nil },
				newClient: func(config jev.Config) engineJevEvaluator {
					if config.Model != "jev-1.13.0" || config.APIKey != "TEST_KEY" {
						t.Fatalf("configuration not delegated: %+v", config)
					}
					return engineTestJevFunc(func(ctx context.Context, state any, questions map[string]jev.Question) (jev.Evaluation, error) {
						calls++
						if _, exists := ctx.Deadline(); !exists {
							t.Fatal("missing overall request deadline")
						}
						if len(questions) != 2 || questions["verdict"].Type != "choice" || len(questions["verdict"].Criteria) != 3 || len(questions["cite_1"].Criteria) != 2 {
							t.Fatalf("not constrained choices: %+v", questions)
						}
						if strings.Contains(questions["verdict"].Instructions, "JSON") {
							t.Fatal("constrained-choice Jev must not be asked to generate JSON or prose")
						}
						for _, verdict := range []string{"real", "not_real", "needs_more_evidence"} {
							if questions["verdict"].Criteria[verdict] == "" {
								t.Fatalf("missing choice %s", verdict)
							}
						}
						encoded, _ := sonic.Marshal(state)
						if strings.Contains(string(encoded), "seeded-order") || strings.Contains(string(encoded), "codex") {
							t.Fatalf("Jev received unprepared input: %s", encoded)
						}
						if tc.invalid {
							return jev.Evaluation{}, &jev.Error{Code: jev.CodeInvalidResponse, Message: "unparseable provider answer"}
						}
						return jev.Evaluation{Model: "jev-1.13.0", Answers: map[string]jev.ChoiceAnswer{
							"verdict": {Type: "choice", Choice: "real", Confidence: tc.confidence, Probabilities: map[string]float64{"real": 0.9, "not_real": 0.02, "needs_more_evidence": 0.08}},
							"cite_1":  {Type: "choice", Choice: tc.cite, Confidence: 0.95},
						}}, nil
					})
				},
			}
			registry := &EngineRegistry{engines: map[string]Engine{"jev": backend}}
			result, err := registry.Run(context.Background(), EngineOptions{Task: EngineTaskAdjudicate, Model: "jev-1.13.0"}, []byte(engineAdjudicationFixture))
			if err != nil || result.Verdict != tc.want || result.Assessment != EngineAssessmentModel || calls != 1 || result.Model != "jev-1.13.0" {
				t.Fatalf("unexpected Jev result: %+v %v calls=%d", result, err, calls)
			}
			if !tc.invalid && (result.Confidence == nil || *result.Confidence != tc.confidence || result.Probabilities["real"] != 0.9) {
				t.Fatalf("provider confidence or probabilities changed: %+v", result)
			}
		})
	}
}

func TestEngineJevCannotProposeFreeform(t *testing.T) {
	registry := EngineNewRegistry()
	_, err := registry.Run(context.Background(), EngineOptions{Task: EngineTaskTriage, Engine: "jev"}, []byte(engineTriageFixture))
	if engineTestCode(err) != "engine_capability" {
		t.Fatalf("Jev freeform task was not refused: %v", err)
	}
}
