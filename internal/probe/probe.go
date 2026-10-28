package probe

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/bytedance/sonic"
)

type ProbeOracleKind string
type ProbeState string
type ProbeVerdict string

const (
	ProbeAuthzCrossPrincipal   ProbeOracleKind = "authz_cross_principal"
	ProbeAuthzUnauthenticated  ProbeOracleKind = "authz_unauthenticated"
	ProbeObjectReference       ProbeOracleKind = "object_reference"
	ProbeInputReflection       ProbeOracleKind = "input_reflection"
	ProbeMutationSideEffect    ProbeOracleKind = "mutation_side_effect"
	ProbeRateOrLockout         ProbeOracleKind = "rate_or_lockout"
	ProbeInformationDisclosure ProbeOracleKind = "information_disclosure"
	ProbeUntested              ProbeState      = "untested"
	ProbeAttempted             ProbeState      = "attempted"
	ProbeInconclusive          ProbeState      = "inconclusive"
	ProbeVerified              ProbeState      = "verified"
	ProbeRefuted               ProbeState      = "refuted"
	ProbeBlocked               ProbeState      = "blocked"
	ProbePass                  ProbeVerdict    = "pass"
	ProbeFail                  ProbeVerdict    = "fail"
	ProbeVerdictInconclusive   ProbeVerdict    = "inconclusive"
	ProbeDefaultLimit                          = 5
	ProbeMaxLimit                              = 100
	ProbeModelAssessed                         = "model_assessed"
	ProbeLedgerName                            = "probes.jsonl"
	probeMaxTextBytes                          = 2048
	probeMaxRecordBytes                        = 64 << 10
)

var (
	ProbeErrInvalid  = errors.New("invalid probe argument")
	ProbeErrNotFound = errors.New("probe not found")
	ProbeErrCorrupt  = errors.New("invalid probe ledger")
	probeUUID        = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	probeEvidenceID  = regexp.MustCompile(`^(run|op|operation)[_:-][A-Za-z0-9][A-Za-z0-9_.:-]{0,127}(/(op|operation)[_:-][A-Za-z0-9][A-Za-z0-9_.:-]{0,127})?$`)
	probeRequestID   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$`)
)

type Probe struct {
	ID             string          `json:"id"`
	EndpointFamily string          `json:"endpoint_family"`
	MutationShape  string          `json:"mutation_shape"`
	IdentityShape  []string        `json:"identity_shape"`
	Hypothesis     string          `json:"hypothesis"`
	OracleKind     ProbeOracleKind `json:"oracle_kind"`
	Precondition   string          `json:"precondition"`
	RequestRef     string          `json:"request_ref"`
	State          ProbeState      `json:"state"`
	Evidence       []string        `json:"evidence"`
	Priority       int             `json:"priority"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	Verdict        ProbeVerdict    `json:"verdict,omitempty"`
	Assessment     string          `json:"assessment,omitempty"`
	Note           string          `json:"note,omitempty"`
}

type ProbeSpec struct {
	Method        string
	URL           string
	RequestRef    string
	Hypothesis    string
	OracleKind    ProbeOracleKind
	Precondition  string
	MutationShape string
	IdentityShape []string
}

type ProbeRecordSpec struct {
	Verdict  ProbeVerdict
	State    ProbeState
	Evidence []string
	Note     string
}

// The journal preserves caller assessments separately from executed evidence.
// A reported pass never manufactures an executed_and_observed finding.
type ProbeEvent struct {
	Version    int             `json:"version"`
	Sequence   int             `json:"sequence"`
	Kind       string          `json:"kind"`
	ProbeID    string          `json:"probe_id"`
	OracleKind ProbeOracleKind `json:"oracle_kind"`
	At         time.Time       `json:"at"`
	Proposal   *Probe          `json:"proposal,omitempty"`
	State      ProbeState      `json:"state"`
	Verdict    ProbeVerdict    `json:"verdict,omitempty"`
	Evidence   []string        `json:"evidence,omitempty"`
	Note       string          `json:"note,omitempty"`
	Assessment string          `json:"assessment,omitempty"`
}

