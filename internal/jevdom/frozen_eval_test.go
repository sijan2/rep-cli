package jevdom

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/jev"
)

// This is a frozen-observation selection evaluation, not a browser execution
// benchmark. The default provider is a deterministic test double; it establishes
// harness/packing correctness and must never be reported as Jev accuracy.
type frozenCase struct {
	ID, Source, Goal     string
	Page                 fakePage
	AcceptableBackendIDs []int64 // empty means the independently labeled answer is none
}

type frozenTrial struct {
	Case       string    `json:"case"`
	Source     string    `json:"source"`
	Strategy   string    `json:"strategy"`
	Status     string    `json:"status"`
	Expected   []int64   `json:"acceptable_backend_ids"`
	Selected   int64     `json:"selected_backend_id"`
	Correct    bool      `json:"selection_correct"`
	Accepted   bool      `json:"automatically_accepted"`
	Confidence float64   `json:"confidence"`
	DurationMS float64   `json:"duration_ms"`
	Requests   int       `json:"provider_requests"`
	Usage      jev.Usage `json:"usage"`
	Model      string    `json:"model"`
	Error      string    `json:"error,omitempty"`
}

type frozenMetrics struct {
	Trials         int     `json:"trials"`
	Correct        int     `json:"correct"`
	Accepted       int     `json:"accepted"`
	AcceptedErrors int     `json:"accepted_errors"`
	Failures       int     `json:"failures"`
	Requests       int     `json:"provider_requests"`
	InputTokens    int     `json:"input_tokens"`
	OutputTokens   int     `json:"output_tokens"`
	P50MS          float64 `json:"p50_ms"`
	P95MS          float64 `json:"p95_ms"`
}

type frozenReport struct {
	Provider              string                   `json:"provider"`
	MeasuresModelAccuracy bool                     `json:"measures_model_accuracy"`
	MeasuresExecution     bool                     `json:"measures_execution"`
	Trials                []frozenTrial            `json:"trials"`
	Strategies            map[string]frozenMetrics `json:"strategies"`
}

func frozenCorpus(t *testing.T) []frozenCase {
	t.Helper()
	data, err := os.ReadFile("testdata/chromium-products.json")
	if err != nil {
		t.Fatal(err)
	}
	type capture struct {
		FrameTree frameTree        `json:"frameTree"`
		Nodes     []map[string]any `json:"nodes"`
	}
	var source struct{ Before, After capture }
	if err = json.Unmarshal(data, &source); err != nil {
		t.Fatal(err)
	}
	convert := func(c capture) fakePage {
		return fakePage{frames: []fakeFrame{{id: c.FrameTree.Frame.ID, loader: c.FrameTree.Frame.LoaderID, url: c.FrameTree.Frame.URL, nodes: c.Nodes}}}
	}
	before, after := convert(source.Before), convert(source.After)
	duplicate := page(0)
	duplicate.frames[0].nodes = append(duplicate.frames[0].nodes, node(2, "group", "Billing", "1000000"), node(3, "button", "Continue", "2"), node(4, "group", "Shipping", "1000000"), node(5, "button", "Continue", "4"))
	long := page(1)
	longName := "Open " + strings.Repeat("long label ", 20)
	long.frames[0].nodes[1]["name"] = map[string]any{"value": longName}
	return []frozenCase{
		{"products-basic-before", "real_chromium_generated_fixture", "Buy Widget Basic $10", before, []int64{12}},
		{"products-basic-after", "real_chromium_generated_fixture", "Buy Widget Basic $10", after, []int64{16}},
		{"products-pro-before", "real_chromium_generated_fixture", "Buy Widget Pro $20", before, []int64{16}},
		{"products-no-match", "real_chromium_generated_fixture", "Buy Widget Ultra $50", before, nil},
		{"duplicate-billing", "synthetic_accessibility_fixture", "Continue in Billing", duplicate, []int64{3}},
		{"long-label-review", "synthetic_accessibility_fixture", longName, long, []int64{1}},
		{"103-controls", "synthetic_accessibility_fixture", "Find Item 102", page(103), []int64{103}},
		{"103-no-match", "synthetic_accessibility_fixture", "Find Download invoice", page(103), nil},
	}
}

