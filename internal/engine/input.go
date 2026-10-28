package engine

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/jev"
)

var engineReferencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./:#-]{0,159}$`)
var engineNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
var engineModelPattern = regexp.MustCompile(`^jev-[A-Za-z0-9._-]{1,100}$`)

type enginePrepared struct {
	triage       *EngineTriageInput
	adjudicate   *EngineAdjudicateInput
	families     map[string]string
	evidence     map[string]string
	sanitization EngineSanitization
}

func (p *enginePrepared) state() any {
	if p.triage != nil {
		return p.triage
	}
	return struct {
		Oracle   EngineOracle         `json:"oracle"`
		Evidence []EngineEvidenceFact `json:"evidence"`
	}{p.adjudicate.Proposal.Oracle, p.adjudicate.Evidence}
}

func engineDecode(data []byte, target any) error {
	decoder := sonic.ConfigDefault.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}

func enginePrepare(task EngineTask, selected string, raw []byte) (*enginePrepared, error) {
	p := &enginePrepared{families: map[string]string{}, evidence: map[string]string{}, sanitization: EngineSanitization{Warnings: []string{}}}
	var tree map[string]any
	if sonic.Unmarshal(raw, &tree) != nil || tree == nil {
		return nil, engineError("engine_invalid_input", "input must be one typed JSON object")
	}
	if !engineDropSensitive(tree, &p.sanitization, 0) {
		return nil, engineError("engine_invalid_input", "input nesting exceeds 20 levels")
	}
	clean, err := sonic.Marshal(tree)
	if err != nil {
		return nil, engineError("engine_invalid_input", "cannot encode typed input")
	}
	switch task {
	case EngineTaskTriage:
		p.triage = &EngineTriageInput{}
		if engineDecode(clean, p.triage) != nil {
			return nil, engineError("engine_invalid_input", "triage requires endpoint-family facts matching its input schema; unknown fields are refused")
		}
		err = enginePrepareTriage(p)
	case EngineTaskAdjudicate:
		p.adjudicate = &EngineAdjudicateInput{}
		if engineDecode(clean, p.adjudicate) != nil {
			return nil, engineError("engine_invalid_input", "adjudicate requires a proposal and executed evidence matching its input schema; unknown fields are refused")
		}
		err = enginePrepareAdjudication(p, selected)
	default:
		return nil, engineError("engine_invalid_task", "task must be triage or adjudicate")
	}
	if err != nil {
		return nil, err
	}
	if p.sanitization.HeadersDropped > 0 {
		p.sanitization.Warnings = append(p.sanitization.Warnings, "Dropped all supplied headers, including any authentication or credential-bearing headers.")
	}
	if p.sanitization.CookiesDropped > 0 || p.sanitization.CredentialFieldsDropped > 0 {
		p.sanitization.Warnings = append(p.sanitization.Warnings, "Dropped supplied cookies and credential fields.")
	}
	if p.sanitization.BodiesDropped > 0 {
		p.sanitization.Warnings = append(p.sanitization.Warnings, "Dropped raw request and response bodies.")
	}
	if p.sanitization.VolatileChangesDropped > 0 {
		p.sanitization.Warnings = append(p.sanitization.Warnings, "Excluded changes to fields covered by the volatility mask.")
	}
	return p, nil
}

func engineDropSensitive(value any, report *EngineSanitization, depth int) bool {
	if depth > 20 {
		return false
	}
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			name := strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(key))
			switch {
			case strings.Contains(name, "header"):
				report.HeadersDropped++
			case strings.Contains(name, "cookie"):
				report.CookiesDropped++
			case name == "body" || name == "rawbody" || name == "requestbody" || name == "responsebody" || name == "postdata" || name == "rawrequest" || name == "rawresponse":
				report.BodiesDropped++
			case strings.Contains(name, "authorization") || strings.Contains(name, "password") || strings.Contains(name, "secret") || strings.Contains(name, "apikey") || strings.Contains(name, "token") || name == "auth" || name == "credentials":
				report.CredentialFieldsDropped++
			default:
				if !engineDropSensitive(child, report, depth+1) {
					return false
				}
				continue
			}
			delete(node, key)
		}
	case []any:
		for _, child := range node {
			if !engineDropSensitive(child, report, depth+1) {
				return false
			}
		}
	}
	return true
}

