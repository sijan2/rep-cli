// Package surface turns captured traffic into an attack-surface model.
//
// The previous audit scored every request independently with a sum of five
// booleans. Because a browser attaches cookies to every same-origin request,
// its auth signal fired on fonts and stylesheets, so a real capture produced
// hundreds of rows tied at the same score and the ranking degenerated into
// sort-by-id. Scoring here is per endpoint FAMILY rather than per request, and
// it starts by discarding the traffic that cannot hold a vulnerability.
package surface

import (
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Kind separates traffic that can carry a vulnerability from traffic that
// cannot. Static assets are excluded from scoring entirely.
type Kind string

const (
	KindStatic   Kind = "static"
	KindAPI      Kind = "api"
	KindDocument Kind = "document"
	KindOther    Kind = "other"
)

// OracleKind names the experiment a candidate is worth. A candidate without an
// oracle is a URL, not a hypothesis, so every candidate carries one.
type OracleKind string

const (
	OracleAuthzCrossPrincipal  OracleKind = "authz_cross_principal"
	OracleAuthzUnauthenticated OracleKind = "authz_unauthenticated"
	OracleObjectReference      OracleKind = "object_reference"
	OracleInputReflection      OracleKind = "input_reflection"
	OracleMutationSideEffect   OracleKind = "mutation_side_effect"
	OracleInfoDisclosure       OracleKind = "information_disclosure"
)

var staticExtensions = map[string]bool{
	".js": true, ".mjs": true, ".cjs": true, ".css": true, ".map": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".svg": true, ".ico": true, ".avif": true, ".bmp": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".mp4": true, ".webm": true, ".mp3": true, ".wav": true, ".ogg": true,
}

var staticContentTypes = []string{
	"text/css", "image/", "font/", "video/", "audio/",
	"application/javascript", "text/javascript", "application/font",
}

var staticResourceTypes = map[string]bool{
	"stylesheet": true, "image": true, "font": true, "media": true,
	"script": true, "texttrack": true, "manifest": true,
}

// Numeric, UUID and long-hex path segments are object identifiers, not route
// structure. Collapsing them is what turns 100 near-identical requests into one
// family with 100 observed object values.
var (
	reNumeric = regexp.MustCompile(`^\d+$`)
	reUUID    = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	reHex     = regexp.MustCompile(`(?i)^[0-9a-f]{16,}$`)
	reOpaque  = regexp.MustCompile(`^[A-Za-z0-9_-]{20,}$`)
	reVersion = regexp.MustCompile(`^v\d{1,2}$`)
	// Content-hashed build artifacts: main-2d8e3876ec53ebc0.js, 3b0f561a.css
	reHashed = regexp.MustCompile(`(?i)[.\-_][0-9a-f]{8,}\.[a-z0-9]{2,5}$`)
)

// csrfTokenNames are header or parameter names that carry a CSRF defence.
var csrfTokenNames = []string{"csrf", "xsrf", "_token", "authenticity_token", "requestverificationtoken"}

// clientAssertedNames are values a client should never be trusted to set.
var clientAssertedNames = []string{
	"price", "amount", "total", "cost", "quantity", "qty", "discount",
	"role", "is_admin", "isadmin", "admin", "is_staff", "permission", "scope",
	"user_id", "userid", "account_id", "accountid", "tenant", "org_id", "orgid",
	"status", "state", "verified", "approved", "balance", "credit",
}

// Observation is the minimal projection of a captured request that scoring
// needs. Keeping it narrow means scoring never depends on response bytes.
type Observation struct {
	ID           string
	Method       string
	URL          string
	Domain       string
	ResourceType string
	ContentType  string
	Status       int
	HasCookie    bool
	HasAuthzHdr  bool
	HeaderNames  []string
	BodyKeys     []string
	QueryKeys    []string
	RespBytes    int
	IsPrimary    bool
}

// Variable is an identifier lifted out of a route during normalization.
type Variable struct {
	// Position is "path[2]" or "query:id" so two observations of the same slot
	// can be compared.
	Position string `json:"position"`
	Shape    string `json:"shape"` // numeric, uuid, hex, opaque
	Value    string `json:"value"`
}

// Family is one normalized endpoint plus everything observed about it.
type Family struct {
	Key         string   `json:"key"`
	Method      string   `json:"method"`
	Host        string   `json:"host"`
	Route       string   `json:"route"`
	Kind        Kind     `json:"kind"`
	Count       int      `json:"count"`
	RequestIDs  []string `json:"request_ids"`
	Statuses    []int    `json:"statuses"`
	IsPrimary   bool     `json:"is_primary"`
	Authed      bool     `json:"authenticated"`
	StateChange bool     `json:"state_changing"`
	HasCSRF     bool     `json:"has_csrf_token"`

	// DistinctObjectValues counts how many different identifiers were seen in
	// each variable slot. More than one proves the slot is an object reference
	// the client chooses, which is the precondition for an IDOR test.
	DistinctObjectValues map[string]int `json:"distinct_object_values,omitempty"`
	ClientAsserted       []string       `json:"client_asserted,omitempty"`
	MaxRespBytes         int            `json:"max_response_bytes"`

	Score   float64  `json:"score"`
	Signals []Signal `json:"signals"`
}

// Signal is a scored, named reason a family is interesting. Weights are
// continuous so families do not tie the way the old boolean sum did.
type Signal struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight"`
	Detail string  `json:"detail,omitempty"`
}

