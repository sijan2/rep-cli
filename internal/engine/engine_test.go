package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
)

const engineTriageFixture = `{"families":[{"family_id":"orders-family","method":"GET","route":"/api/orders/{id}","sample_count":4,"status_codes":[200,403],"signals":["object_reference"],"principals":["alice"]}]}`
const engineAdjudicationFixture = `{"proposal":{"engine":"codex","model":"gpt-6.1-sol","oracle":"authz_cross_principal"},"evidence":[{"id":"replay:42","assessment":"executed_and_observed","subject":"bob","resource":"seeded-order","status_deltas":[{"baseline":403,"observed":200}],"stable_field_changes":[{"field":"/owner","change":"different"}],"volatility_mask":["/timestamp"],"principal_comparisons":[{"baseline_principal":"alice","candidate_principal":"bob","same_resource":true,"candidate_authorized":false,"access":"allowed"}]}]}`
const engineTriageResponse = `{"proposals":[{"rank":1,"family_id":"family_1","oracle":"object_reference","hypothesis":"Another principal may access the seeded order.","next_test":"Compare the owner's and the other principal's access to the same seeded resource."}]}`
const engineAdjudicationResponse = `{"verdict":"real","citations":["evidence_1"]}`

type engineTestBackend struct {
	name     string
	response EngineResponse
	err      error
	seen     *EngineRequest
	calls    int
	wait     bool
}

func (e *engineTestBackend) Info() EngineInfo {
	model := "gpt-6.1-sol"
	if e.name == "jev" {
		model = "jev-latest"
	}
	return EngineInfo{Name: e.name, DefaultModel: model, Available: true, Capabilities: EngineCapabilities{Freeform: e.name == "codex", ConstrainedChoice: true}}
}

func (e *engineTestBackend) Evaluate(ctx context.Context, request EngineRequest) (EngineResponse, error) {
	e.calls++
	e.seen = &request
	if e.wait {
		<-ctx.Done()
		return EngineResponse{}, ctx.Err()
	}
	return e.response, e.err
}

func engineTestRegistry(name, response string) (*EngineRegistry, *engineTestBackend) {
	backend := &engineTestBackend{name: name, response: EngineResponse{Output: []byte(response)}}
	return &EngineRegistry{engines: map[string]Engine{name: backend}}, backend
}

