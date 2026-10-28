package engine

import (
	"context"
	"time"
)

type EngineTask string
type EngineOracle string
type EngineVerdict string

const (
	EngineTaskTriage     EngineTask = "triage"
	EngineTaskAdjudicate EngineTask = "adjudicate"

	EngineReal              EngineVerdict = "real"
	EngineNotReal           EngineVerdict = "not_real"
	EngineNeedsMoreEvidence EngineVerdict = "needs_more_evidence"

	EngineAssessmentModel = "model_assessed"
	EngineDefaultTimeout  = 60 * time.Second
	EngineMaxTimeout      = 2 * time.Minute
	EngineMaxInputBytes   = 64 * 1024
	EngineMaxOutputBytes  = 64 * 1024
	EngineDefaultOutput   = 32 * 1024
	EngineMaxFamilies     = 32
	EngineMaxEvidence     = 16
	EngineMaxProposals    = 10
)

var engineOracles = []EngineOracle{
	"authz_cross_principal", "authz_unauthenticated", "object_reference",
	"input_reflection", "mutation_side_effect", "rate_or_lockout", "information_disclosure",
}

type EngineCapabilities struct {
	ConstrainedChoice bool `json:"constrained_choice"`
	Freeform          bool `json:"freeform"`
}

type EngineInfo struct {
	Name         string             `json:"name"`
	Models       []string           `json:"models"`
	DefaultModel string             `json:"default_model"`
	Capabilities EngineCapabilities `json:"capabilities"`
	Tasks        []EngineTask       `json:"tasks"`
	Available    bool               `json:"available"`
	Reason       string             `json:"reason,omitempty"`
}

// Engine implementations receive only inputs prepared by the registry. Keeping
// the state private prevents a second, unsanitized free-prompt entry point.
type Engine interface {
	Info() EngineInfo
	Evaluate(context.Context, EngineRequest) (EngineResponse, error)
}

type EngineRequest struct {
	Task   EngineTask
	Model  string
	Effort string
	Limits EngineLimits
	input  *enginePrepared
}

type EngineResponse struct {
	Output        []byte
	Model         string
	Confidence    *float64
	Probabilities map[string]float64
}

type EngineLimits struct {
	Timeout        time.Duration `json:"timeout"`
	MaxInputBytes  int           `json:"max_input_bytes"`
	MaxOutputBytes int           `json:"max_output_bytes"`
}

type EngineOptions struct {
	Task   EngineTask
	Engine string
	Model  string
	Effort string
	Limits EngineLimits
}

type EngineAssessment struct {
	Assessment string    `json:"assessment"`
	Engine     string    `json:"engine"`
	Model      string    `json:"model"`
	Timestamp  time.Time `json:"timestamp"`
}

// These structs are the input schemas. Strings outside enums are opaque local
// references or route metadata, never instructions or captured content.
type EngineTriageInput struct {
	Families []EngineFamilyFact `json:"families"`
}

type EngineFamilyFact struct {
	FamilyID     string   `json:"family_id"`
	Method       string   `json:"method"`
	Route        string   `json:"route,omitempty"`
	URL          string   `json:"url,omitempty"`
	ContentType  string   `json:"content_type,omitempty"`
	ResourceType string   `json:"resource_type,omitempty"`
	StatusCodes  []int    `json:"status_codes"`
	SampleCount  int      `json:"sample_count"`
	Principals   []string `json:"principals,omitempty"`
	Signals      []string `json:"signals"`
}

type EngineAdjudicateInput struct {
	Proposal EngineProposal       `json:"proposal"`
	Evidence []EngineEvidenceFact `json:"evidence"`
}

type EngineEvidenceFact struct {
	ID                   string                      `json:"id"`
	Assessment           string                      `json:"assessment"`
	Subject              string                      `json:"subject"`
	Resource             string                      `json:"resource"`
	StatusDeltas         []EngineStatusDelta         `json:"status_deltas"`
	StableFieldChanges   []EngineFieldChange         `json:"stable_field_changes"`
	VolatilityMask       []string                    `json:"volatility_mask"`
	PrincipalComparisons []EnginePrincipalComparison `json:"principal_comparisons"`
}

type EngineStatusDelta struct {
	Baseline int `json:"baseline"`
	Observed int `json:"observed"`
}

type EngineFieldChange struct {
	Field  string `json:"field"`
	Change string `json:"change"`
}

type EnginePrincipalComparison struct {
	BaselinePrincipal   string `json:"baseline_principal"`
	CandidatePrincipal  string `json:"candidate_principal"`
	SameResource        *bool  `json:"same_resource"`
	CandidateAuthorized *bool  `json:"candidate_authorized"`
	Access              string `json:"access"`
}

type EngineProposal struct {
	EngineAssessment
	Rank       int          `json:"rank"`
	FamilyID   string       `json:"family_id"`
	Oracle     EngineOracle `json:"oracle"`
	Hypothesis string       `json:"hypothesis"`
	NextTest   string       `json:"next_test"`
}

type EngineSanitization struct {
	HeadersDropped          int      `json:"headers_dropped"`
	CookiesDropped          int      `json:"cookies_dropped"`
	BodiesDropped           int      `json:"bodies_dropped"`
	CredentialFieldsDropped int      `json:"credential_fields_dropped"`
	RoutesScrubbed          int      `json:"routes_scrubbed"`
	ReferencesAliased       int      `json:"references_aliased"`
	VolatileChangesDropped  int      `json:"volatile_changes_dropped"`
	Warnings                []string `json:"warnings"`
}

type EngineResult struct {
	EngineAssessment
	Task          EngineTask         `json:"task"`
	Verdict       EngineVerdict      `json:"verdict,omitempty"`
	Proposals     []EngineProposal   `json:"proposals,omitempty"`
	Citations     []string           `json:"citations"`
	NeedsReview   bool               `json:"needs_review"`
	Reason        string             `json:"reason,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Sanitization  EngineSanitization `json:"sanitization"`
}

type EngineError struct {
	Code    string
	Message string
}

func (e *EngineError) Error() string { return e.Message }

func engineError(code, message string) error { return &EngineError{Code: code, Message: message} }

func EngineDefaultLimits() EngineLimits {
	return EngineLimits{Timeout: EngineDefaultTimeout, MaxInputBytes: EngineMaxInputBytes, MaxOutputBytes: EngineDefaultOutput}
}

func EngineValidateLimits(limits EngineLimits) error {
	if limits.Timeout <= 0 || limits.Timeout > EngineMaxTimeout || limits.MaxInputBytes <= 0 || limits.MaxInputBytes > EngineMaxInputBytes || limits.MaxOutputBytes <= 0 || limits.MaxOutputBytes > EngineMaxOutputBytes {
		return engineError("engine_invalid_limits", "engine limits require a timeout in (0, 2m], input bytes in 1..65536, and output bytes in 1..65536")
	}
	return nil
}

func engineValidOracle(oracle EngineOracle) bool {
	for _, allowed := range engineOracles {
		if oracle == allowed {
			return true
		}
	}
	return false
}