// Candidate is a family paired with the experiment worth running against it.
type Candidate struct {
	Family     string     `json:"family"`
	Method     string     `json:"method"`
	Host       string     `json:"host"`
	Route      string     `json:"route"`
	Score      float64    `json:"score"`
	Oracle     OracleKind `json:"oracle"`
	Hypothesis string     `json:"hypothesis"`
	RefID      string     `json:"ref_request_id"`
	Next       string     `json:"next"`
	Signals    []Signal   `json:"signals"`
}

// ClassifyKind decides whether a request can hold a vulnerability at all.
func ClassifyKind(o Observation) Kind {
	if staticResourceTypes[strings.ToLower(o.ResourceType)] {
		return KindStatic
	}
	ct := strings.ToLower(o.ContentType)
	for _, prefix := range staticContentTypes {
		if strings.HasPrefix(ct, prefix) {
			return KindStatic
		}
	}
	if u, err := url.Parse(o.URL); err == nil {
		ext := strings.ToLower(path.Ext(u.Path))
		if staticExtensions[ext] {
			return KindStatic
		}
		if reHashed.MatchString(u.Path) {
			return KindStatic
		}
	}
	if strings.Contains(ct, "json") || strings.Contains(ct, "xml") ||
		strings.Contains(o.URL, "/api/") || strings.Contains(o.URL, "/graphql") {
		return KindAPI
	}
	if strings.HasPrefix(ct, "text/html") || strings.ToLower(o.ResourceType) == "document" {
		return KindDocument
	}
	return KindOther
}

// NormalizeRoute collapses identifier segments so that /doc/1 and /doc/2 share
// a family, and reports which slots held identifiers.
func NormalizeRoute(rawURL string) (route string, vars []Variable) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, nil
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	out := make([]string, 0, len(segments))
	for i, seg := range segments {
		if seg == "" {
			continue
		}
		shape := identifierShape(seg)
		if shape == "" || reVersion.MatchString(strings.ToLower(seg)) {
			out = append(out, seg)
			continue
		}
		vars = append(vars, Variable{Position: "path[" + strconv.Itoa(i) + "]", Shape: shape, Value: seg})
		out = append(out, "{"+shape+"}")
	}
	route = "/" + strings.Join(out, "/")

	keys := make([]string, 0, len(u.Query()))
	for k, values := range u.Query() {
		keys = append(keys, k)
		for _, v := range values {
			if shape := identifierShape(v); shape != "" {
				vars = append(vars, Variable{Position: "query:" + k, Shape: shape, Value: v})
			}
		}
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		route += "?" + strings.Join(keys, "&")
	}
	return route, vars
}

func identifierShape(s string) string {
	switch {
	case reNumeric.MatchString(s):
		return "num"
	case reUUID.MatchString(s):
		return "uuid"
	case reHex.MatchString(s):
		return "hex"
	case reOpaque.MatchString(s) && !isWordy(s):
		return "opaque"
	}
	return ""
}