func engineTestCode(err error) string {
	var classified *EngineError
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

func TestEngineSanitizesBeforeAnyModel(t *testing.T) {
	registry, backend := engineTestRegistry("codex", engineTriageResponse)
	raw := `{"headers":{"Authorization":"Bearer TOP_SECRET","Cookie":"SESSION_SECRET","X-Api-Key":"KEY_SECRET"},"body":"BODY_SECRET","families":[{"family_id":"orders-family","method":"GET","url":"https://USER_SECRET:PASS_SECRET@HOST_SECRET.example/api/orders/PATH_SECRET?token=QUERY_SECRET#FRAGMENT_SECRET","content_type":"application/json; secret=CONTENT_SECRET","resource_type":"RESOURCE_SECRET","sample_count":4,"status_codes":[200],"signals":["object_reference"],"principals":["PRINCIPAL_SECRET"],"request_headers":[{"name":"Authorization","value":"HEADER_SECRET"}],"response_body":{"nested":"BODY2_SECRET"},"cookie":"COOKIE_SECRET","api_key":"DIRECT_KEY_SECRET"}]}`
	result, err := registry.Run(context.Background(), EngineOptions{Task: EngineTaskTriage}, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	state, _ := sonic.Marshal(backend.seen.input.state())
	if strings.Contains(string(state), "SECRET") || strings.Contains(string(state), "headers") || strings.Contains(string(state), "body") {
		t.Fatalf("unsanitized facts reached model: %s", state)
	}
	if !strings.Contains(string(state), `/api/orders/[redacted]`) || !strings.Contains(string(state), "principal_1") {
		t.Fatalf("lost route shape or principal identity relation: %s", state)
	}
	if result.Sanitization.HeadersDropped != 2 || result.Sanitization.BodiesDropped != 2 || result.Sanitization.CookiesDropped != 1 || result.Sanitization.CredentialFieldsDropped != 1 || len(result.Sanitization.Warnings) < 3 {
		t.Fatalf("missing explicit redaction report: %+v", result.Sanitization)
	}
	if result.Assessment != "model_assessed" || result.Engine != "codex" || result.Model != "gpt-6.1-sol" || result.Timestamp.IsZero() || len(result.Proposals) != 1 {
		t.Fatalf("missing provenance: %+v", result)
	}
	proposal := result.Proposals[0]
	if proposal.FamilyID != "orders-family" || proposal.EngineAssessment != result.EngineAssessment {
		t.Fatalf("proposal cannot carry its own provenance: %+v", proposal)
	}
}

func TestEngineIndependentAdjudication(t *testing.T) {
	for _, tc := range []struct {
		name, selected, proposer, model string
	}{
		{"same engine different model", "codex", "codex", "gpt-6-luna"},
		{"normalized engine", "codex", " CODEX ", "gpt-6-astra"},
		{"same jev engine", "jev", "jev", "jev-latest"},
		{"default jev engine", "", "jev", ""},
		{"missing proposer", "jev", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry, backend := engineTestRegistry("jev", engineAdjudicationResponse)
			registry.engines["codex"] = backend
			raw := strings.Replace(engineAdjudicationFixture, `"engine":"codex"`, `"engine":"`+tc.proposer+`"`, 1)
			_, err := registry.Run(context.Background(), EngineOptions{Task: EngineTaskAdjudicate, Engine: tc.selected, Model: tc.model}, []byte(raw))
			if engineTestCode(err) != "engine_independence_required" || !strings.Contains(err.Error(), "independence") || backend.calls != 0 {
				t.Fatalf("independence bypass: error=%v calls=%d", err, backend.calls)
			}
		})
	}
}

func TestEngineInvalidAnswersFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		task   EngineTask
		answer string
	}{
		{"prose", EngineTaskTriage, "The endpoint is vulnerable."},
		{"markdown", EngineTaskTriage, "```json\n" + engineTriageResponse + "\n```"},
		{"unknown oracle", EngineTaskTriage, strings.Replace(engineTriageResponse, "object_reference", "remote_code_execution", 1)},
		{"unknown family", EngineTaskTriage, strings.Replace(engineTriageResponse, "family_1", "family_99", 1)},
		{"bad rank", EngineTaskTriage, strings.Replace(engineTriageResponse, `"rank":1`, `"rank":2`, 1)},
		{"observed claim", EngineTaskTriage, strings.Replace(engineTriageResponse, `"rank":1`, `"assessment":"executed_and_observed","rank":1`, 1)},
		{"no proposals", EngineTaskTriage, `{"proposals":[]}`},
		{"null", EngineTaskAdjudicate, `null`},
		{"unknown verdict", EngineTaskAdjudicate, `{"verdict":"probably_real","citations":["evidence_1"]}`},
		{"no citations", EngineTaskAdjudicate, `{"verdict":"real","citations":[]}`},
		{"made up citation", EngineTaskAdjudicate, `{"verdict":"real","citations":["evidence_99"]}`},
		{"missing citations", EngineTaskAdjudicate, `{"verdict":"not_real"}`},
		{"extra field", EngineTaskAdjudicate, `{"verdict":"real","citations":["evidence_1"],"observed":true}`},
		{"trailing object", EngineTaskAdjudicate, engineAdjudicationResponse + " {}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name, raw := "codex", engineTriageFixture
			if tc.task == EngineTaskAdjudicate {
				name, raw = "jev", engineAdjudicationFixture
			}
			registry, backend := engineTestRegistry(name, tc.answer)
			result, err := registry.Run(context.Background(), EngineOptions{Task: tc.task}, []byte(raw))
			if err != nil || result.Verdict != EngineNeedsMoreEvidence || result.Assessment != EngineAssessmentModel || len(result.Proposals) != 0 || len(result.Citations) != 0 || backend.calls != 1 {
				t.Fatalf("not fail closed: result=%+v err=%v calls=%d", result, err, backend.calls)
			}
		})
	}
}