func runFrozenEvaluation(ctx context.Context, evaluator Evaluator, corpus []frozenCase, provider string, live bool) frozenReport {
	report := frozenReport{Provider: provider, MeasuresModelAccuracy: live, Strategies: map[string]frozenMetrics{}}
	durations := map[string][]float64{}
	for index, item := range corpus {
		strategies := []string{"auto", "legacy"}
		if index%2 != 0 {
			slices.Reverse(strategies)
		}
		for _, strategy := range strategies {
			opt := DefaultOptions()
			opt.TabID = 12
			opt.Goal = item.Goal
			opt.Limit = 480
			opt.NoCache = true
			opt.ObservationMode = "legacy"
			opt.Strategy = strategy
			if validModel.MatchString(provider) {
				opt.Model = provider
			}
			started := time.Now()
			result, err := (Selector{Browser: browserFor(item.Page), Evaluator: evaluator}).Select(ctx, opt)
			trial := frozenTrial{Case: item.ID, Source: item.Source, Strategy: strategy, Status: result.Status, Expected: item.AcceptableBackendIDs, Confidence: result.Confidence, DurationMS: milliseconds(started), Requests: result.Timing.Requests, Usage: result.Usage, Model: result.Model}
			if err != nil {
				trial.Error = err.Error()
			} else {
				if result.Selected != nil {
					trial.Selected = result.Selected.BackendDOMNodeID
					trial.Correct = slices.Contains(item.AcceptableBackendIDs, trial.Selected)
				} else {
					trial.Correct = len(item.AcceptableBackendIDs) == 0 && len(result.Decisions) > 0 && result.Decisions[len(result.Decisions)-1].Answer.Choice == "none"
				}
				trial.Accepted = !result.NeedsReview && (result.Status == "selected" || result.Status == "no_match")
			}
			report.Trials = append(report.Trials, trial)
			metrics := report.Strategies[strategy]
			metrics.Trials++
			metrics.Requests += trial.Requests
			metrics.InputTokens += trial.Usage.InputTokens
			metrics.OutputTokens += trial.Usage.OutputTokens
			if trial.Correct {
				metrics.Correct++
			}
			if trial.Accepted {
				metrics.Accepted++
				if !trial.Correct {
					metrics.AcceptedErrors++
				}
			}
			if trial.Error != "" {
				metrics.Failures++
			}
			report.Strategies[strategy] = metrics
			durations[strategy] = append(durations[strategy], trial.DurationMS)
		}
	}
	for strategy, values := range durations {
		slices.Sort(values)
		metrics := report.Strategies[strategy]
		metrics.P50MS = values[(len(values)-1)/2]
		metrics.P95MS = values[(len(values)*95+99)/100-1]
		report.Strategies[strategy] = metrics
	}
	return report
}

func TestFrozenObservationEvaluationHarness(t *testing.T) {
	evaluator := &packingEvaluator{t: t, match: func(goal string, c visibleCandidate) bool {
		if strings.HasPrefix(goal, "Find Item ") {
			return goal == "Find "+c.Name
		}
		if !strings.Contains(goal, c.Name) {
			return false
		}
		if len(c.ContextRelations) == 0 {
			return true
		}
		for _, r := range c.ContextRelations {
			if strings.Contains(goal, r.Name) {
				return true
			}
		}
		return false
	}}
	report := runFrozenEvaluation(context.Background(), evaluator, frozenCorpus(t), "deterministic_test_double", false)
	if len(report.Trials) != 16 || report.MeasuresModelAccuracy || report.MeasuresExecution {
		t.Fatal("incorrect harness provenance")
	}
	for strategy, metrics := range report.Strategies {
		if metrics.Failures != 0 || metrics.AcceptedErrors != 0 || metrics.Accepted >= metrics.Trials {
			t.Fatalf("harness did not preserve correctness and long-label abstention: %s %+v", strategy, metrics)
		}
	}
	if report.Strategies["auto"].Requests >= report.Strategies["legacy"].Requests {
		t.Fatal("comparison did not exercise strategy request-count difference")
	}
	for _, trial := range report.Trials {
		if trial.Case == "long-label-review" && trial.Accepted {
			t.Fatal("truncated evidence accepted without review")
		}
	}
}

// Opt in explicitly: REP_JEV_EVAL_LIVE=1 REP_JEV_EVAL_MODEL=<pinned model>
// REP_JEV_EVAL_OUTPUT=/private/path/report.json go test ./internal/jevdom
// -run '^TestFrozenObservationLiveEvaluation$' -count=1 -v
// This performs billed requests; it is never run by the ordinary test suite.
func TestFrozenObservationLiveEvaluation(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	report := runFrozenEvaluation(ctx, jev.NewClient(config), frozenCorpus(t), model, true)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("REP_JEV_EVAL_OUTPUT"); path != "" {
		if err = os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, strategy := range []string{"auto", "legacy"} {
		m := report.Strategies[strategy]
		t.Logf("%s: correct=%d/%d accepted=%d accepted_errors=%d failures=%d requests=%d p50=%.1fms p95=%.1fms", strategy, m.Correct, m.Trials, m.Accepted, m.AcceptedErrors, m.Failures, m.Requests, m.P50MS, m.P95MS)
	}
	for _, trial := range report.Trials {
		if trial.Error != "" {
			t.Error(fmt.Sprintf("%s/%s: %s", trial.Case, trial.Strategy, trial.Error))
		}
	}
}