// isWordy keeps readable slugs like "introducing-autonomous-exposure" out of
// the identifier buckets; they are route structure, not object references.
func isWordy(s string) bool {
	vowels := 0
	for _, r := range strings.ToLower(s) {
		if strings.ContainsRune("aeiou", r) {
			vowels++
		}
	}
	return float64(vowels) > float64(len(s))*0.2
}

// Build groups observations into families and scores them.
func Build(observations []Observation) []Family {
	byKey := map[string]*Family{}
	valueSets := map[string]map[string]map[string]bool{} // familyKey -> position -> values

	for _, o := range observations {
		kind := ClassifyKind(o)
		route, vars := NormalizeRoute(o.URL)
		key := strings.ToUpper(o.Method) + " " + o.Domain + " " + route

		fam, ok := byKey[key]
		if !ok {
			fam = &Family{
				Key: key, Method: strings.ToUpper(o.Method), Host: o.Domain,
				Route: route, Kind: kind, IsPrimary: o.IsPrimary,
				DistinctObjectValues: map[string]int{},
			}
			byKey[key] = fam
			valueSets[key] = map[string]map[string]bool{}
		}
		fam.Count++
		if len(fam.RequestIDs) < 5 {
			fam.RequestIDs = append(fam.RequestIDs, o.ID)
		}
		if !containsInt(fam.Statuses, o.Status) {
			fam.Statuses = append(fam.Statuses, o.Status)
		}
		if o.HasCookie || o.HasAuthzHdr {
			fam.Authed = true
		}
		if isStateChanging(o.Method) {
			fam.StateChange = true
		}
		if hasCSRF(o) {
			fam.HasCSRF = true
		}
		if o.RespBytes > fam.MaxRespBytes {
			fam.MaxRespBytes = o.RespBytes
		}
		for _, name := range append(append([]string{}, o.BodyKeys...), o.QueryKeys...) {
			if isClientAsserted(name) && !containsString(fam.ClientAsserted, name) {
				fam.ClientAsserted = append(fam.ClientAsserted, name)
			}
		}
		for _, v := range vars {
			if valueSets[key][v.Position] == nil {
				valueSets[key][v.Position] = map[string]bool{}
			}
			valueSets[key][v.Position][v.Value] = true
		}
	}

	families := make([]Family, 0, len(byKey))
	for key, fam := range byKey {
		for pos, values := range valueSets[key] {
			fam.DistinctObjectValues[pos] = len(values)
		}
		sort.Ints(fam.Statuses)
		sort.Strings(fam.ClientAsserted)
		score(fam)
		families = append(families, *fam)
	}
	sort.Slice(families, func(i, j int) bool {
		if families[i].Score != families[j].Score {
			return families[i].Score > families[j].Score
		}
		return families[i].Key < families[j].Key
	})
	return families
}

// score assigns continuous weights. Static traffic scores zero regardless of
// what else it carries: a cookie on a font proves nothing.
func score(f *Family) {
	if f.Kind == KindStatic {
		f.Score = 0
		f.Signals = nil
		return
	}
	add := func(name string, weight float64, detail string) {
		f.Signals = append(f.Signals, Signal{Name: name, Weight: weight, Detail: detail})
		f.Score += weight
	}

	// An object reference is only interesting once the client has been observed
	// choosing more than one value for the slot.
	for pos, n := range f.DistinctObjectValues {
		switch {
		case n > 1 && strings.HasPrefix(pos, "path["):
			add("object_reference", 4.0+minFloat(float64(n)/10, 2.0), pos+": "+strconv.Itoa(n)+" distinct values")
		case n > 1:
			add("object_reference_query", 2.5, pos+": "+strconv.Itoa(n)+" distinct values")
		case strings.HasPrefix(pos, "path["):
			add("object_slot", 1.0, pos+": single observed value")
		}
	}
	if f.Authed && f.Kind == KindAPI {
		add("authenticated_api", 2.5, "")
	} else if f.Authed {
		add("authenticated", 1.0, "")
	}
	if f.StateChange {
		add("state_changing", 2.0, f.Method)
		if f.Authed && !f.HasCSRF {
			add("no_csrf_token", 2.5, "cookie-authenticated mutation without a CSRF token")
		}
	}
	if len(f.ClientAsserted) > 0 {
		add("client_asserted", 3.0, strings.Join(f.ClientAsserted, ","))
	}
	if f.MaxRespBytes > 20000 && f.Kind == KindAPI {
		add("large_api_response", 1.0, strconv.Itoa(f.MaxRespBytes)+" bytes")
	}
	if len(f.Statuses) > 1 {
		add("status_variance", 0.5, "")
	}
	if !f.IsPrimary {
		f.Score *= 0.4 // third-party traffic is rarely the engagement's target
		f.Signals = append(f.Signals, Signal{Name: "third_party", Weight: 0, Detail: "score damped"})
	}
}