func TestEngineOracleEnum(t *testing.T) {
	for _, oracle := range engineOracles {
		t.Run(string(oracle), func(t *testing.T) {
			registry, _ := engineTestRegistry("codex", strings.Replace(engineTriageResponse, "object_reference", string(oracle), 1))
			result, err := registry.Run(context.Background(), EngineOptions{Task: EngineTaskTriage}, []byte(engineTriageFixture))
			if err != nil || len(result.Proposals) != 1 || result.Proposals[0].Oracle != oracle {
				t.Fatalf("fixed oracle rejected: %+v, %v", result, err)
			}
		})
	}
}

func TestEngineEvidenceSanitizationAndCitations(t *testing.T) {
	registry, backend := engineTestRegistry("jev", engineAdjudicationResponse)
	raw := strings.Replace(engineAdjudicationFixture, `"field":"/owner"`, `"field":"/SECRET_FIELD"`, 1)
	raw = strings.Replace(raw, `"model":"gpt-6.1-sol"`, `"model":"gpt-6.1-sol","hypothesis":"SECRET_PROMPT","next_test":"SECRET_NARRATIVE"`, 1)
	result, err := registry.Run(context.Background(), EngineOptions{Task: EngineTaskAdjudicate}, []byte(raw))
	if err != nil || result.Verdict != EngineReal || len(result.Citations) != 1 || result.Citations[0] != "replay:42" {
		t.Fatalf("lost valid citation: %+v %v", result, err)
	}
	state, _ := sonic.Marshal(backend.seen.input.state())
	for _, secret := range []string{"SECRET", "codex", "gpt-6.1-sol", "replay:42", "seeded-order", "alice", "bob"} {
		if strings.Contains(string(state), secret) {
			t.Fatalf("judge received proposer text or raw reference %q: %s", secret, state)
		}
	}
	evidence := backend.seen.input.adjudicate.Evidence[0]
	if evidence.Subject != evidence.PrincipalComparisons[0].CandidatePrincipal || evidence.Subject == evidence.PrincipalComparisons[0].BaselinePrincipal {
		t.Fatalf("aliasing changed principal relationships: %+v", evidence)
	}
}

func TestEngineUncertainEvidenceStaysUncertain(t *testing.T) {
	for _, tc := range []struct {
		name, field, mask string
	}{
		{"exact volatile field", "/owner", "/owner"},
		{"volatile ancestor", "/owner/name", "/owner"},
		{"volatile descendant", "/owner", "/owner/name"},
		{"volatile root", "/owner", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input EngineAdjudicateInput
			_ = sonic.UnmarshalString(engineAdjudicationFixture, &input)
			input.Evidence[0].PrincipalComparisons = nil
			input.Evidence[0].StableFieldChanges[0].Field = tc.field
			input.Evidence[0].VolatilityMask = []string{tc.mask}
			raw, _ := sonic.Marshal(input)
			registry, _ := engineTestRegistry("jev", engineAdjudicationResponse)
			result, err := registry.Run(context.Background(), EngineOptions{Task: EngineTaskAdjudicate}, raw)
			if err != nil || result.Verdict != EngineNeedsMoreEvidence || result.Sanitization.VolatileChangesDropped != 1 || result.Reason != "status_deltas_alone_are_insufficient" {
				t.Fatalf("volatile field upgraded to proof: %+v %v", result, err)
			}
		})
	}
	registry, backend := engineTestRegistry("jev", engineAdjudicationResponse)
	confidence := 0.79
	backend.response.Confidence = &confidence
	result, err := registry.Run(context.Background(), EngineOptions{Task: EngineTaskAdjudicate}, []byte(engineAdjudicationFixture))
	if err != nil || result.Verdict != EngineNeedsMoreEvidence || result.Reason != "engine_low_confidence" || *result.Confidence != confidence {
		t.Fatalf("low confidence upgraded: %+v %v", result, err)
	}
}