func enginePrepareTriage(p *enginePrepared) error {
	invalid := func() error {
		return engineError("engine_invalid_input", "triage requires 1..32 unique families, HTTP metadata, positive sample_count, status_codes, and fixed-enum signals")
	}
	if len(p.triage.Families) == 0 || len(p.triage.Families) > EngineMaxFamilies {
		return invalid()
	}
	seen := map[string]bool{}
	aliases := map[string]string{}
	for i := range p.triage.Families {
		family := &p.triage.Families[i]
		if !engineReferencePattern.MatchString(family.FamilyID) || seen[family.FamilyID] || family.SampleCount < 1 || len(family.StatusCodes) == 0 || len(family.StatusCodes) > 16 || len(family.Principals) > 16 || len(family.Signals) > 16 {
			return invalid()
		}
		seen[family.FamilyID] = true
		alias := fmt.Sprintf("family_%d", i+1)
		p.families[alias], family.FamilyID = family.FamilyID, alias
		p.sanitization.ReferencesAliased++
		for _, code := range family.StatusCodes {
			if code < 100 || code > 599 {
				return invalid()
			}
		}
		for _, signal := range family.Signals {
			switch signal {
			case "object_reference", "state_changing", "auth_present", "unauthenticated_capture", "user_controlled_input", "rate_limit_observed", "sensitive_resource", "missing_csrf", "input_reflected":
			default:
				return invalid()
			}
		}
		if family.Signals == nil {
			family.Signals = []string{}
		}
		if (family.URL == "") == (family.Route == "") {
			return engineError("engine_invalid_input", "each family requires exactly one of route or url")
		}
		location := family.URL
		if family.Route != "" {
			if !strings.HasPrefix(family.Route, "/") || strings.HasPrefix(family.Route, "//") {
				return engineError("engine_invalid_input", "family route must be an absolute path")
			}
			location = "https://engine.invalid" + family.Route
		}
		metadata, err := jev.SanitizeTraffic(jev.TrafficInput{Method: family.Method, URL: location, ContentType: family.ContentType, ResourceType: family.ResourceType})
		if err != nil || metadata.Method == "OTHER" {
			return invalid()
		}
		// Hostnames, query values, userinfo and fragments are unnecessary to rank
		// a family. Reuse Jev's allowlisted path segments, including its redaction.
		family.Method, family.Route, family.URL = metadata.Method, metadata.Path, ""
		family.ContentType, family.ResourceType = metadata.ContentType, metadata.ResourceType
		p.sanitization.RoutesScrubbed++
		for j, principal := range family.Principals {
			if !engineReferencePattern.MatchString(principal) {
				return invalid()
			}
			family.Principals[j] = engineAlias(aliases, "principal", principal, &p.sanitization)
		}
	}
	return nil
}