type ProbeBatch struct {
	Probes           []Probe `json:"probes"`
	MatchingUntested int     `json:"matching_untested"`
	TotalUntested    int     `json:"total_untested"`
	Omitted          int     `json:"omitted"`
	FrontierEmpty    bool    `json:"frontier_empty"`
	Interpretation   string  `json:"interpretation"`
}

type ProbeOracleCoverage struct {
	Total   int                `json:"total"`
	ByState map[ProbeState]int `json:"by_state"`
}

type ProbeCoverage struct {
	Total          int                                     `json:"total"`
	ByState        map[ProbeState]int                      `json:"by_state"`
	ByOracle       map[ProbeOracleKind]ProbeOracleCoverage `json:"by_oracle"`
	Untested       []Probe                                 `json:"untested"`
	Omitted        int                                     `json:"untested_omitted"`
	FrontierEmpty  bool                                    `json:"frontier_empty"`
	Interpretation string                                  `json:"interpretation"`
}

type ProbeDetail struct {
	Probe         Probe        `json:"probe"`
	History       []ProbeEvent `json:"history"`
	TotalEvents   int          `json:"total_events"`
	OmittedEvents int          `json:"omitted_events"`
}

func ProbeOracleKinds() []ProbeOracleKind {
	return []ProbeOracleKind{ProbeAuthzCrossPrincipal, ProbeAuthzUnauthenticated, ProbeObjectReference,
		ProbeInputReflection, ProbeMutationSideEffect, ProbeRateOrLockout, ProbeInformationDisclosure}
}

func ProbeStates() []ProbeState {
	return []ProbeState{ProbeUntested, ProbeAttempted, ProbeInconclusive, ProbeVerified, ProbeRefuted, ProbeBlocked}
}

func ProbeParseOracle(value string) (ProbeOracleKind, error) {
	for _, kind := range ProbeOracleKinds() {
		if value == string(kind) {
			return kind, nil
		}
	}
	return "", fmt.Errorf("%w: unknown oracle %q", ProbeErrInvalid, value)
}

// Capture IDs, prose, baseline responses, and timestamps cannot change the
// identity of an experiment. Only the canonical action tuple enters this hash.
func probeContentID(family, mutation string, principals []string, oracle ProbeOracleKind) string {
	key := struct {
		EndpointFamily string          `json:"endpoint_family"`
		MutationShape  string          `json:"mutation_shape"`
		IdentityShape  []string        `json:"identity_shape"`
		OracleKind     ProbeOracleKind `json:"oracle_kind"`
	}{family, mutation, principals, oracle}
	data, _ := sonic.Marshal(key)
	return fmt.Sprintf("probe_%x", sha256.Sum256(data))
}