func TestEngineRejectsInvalidInputsBeforeModel(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		task      EngineTask
	}{
		{"free prompt", `{"prompt":"hello"}`, EngineTaskTriage},
		{"untyped signal", strings.Replace(engineTriageFixture, `"object_reference"`, `"run arbitrary code"`, 1), EngineTaskTriage},
		{"unknown input", strings.Replace(engineTriageFixture, `"method":"GET"`, `"method":"GET","notes":"RAW_DATA"`, 1), EngineTaskTriage},
		{"no families", `{"families":[]}`, EngineTaskTriage},
		{"body values in evidence", strings.Replace(engineAdjudicationFixture, `"change":"different"`, `"change":"different","before":"RAW_DATA"`, 1), EngineTaskAdjudicate},
		{"inferred evidence", strings.Replace(engineAdjudicationFixture, "executed_and_observed", "model_assessed", 1), EngineTaskAdjudicate},
		{"unnamed subject", strings.Replace(engineAdjudicationFixture, `"subject":"bob"`, `"subject":""`, 1), EngineTaskAdjudicate},
		{"missing access prerequisite", strings.Replace(engineAdjudicationFixture, `"same_resource":true,`, ``, 1), EngineTaskAdjudicate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "codex"
			if tc.task == EngineTaskAdjudicate {
				name = "jev"
			}
			registry, backend := engineTestRegistry(name, "{}")
			_, err := registry.Run(context.Background(), EngineOptions{Task: tc.task}, []byte(tc.raw))
			if engineTestCode(err) != "engine_invalid_input" || backend.calls != 0 {
				t.Fatalf("invalid facts reached model: %v calls=%d", err, backend.calls)
			}
		})
	}
}

func TestEngineBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits EngineLimits
		code   string
	}{
		{"input", EngineLimits{Timeout: time.Second, MaxInputBytes: 10, MaxOutputBytes: 1000}, "engine_input_limit"},
		{"timeout invalid", EngineLimits{Timeout: 3 * time.Minute, MaxInputBytes: 1000, MaxOutputBytes: 1000}, "engine_invalid_limits"},
		{"input invalid", EngineLimits{Timeout: time.Second, MaxInputBytes: -1, MaxOutputBytes: 1000}, "engine_invalid_limits"},
		{"output invalid", EngineLimits{Timeout: time.Second, MaxInputBytes: 1000, MaxOutputBytes: EngineMaxOutputBytes + 1}, "engine_invalid_limits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry, backend := engineTestRegistry("codex", engineTriageResponse)
			_, err := registry.Run(context.Background(), EngineOptions{Task: EngineTaskTriage, Limits: tc.limits}, []byte(engineTriageFixture))
			if engineTestCode(err) != tc.code || backend.calls != 0 {
				t.Fatalf("bad preflight bound: %v calls=%d", err, backend.calls)
			}
		})
	}
	registry, backend := engineTestRegistry("codex", engineTriageResponse)
	limits := EngineDefaultLimits()
	limits.MaxOutputBytes = 10
	result, err := registry.Run(context.Background(), EngineOptions{Task: EngineTaskTriage, Limits: limits}, []byte(engineTriageFixture))
	if err != nil || result.Verdict != EngineNeedsMoreEvidence || result.Reason != "engine_output_limit" {
		t.Fatalf("unbounded answer: %+v %v", result, err)
	}
	backend.wait = true
	limits.Timeout = 10 * time.Millisecond
	start := time.Now()
	result, err = registry.Run(context.Background(), EngineOptions{Task: EngineTaskTriage, Limits: limits}, []byte(engineTriageFixture))
	if err != nil || result.Verdict != EngineNeedsMoreEvidence || time.Since(start) > time.Second || result.Reason != "engine_timeout_or_canceled" {
		t.Fatalf("unbounded call: %+v %v", result, err)
	}
}