func enginePrepareAdjudication(p *enginePrepared, selected string) error {
	input := p.adjudicate
	proposer := strings.ToLower(strings.TrimSpace(input.Proposal.Engine))
	if !engineNamePattern.MatchString(proposer) {
		return engineError("engine_independence_required", "independence requires proposal.engine to name the engine that produced the proposal")
	}
	if proposer == selected {
		return engineError("engine_independence_required", "independence requires an adjudicator engine different from proposal.engine; changing only the model is insufficient")
	}
	if !engineValidOracle(input.Proposal.Oracle) || (input.Proposal.Assessment != "" && input.Proposal.Assessment != EngineAssessmentModel) {
		return engineError("engine_invalid_input", "proposal requires a fixed-enum oracle and must not claim to be observed evidence")
	}
	if len(input.Evidence) == 0 || len(input.Evidence) > EngineMaxEvidence {
		return engineError("engine_invalid_input", "adjudicate requires 1..16 executed evidence summaries")
	}
	// The judge receives the oracle, not the proposer's narrative, confidence,
	// engine or model. Those influence neither the evidence nor its selection.
	input.Proposal = EngineProposal{Oracle: input.Proposal.Oracle}
	seen := map[string]bool{}
	principals, resources, fields := map[string]string{}, map[string]string{}, map[string]string{}
	for i := range input.Evidence {
		fact := &input.Evidence[i]
		// Name the offending field. A caller assembling this by hand otherwise
		// has to bisect five separate requirements from one message.
		switch {
		case !engineReferencePattern.MatchString(fact.ID):
			return engineError("engine_invalid_input", fmt.Sprintf("evidence[%d].id %q must match %s", i, fact.ID, engineReferencePattern))
		case seen[fact.ID]:
			return engineError("engine_invalid_input", fmt.Sprintf("evidence[%d].id %q is duplicated; each summary needs a unique pointer", i, fact.ID))
		case fact.Assessment != "executed_and_observed":
			return engineError("engine_invalid_input", fmt.Sprintf("evidence[%d].assessment is %q; adjudication accepts only executed_and_observed facts", i, fact.Assessment))
		case !engineReferencePattern.MatchString(fact.Subject):
			return engineError("engine_invalid_input", fmt.Sprintf("evidence[%d].subject %q must be an identifier matching %s, not prose", i, fact.Subject, engineReferencePattern))
		case !engineReferencePattern.MatchString(fact.Resource):
			return engineError("engine_invalid_input", fmt.Sprintf("evidence[%d].resource %q must be an identifier matching %s, not prose", i, fact.Resource, engineReferencePattern))
		}
		if len(fact.StatusDeltas) > 16 || len(fact.StableFieldChanges) > 32 || len(fact.VolatilityMask) > 64 || len(fact.PrincipalComparisons) > 16 || len(fact.StatusDeltas)+len(fact.StableFieldChanges)+len(fact.PrincipalComparisons) == 0 {
			return engineError("engine_invalid_input", "evidence facts exceed their bounds or contain no observations")
		}
		seen[fact.ID] = true
		alias := fmt.Sprintf("evidence_%d", i+1)
		p.evidence[alias], fact.ID = fact.ID, alias
		p.sanitization.ReferencesAliased++
		fact.Subject = engineAlias(principals, "principal", fact.Subject, &p.sanitization)
		fact.Resource = engineAlias(resources, "resource", fact.Resource, &p.sanitization)
		for _, delta := range fact.StatusDeltas {
			if delta.Baseline < 100 || delta.Baseline > 599 || delta.Observed < 100 || delta.Observed > 599 {
				return engineError("engine_invalid_input", "status deltas require HTTP codes in 100..599")
			}
		}
		mask := append([]string(nil), fact.VolatilityMask...)
		for j, field := range fact.VolatilityMask {
			if !engineValidField(field) {
				return engineError("engine_invalid_input", "volatility_mask requires JSON pointers of at most 256 bytes")
			}
			fact.VolatilityMask[j] = engineAlias(fields, "field", field, &p.sanitization)
		}
		stable := make([]EngineFieldChange, 0, len(fact.StableFieldChanges))
		for _, change := range fact.StableFieldChanges {
			if !engineValidField(change.Field) || !engineOneOf(change.Change, "equal", "different", "added", "removed") {
				return engineError("engine_invalid_input", "stable_field_changes requires a JSON pointer and change equal, different, added, or removed; raw values are forbidden")
			}
			if engineFieldMasked(change.Field, mask) {
				p.sanitization.VolatileChangesDropped++
				continue
			}
			change.Field = engineAlias(fields, "field", change.Field, &p.sanitization)
			stable = append(stable, change)
		}
		fact.StableFieldChanges = stable
		for j := range fact.PrincipalComparisons {
			comparison := &fact.PrincipalComparisons[j]
			if !engineReferencePattern.MatchString(comparison.BaselinePrincipal) || !engineReferencePattern.MatchString(comparison.CandidatePrincipal) || comparison.SameResource == nil || comparison.CandidateAuthorized == nil || !engineOneOf(comparison.Access, "allowed", "denied", "unknown") {
				return engineError("engine_invalid_input", "principal comparisons require named principals, same_resource, candidate_authorized, and access allowed, denied, or unknown")
			}
			comparison.BaselinePrincipal = engineAlias(principals, "principal", comparison.BaselinePrincipal, &p.sanitization)
			comparison.CandidatePrincipal = engineAlias(principals, "principal", comparison.CandidatePrincipal, &p.sanitization)
		}
	}
	return nil
}

func engineAlias(aliases map[string]string, prefix, original string, report *EngineSanitization) string {
	if alias, ok := aliases[original]; ok {
		return alias
	}
	alias := fmt.Sprintf("%s_%d", prefix, len(aliases)+1)
	aliases[original] = alias
	report.ReferencesAliased++
	return alias
}

func engineValidField(field string) bool {
	if len(field) > 256 || (field != "" && !strings.HasPrefix(field, "/")) || strings.ContainsAny(field, "\r\n\x00") {
		return false
	}
	for i := 0; i < len(field); i++ {
		if field[i] == '~' {
			if i+1 >= len(field) || (field[i+1] != '0' && field[i+1] != '1') {
				return false
			}
			i++
		}
	}
	return true
}

func engineFieldMasked(field string, mask []string) bool {
	for _, volatile := range mask {
		if volatile == "" || field == volatile || strings.HasPrefix(field, volatile+"/") || strings.HasPrefix(volatile, field+"/") {
			return true
		}
	}
	return false
}

func engineOneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
