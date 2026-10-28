package engine

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/jev"
)

type EngineRegistry struct {
	engines map[string]Engine
}

func EngineNewRegistry() *EngineRegistry {
	return &EngineRegistry{engines: map[string]Engine{"codex": &EngineCodex{}, "jev": &EngineJev{}}}
}

func (r *EngineRegistry) List() []EngineInfo {
	names := make([]string, 0, len(r.engines))
	for name := range r.engines {
		names = append(names, name)
	}
	sort.Strings(names)
	infos := make([]EngineInfo, 0, len(names))
	for _, name := range names {
		infos = append(infos, r.engines[name].Info())
	}
	return infos
}

func (r *EngineRegistry) Run(ctx context.Context, options EngineOptions, raw []byte) (EngineResult, error) {
	if options.Limits == (EngineLimits{}) {
		options.Limits = EngineDefaultLimits()
	}
	if err := EngineValidateLimits(options.Limits); err != nil {
		return EngineResult{}, err
	}
	if len(raw) > options.Limits.MaxInputBytes {
		return EngineResult{}, engineError("engine_input_limit", "input exceeds max-input-bytes")
	}
	if options.Task != EngineTaskTriage && options.Task != EngineTaskAdjudicate {
		return EngineResult{}, engineError("engine_invalid_task", "task must be triage or adjudicate")
	}
	name := strings.ToLower(strings.TrimSpace(options.Engine))
	if name == "" {
		name = "codex"
		if options.Task == EngineTaskAdjudicate {
			name = "jev"
		}
	}
	backend, exists := r.engines[name]
	if !exists {
		return EngineResult{}, engineError("engine_unknown", "unknown engine; use engine list")
	}
	// Provenance is checked before availability, so missing credentials cannot
	// obscure a prohibited self-adjudication or trigger a provider call.
	prepared, err := enginePrepare(options.Task, name, raw)
	if err != nil {
		return EngineResult{}, err
	}
	info := backend.Info()
	if (options.Task == EngineTaskTriage && !info.Capabilities.Freeform) || (options.Task == EngineTaskAdjudicate && !info.Capabilities.ConstrainedChoice) {
		return EngineResult{}, engineError("engine_capability", "selected engine does not support this typed task; Jev supports adjudicate only")
	}
	model := options.Model
	if model == "" {
		model = info.DefaultModel
	}
	if !engineValidModel(name, model) {
		return EngineResult{}, engineError("engine_invalid_model", "model is not supported by this engine; use engine list")
	}
	if options.Effort == "" {
		options.Effort = "medium"
	}
	if !engineOneOf(options.Effort, "low", "medium", "high", "xhigh", "max") {
		return EngineResult{}, engineError("engine_invalid_effort", "effort must be low, medium, high, xhigh, or max")
	}
	if !info.Available {
		return EngineResult{}, engineError("engine_unavailable", info.Reason)
	}
	encoded, err := sonic.Marshal(prepared.state())
	if err != nil || len(encoded) > options.Limits.MaxInputBytes {
		return EngineResult{}, engineError("engine_input_limit", "sanitized input exceeds max-input-bytes")
	}
	callCtx, cancel := context.WithTimeout(ctx, options.Limits.Timeout)
	defer cancel()
	response, callErr := backend.Evaluate(callCtx, EngineRequest{Task: options.Task, Model: model, Effort: options.Effort, Limits: options.Limits, input: prepared})
	result := EngineResult{
		EngineAssessment: EngineAssessment{Assessment: EngineAssessmentModel, Engine: name, Model: model, Timestamp: time.Now().UTC()},
		Task:             options.Task, Citations: []string{}, NeedsReview: true, Sanitization: prepared.sanitization,
	}
	if callCtx.Err() != nil {
		return engineInconclusive(result, "engine_timeout_or_canceled"), nil
	}
	if callErr != nil {
		var classified *EngineError
		if errors.As(callErr, &classified) {
			return engineInconclusive(result, classified.Code), nil
		}
		return engineInconclusive(result, "engine_call_failed"), nil
	}
	if len(response.Output) > options.Limits.MaxOutputBytes {
		return engineInconclusive(result, "engine_output_limit"), nil
	}
	if response.Model != "" {
		if !engineValidModel(name, response.Model) || (name == "codex" && response.Model != model) {
			return engineInconclusive(result, "engine_invalid_response"), nil
		}
		result.Model = response.Model
	}
	if response.Confidence != nil {
		if math.IsNaN(*response.Confidence) || math.IsInf(*response.Confidence, 0) || *response.Confidence < 0 || *response.Confidence > 1 {
			return engineInconclusive(result, "engine_invalid_response"), nil
		}
		result.Confidence, result.Probabilities = response.Confidence, response.Probabilities
	}
	if options.Task == EngineTaskTriage {
		var answer engineTriageAnswer
		if engineDecode(response.Output, &answer) != nil || len(answer.Proposals) == 0 || len(answer.Proposals) > EngineMaxProposals {
			return engineInconclusive(result, "engine_invalid_response"), nil
		}
		seen := map[string]bool{}
		for i, proposal := range answer.Proposals {
			family, ok := prepared.families[proposal.FamilyID]
			key := proposal.FamilyID + ":" + string(proposal.Oracle)
			if !ok || proposal.Rank != i+1 || !engineValidOracle(proposal.Oracle) || seen[key] || !engineValidProse(proposal.Hypothesis) || !engineValidProse(proposal.NextTest) {
				return engineInconclusive(result, "engine_invalid_response"), nil
			}
			seen[key] = true
			result.Proposals = append(result.Proposals, EngineProposal{EngineAssessment: result.EngineAssessment, Rank: proposal.Rank, FamilyID: family, Oracle: proposal.Oracle, Hypothesis: proposal.Hypothesis, NextTest: proposal.NextTest})
		}
		return result, nil
	}
	var answer engineAdjudicationAnswer
	if engineDecode(response.Output, &answer) != nil || !engineOneOf(string(answer.Verdict), string(EngineReal), string(EngineNotReal), string(EngineNeedsMoreEvidence)) || len(answer.Citations) == 0 || len(answer.Citations) > len(prepared.evidence) {
		return engineInconclusive(result, "engine_invalid_response"), nil
	}
	seen := map[string]bool{}
	for _, citation := range answer.Citations {
		original, ok := prepared.evidence[citation]
		if !ok || seen[citation] {
			return engineInconclusive(result, "engine_invalid_citation"), nil
		}
		seen[citation] = true
		result.Citations = append(result.Citations, original)
	}
	result.Verdict = answer.Verdict
	if result.Confidence != nil && *result.Confidence < jev.ReviewThreshold {
		result.Verdict, result.Reason = EngineNeedsMoreEvidence, "engine_low_confidence"
	}
	if result.Verdict != EngineNeedsMoreEvidence && !engineHasStableEvidence(prepared, seen) {
		result.Verdict, result.Reason = EngineNeedsMoreEvidence, "status_deltas_alone_are_insufficient"
	}
	return result, nil
}

func engineValidModel(name, model string) bool {
	if name == "codex" {
		return engineOneOf(model, "gpt-6-astra", "gpt-6.1-sol", "gpt-6-luna")
	}
	return name == "jev" && strings.HasPrefix(model, "jev-") && len(model) > 4 && engineModelPattern.MatchString(model)
}

func engineInconclusive(result EngineResult, reason string) EngineResult {
	result.Verdict, result.Reason, result.NeedsReview = EngineNeedsMoreEvidence, reason, true
	result.Proposals, result.Citations = nil, []string{}
	return result
}

func engineValidProse(value string) bool {
	if len(strings.TrimSpace(value)) == 0 || len(value) > 1024 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}

func engineHasStableEvidence(input *enginePrepared, cited map[string]bool) bool {
	for _, fact := range input.adjudicate.Evidence {
		if !cited[fact.ID] {
			continue
		}
		if len(fact.StableFieldChanges) > 0 {
			return true
		}
		for _, comparison := range fact.PrincipalComparisons {
			if comparison.Access != "unknown" && *comparison.SameResource {
				return true
			}
		}
	}
	return false
}