func ProbeNew(spec ProbeSpec) (Probe, error) {
	if _, err := ProbeParseOracle(string(spec.OracleKind)); err != nil {
		return Probe{}, err
	}
	hypothesis := strings.TrimSpace(spec.Hypothesis)
	if hypothesis == "" || len(hypothesis) > probeMaxTextBytes || strings.ContainsAny(hypothesis, "\r\n") {
		return Probe{}, fmt.Errorf("%w: hypothesis must be one nonempty line of at most %d bytes", ProbeErrInvalid, probeMaxTextBytes)
	}
	if !probeRequestID.MatchString(spec.RequestRef) {
		return Probe{}, fmt.Errorf("%w: ref must be a captured request ID", ProbeErrInvalid)
	}
	family, err := ProbeEndpointFamily(spec.Method, spec.URL)
	if err != nil {
		return Probe{}, err
	}
	mutation, precondition := probeDefaults(spec.OracleKind)
	if strings.TrimSpace(spec.MutationShape) != "" {
		mutation = strings.Join(strings.Fields(spec.MutationShape), " ")
	}
	if strings.TrimSpace(spec.Precondition) != "" {
		precondition = strings.TrimSpace(spec.Precondition)
	}
	if len(mutation) > probeMaxTextBytes || len(precondition) > probeMaxTextBytes {
		return Probe{}, fmt.Errorf("%w: mutation and precondition must be at most %d bytes", ProbeErrInvalid, probeMaxTextBytes)
	}
	principals := []string{}
	for _, name := range spec.IdentityShape {
		name = strings.TrimSpace(name)
		if name == "" || len(name) > 128 || strings.ContainsAny(name, ",\r\n") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return Probe{}, fmt.Errorf("%w: invalid principal name", ProbeErrInvalid)
		}
		if strings.EqualFold(name, "anon") {
			name = "anon"
		}
		principals = append(principals, name)
	}
	principals = probeUnique(principals)
	if len(principals) > 16 {
		return Probe{}, fmt.Errorf("%w: at most 16 principals are supported", ProbeErrInvalid)
	}
	p := Probe{
		EndpointFamily: family, MutationShape: mutation, IdentityShape: principals,
		Hypothesis: hypothesis, OracleKind: spec.OracleKind, Precondition: precondition,
		RequestRef: spec.RequestRef, State: ProbeUntested, Evidence: []string{}, Priority: probePriority(spec.OracleKind),
	}
	p.ID = probeContentID(family, mutation, principals, spec.OracleKind)
	return p, nil
}

func ProbeEndpointFamily(method, rawURL string) (string, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" || strings.IndexFunc(method, func(r rune) bool { return r < 'A' || r > 'Z' }) >= 0 {
		return "", fmt.Errorf("%w: a captured HTTP method is required", ProbeErrInvalid)
	}
	u, err := url.Parse(rawURL)
	if err != nil || u == nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || len(rawURL) > 16<<10 {
		return "", fmt.Errorf("%w: ref must name an absolute HTTP(S) request", ProbeErrInvalid)
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" && !(u.Scheme == "https" && port == "443") && !(u.Scheme == "http" && port == "80") {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			return "", fmt.Errorf("%w: invalid request path escape", ProbeErrInvalid)
		}
		if probeObjectSegment(decoded) {
			segments[i] = "{id}"
		} else {
			segments[i] = url.PathEscape(decoded)
		}
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", fmt.Errorf("%w: invalid request query", ProbeErrInvalid)
	}
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, url.QueryEscape(key))
	}
	sort.Strings(keys)
	family := method + " " + u.Scheme + "://" + host + strings.Join(segments, "/")
	if len(keys) > 0 {
		family += "?" + strings.Join(keys, "&")
	}
	if len(family) > 8192 {
		return "", fmt.Errorf("%w: endpoint family exceeds 8192 bytes", ProbeErrInvalid)
	}
	return family, nil
}