// Candidates pairs each scored family with the single most informative
// experiment, so the agent receives hypotheses rather than URLs.
func Candidates(families []Family, limit int) []Candidate {
	out := make([]Candidate, 0, limit)
	for _, f := range families {
		if f.Score <= 0 || len(f.RequestIDs) == 0 {
			continue
		}
		oracle, hypothesis := chooseOracle(f)
		ref := f.RequestIDs[0]
		out = append(out, Candidate{
			Family: f.Key, Method: f.Method, Host: f.Host, Route: f.Route,
			Score: f.Score, Oracle: oracle, Hypothesis: hypothesis,
			RefID: ref, Next: nextCommand(oracle, ref), Signals: f.Signals,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func chooseOracle(f Family) (OracleKind, string) {
	enumerable := ""
	for pos, n := range f.DistinctObjectValues {
		if n > 1 && strings.HasPrefix(pos, "path[") {
			enumerable = pos
			break
		}
	}
	switch {
	case enumerable != "" && f.Authed:
		return OracleAuthzCrossPrincipal,
			"the object identifier at " + enumerable + " is client-chosen and the endpoint is authenticated, so another principal may reach this object"
	case enumerable != "":
		return OracleObjectReference,
			"the object identifier at " + enumerable + " is client-chosen on an unauthenticated endpoint"
	case len(f.ClientAsserted) > 0:
		return OracleMutationSideEffect,
			"the client asserts " + strings.Join(f.ClientAsserted, ",") + "; the server may trust it"
	case f.StateChange && f.Authed && !f.HasCSRF:
		return OracleMutationSideEffect,
			"cookie-authenticated mutation with no CSRF token observed"
	case f.Authed:
		return OracleAuthzUnauthenticated,
			"authenticated endpoint; confirm it actually requires the credential"
	case f.MaxRespBytes > 20000:
		return OracleInfoDisclosure,
			"large response on an unauthenticated endpoint"
	default:
		return OracleInputReflection,
			"client-supplied input may be reflected"
	}
}

func nextCommand(oracle OracleKind, ref string) string {
	switch oracle {
	case OracleAuthzCrossPrincipal:
		return "rep replay " + ref + " --baseline 5 --as <other-principal>,anon"
	case OracleAuthzUnauthenticated:
		return "rep replay " + ref + " --baseline 5 --as anon"
	case OracleObjectReference:
		return "rep replay " + ref + " --baseline 5 --set 'path-object=<other-id>'"
	case OracleMutationSideEffect:
		return "rep replay " + ref + " --baseline 5  # verify the side effect, not the status"
	case OracleInfoDisclosure:
		return "rep body " + ref + " --info"
	default:
		return "rep detail " + ref
	}
}

func isStateChanging(method string) bool {
	switch strings.ToUpper(method) {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
}

func hasCSRF(o Observation) bool {
	for _, group := range [][]string{o.HeaderNames, o.BodyKeys, o.QueryKeys} {
		for _, name := range group {
			lower := strings.ToLower(name)
			for _, token := range csrfTokenNames {
				if strings.Contains(lower, token) {
					return true
				}
			}
		}
	}
	return false
}

func isClientAsserted(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, candidate := range clientAssertedNames {
		if lower == candidate || strings.HasSuffix(lower, "_"+candidate) {
			return true
		}
	}
	return false
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func containsString(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