func probeObjectSegment(value string) bool {
	if value == "" {
		return false
	}
	digits, hex, upper, lower, number, opaque := true, true, false, false, false, true
	for _, r := range value {
		digits = digits && r >= '0' && r <= '9'
		hex = hex && (r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F')
		upper = upper || r >= 'A' && r <= 'Z'
		lower = lower || r >= 'a' && r <= 'z'
		number = number || r >= '0' && r <= '9'
		opaque = opaque && (r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || r == '-')
	}
	return digits || probeUUID.MatchString(value) || len(value) >= 12 && hex ||
		len(value) >= 16 && opaque && (number && (upper || lower) || len(value) >= 24 && upper && lower)
}

func probeDefaults(kind ProbeOracleKind) (string, string) {
	switch kind {
	case ProbeAuthzCrossPrincipal:
		return "cross_principal", "Two distinct bound principals and a named protected resource exist; the owner's baseline is stable."
	case ProbeAuthzUnauthenticated:
		return "strip_credentials", "An authenticated owner can access the named protected resource; its baseline is stable."
	case ProbeObjectReference:
		return "substitute_object_reference", "Object ownership and an unauthorized principal are known; the owner's resource is distinguishable."
	case ProbeInputReflection:
		return "reflect_input", "A controlled input marker and its response context can be distinguished from baseline variation."
	case ProbeMutationSideEffect:
		return "mutate_input", "The initial resource state and the predicted persistent side effect can be independently observed."
	case ProbeRateOrLockout:
		return "repeat_request", "The principal, initial lockout state, rate window, and expected policy are known."
	default:
		return "inspect_response", "The named sensitive resource and the subject's expected access policy are known."
	}
}

func probePriority(kind ProbeOracleKind) int {
	switch kind {
	case ProbeAuthzCrossPrincipal:
		return 100
	case ProbeObjectReference:
		return 95
	case ProbeAuthzUnauthenticated:
		return 90
	case ProbeMutationSideEffect:
		return 80
	case ProbeInformationDisclosure:
		return 70
	case ProbeInputReflection:
		return 60
	case ProbeRateOrLockout:
		return 50
	}
	return 0
}

func probeUnique(values []string) []string {
	sort.Strings(values)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

func probeValidateRecord(spec ProbeRecordSpec) (ProbeRecordSpec, error) {
	if spec.Verdict != "" && spec.State != "" {
		return spec, fmt.Errorf("%w: choose a verdict or an attempted/blocked state, not both", ProbeErrInvalid)
	}
	switch spec.Verdict {
	case ProbePass:
		spec.State = ProbeVerified
	case ProbeFail:
		spec.State = ProbeRefuted
	case ProbeVerdictInconclusive:
		spec.State = ProbeInconclusive
	case "":
		if spec.State != ProbeAttempted && spec.State != ProbeBlocked {
			return spec, fmt.Errorf("%w: verdict must be pass, fail, or inconclusive; state must be attempted or blocked", ProbeErrInvalid)
		}
	default:
		return spec, fmt.Errorf("%w: verdict must be pass, fail, or inconclusive", ProbeErrInvalid)
	}
	spec.Note = strings.TrimSpace(spec.Note)
	if len(spec.Note) > probeMaxTextBytes || spec.State == ProbeBlocked && spec.Note == "" {
		return spec, fmt.Errorf("%w: note must be at most %d bytes and is required for blocked prerequisites", ProbeErrInvalid, probeMaxTextBytes)
	}
	if len(spec.Evidence) == 0 || len(spec.Evidence) > 16 {
		return spec, fmt.Errorf("%w: supply 1 to 16 run/operation evidence references", ProbeErrInvalid)
	}
	refs := make([]string, 0, len(spec.Evidence))
	for _, ref := range spec.Evidence {
		if !probeEvidenceID.MatchString(ref) {
			return spec, fmt.Errorf("%w: evidence must be a run/operation ID or run_ID/op_ID pointer, never an inline body", ProbeErrInvalid)
		}
		refs = append(refs, ref)
	}
	spec.Evidence = probeUnique(refs)
	return spec, nil
}

func probeValidateLimit(limit int) error {
	if limit < 1 || limit > ProbeMaxLimit {
		return fmt.Errorf("%w: limit must be between 1 and %d", ProbeErrInvalid, ProbeMaxLimit)
	}
	return nil
}

func probeStateCounts() map[ProbeState]int {
	counts := make(map[ProbeState]int)
	for _, state := range ProbeStates() {
		counts[state] = 0
	}
	return counts
}

func probeInterpretation(total, untested int) string {
	if total == 0 {
		return "No probes have been planned. An empty frontier is not proof that the target is secure."
	}
	if untested == 0 {
		return "The planned untested frontier is complete. Blocked, attempted, and inconclusive work remains separately visible. This is not proof that the target is secure."
	}
	return "Untested planned experiments remain. Counts describe this task's recorded plan, not target security."
}
