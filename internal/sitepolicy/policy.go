// Package sitepolicy ships the versioned per-site data files that decide which
// response bodies the network ledger may keep, how a run is paced, and when it
// must stop. Policies are matched by URL shape only, never by query ids,
// bearer tokens or feature flags, and the loader fails closed: one malformed or
// unknown field disables every shipped policy rather than guessing.
package sitepolicy

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

//go:embed policies/*.json
var embedded embed.FS

// Ceilings the extension enforces on browser.ledger.open; LedgerParams clamps
// to them so a caller can never ask for more than the ledger will grant.
const (
	MaxBodyBytes  = 8 << 20
	MaxRingBytes  = 64 << 20
	MaxIdleMS     = 600000
	DefaultIdleMS = 120000
)

// Decoder names a records decoder: "reddit_html" | "x_tweetdetail" | "hn_html" | "json".
// Empty means the body is content-sniffed (records.Detect), which only the
// generic policy uses.
type BodyOp struct {
	Op, Method, Host, PathRe string
	Types                    []string
	Decoder                  string
}

// Continuation documents the page's own controls for a continuation kind. It
// is descriptive: nothing in core clicks these selectors.
type Continuation struct{ Kind, Selector, Trigger string }

type Pacing struct {
	MinIntervalMS, MaxInFlight, AdvancesPerRead, ReserveMin int
	ReserveFraction                                         float64
	DailyRecords                                            int
}

type Stop struct {
	Statuses             []int
	Headers              []string
	TitleContains        []string
	RedirectPathPrefixes []string
	RedirectQueryKeys    []string
}

type Policy struct {
	ID, Version      string
	Hosts            []string
	TOSRisk          string // low|medium|high
	Replay           string // never|read_only|allowed
	SignatureHeaders []string
	BodyOps          []BodyOp
	PrivatePaths     []string
	Primary          struct{ Op, PathRe string }
	Inputs           []string
	Continuations    []Continuation
	DOMKeys          map[string]string
	Contracts        map[string]float64
	Expected         string
	Pacing           Pacing
	Stop             Stop
}

// Inputs are the canonical takeover keys extracted from a URL, e.g. reddit
// {"sub","t3"}, x {"status"}, hn {"item"}. Takeover and drift are judged on
// these, so the same thread reached through a different slug or letter case
// yields identical Inputs.
type Inputs map[string]string

// Static resource types are metadata-only in the ledger regardless of policy,
// so admitting one would be a policy mistake rather than a choice.
var staticTypes = map[string]bool{"Image": true, "Font": true, "Stylesheet": true, "Script": true, "Media": true, "Manifest": true, "Other": true}

var (
	validRisk     = map[string]bool{"low": true, "medium": true, "high": true}
	validReplay   = map[string]bool{"never": true, "read_only": true, "allowed": true}
	validDecoder  = map[string]bool{"reddit_html": true, "x_tweetdetail": true, "hn_html": true, "json": true}
	validTrigger  = map[string]bool{"click": true, "scroll": true, "navigate": true}
	validContract = map[string]bool{"id": true, "parent": true, "author": true, "time": true, "text": true, "unknown_share": true}
)

// Thread URL shapes per policy. Reddit ids and subreddit names are
// case-insensitive on the site, X status ids are decimal, HN items are numeric.
var (
	redditThreadRe = regexp.MustCompile(`(?i)^/r/([^/]+)/comments/([a-z0-9]+)`)
	xStatusRe      = regexp.MustCompile(`(?i)^/(?:i/web|[^/]+)/status/([0-9]+)`)
)

// Match returns the shipped policy covering rawURL's host. ok reports host
// coverage, not thread-ness: a non-thread page on a covered host still gets the
// site policy (so the generic policy can never widen admission on a high-risk
// site), and Inputs is then incomplete. A thread URL yields every key in
// Policy.Inputs.
func Match(rawURL string) (*Policy, Inputs, bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return nil, nil, false
	}
	host := strings.ToLower(u.Hostname())
	policies, err := loadAll()
	if err != nil {
		return nil, nil, false
	}
	for _, p := range policies {
		for _, h := range p.Hosts {
			if h == host {
				return p.clone(), p.inputsOf(u), true
			}
		}
	}
	return nil, nil, false
}

func (p *Policy) inputsOf(u *url.URL) Inputs {
	in := Inputs{}
	switch p.ID {
	case "reddit":
		if m := redditThreadRe.FindStringSubmatch(u.EscapedPath()); m != nil {
			in["sub"], in["t3"] = strings.ToLower(m[1]), strings.ToLower(m[2])
		}
	case "x":
		if m := xStatusRe.FindStringSubmatch(u.EscapedPath()); m != nil {
			in["status"] = m[1]
		}
	case "hn":
		if u.EscapedPath() == "/item" {
			if id := u.Query().Get("id"); id != "" && isDigits(id) {
				in["item"] = id
			}
		}
	}
	return in
}

// Complete reports whether in carries every canonical key the policy declares,
// i.e. whether the URL it came from was a thread page.
func (p *Policy) Complete(in Inputs) bool {
	if len(p.Inputs) == 0 {
		return false
	}
	for _, key := range p.Inputs {
		if in[key] == "" {
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// Load returns a private copy of the shipped policy id, or the load error that
// disabled the set.
func Load(id string) (*Policy, error) {
	policies, err := loadAll()
	if err != nil {
		return nil, err
	}
	for _, p := range policies {
		if p.ID == id {
			return p.clone(), nil
		}
	}
	return nil, fmt.Errorf("sitepolicy: unknown policy %q", id)
}

// All returns private copies of every shipped policy sorted by ID, or nil when
// the embedded set failed validation (see Validate for the reason).
func All() []*Policy {
	policies, err := loadAll()
	if err != nil {
		return nil
	}
	out := make([]*Policy, 0, len(policies))
	for _, p := range policies {
		out = append(out, p.clone())
	}
	return out
}

// Validate reports why the embedded set is unusable; nil when it loaded.
func Validate() error {
	_, err := loadAll()
	return err
}

// Generic is the data-class policy for sites without a shipped file: GET
// documents and XHR/Fetch JSON of the site's own registrable domain, 8 MiB
// cap, replay read_only, low ToS risk. Unbound (no Hosts), Admit treats every
// URL as first-party to itself; LedgerParams then emits empty hosts, which the
// extension never matches, so bind it with GenericFor before opening a ledger.
func Generic() *Policy {
	return &Policy{
		ID:      "generic",
		Version: "1",
		TOSRisk: "low",
		Replay:  "read_only",
		BodyOps: []BodyOp{
			{Op: "document", Method: "GET", PathRe: "^/", Types: []string{"Document"}},
			{Op: "json", Method: "GET", PathRe: "^/", Types: []string{"XHR", "Fetch"}, Decoder: "json"},
			{Op: "json", Method: "POST", PathRe: "^/", Types: []string{"XHR", "Fetch"}, Decoder: "json"},
		},
		Primary:   struct{ Op, PathRe string }{Op: "document", PathRe: "^/"},
		Contracts: map[string]float64{"id": 0, "parent": 0.01, "author": 0.05, "time": 0.05, "text": 0.1, "unknown_share": 0.1},
		Pacing:    Pacing{MinIntervalMS: 500, MaxInFlight: 1, AdvancesPerRead: 20},
		Stop:      Stop{Statuses: []int{401, 403, 429}, Headers: []string{"cf-mitigated"}},
	}
}

// GenericFor binds Generic to the registrable domain of rawURL so that Admit
// and LedgerParams keep only first-party bodies.
func GenericFor(rawURL string) *Policy {
	p := Generic()
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return p
	}
	domain := RegistrableDomain(u.Hostname())
	p.Hosts = []string{domain}
	for i := range p.BodyOps {
		p.BodyOps[i].Host = domain
	}
	return p
}

// Admit mirrors the extension's body admission so Go and the ledger agree on
// which bodies exist: static types never, private URLs never, otherwise the
// first body op whose method, host (exact or dot-suffix, never substring),
// path regexp and resource type all match.
func (p *Policy) Admit(method, rawURL, resourceType string) (string, bool) {
	if staticTypes[resourceType] {
		return "", false
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return "", false
	}
	if p.Private(rawURL) {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	for _, op := range p.BodyOps {
		if !strings.EqualFold(op.Method, method) || !p.opHostMatches(op, host) {
			continue
		}
		if op.PathRe != "" {
			re, err := compiled(op.PathRe)
			if err != nil || !re.MatchString(u.EscapedPath()) {
				continue
			}
		}
		if len(op.Types) > 0 && !containsFold(op.Types, resourceType) {
			continue
		}
		return op.Op, true
	}
	return "", false
}

// IsPrimary reports whether rawURL is the response that leads a thread read. A
// body op can admit several paths; only the primary path carries the thread.
func (p *Policy) IsPrimary(method, rawURL, resourceType string) bool {
	op, admitted := p.Admit(method, rawURL, resourceType)
	if !admitted || op != p.Primary.Op {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	re, err := compiled(p.Primary.PathRe)
	return err == nil && re.MatchString(u.EscapedPath())
}

func (p *Policy) opHostMatches(op BodyOp, host string) bool {
	if op.Host != "" {
		return hostWithin(host, op.Host)
	}
	if len(p.Hosts) > 0 {
		for _, h := range p.Hosts {
			if hostWithin(host, h) {
				return true
			}
		}
		return false
	}
	return hostWithin(host, RegistrableDomain(host))
}

func hostWithin(host, domain string) bool {
	domain = strings.ToLower(domain)
	return domain != "" && (host == domain || strings.HasSuffix(host, "."+domain))
}

// Private reports whether any private path is a substring of rawURL, the same
// test the extension applies before fetching a body.
func (p *Policy) Private(rawURL string) bool {
	for _, s := range p.PrivatePaths {
		if s != "" && strings.Contains(rawURL, s) {
			return true
		}
	}
	return false
}

// HasSignature reports the first declared per-request signature header present
// in headers (case-insensitive on the key; values may be redacted markers).
// Replay is refused when one is present because rep never forges signatures.
func (p *Policy) HasSignature(headers map[string][]string) (string, bool) {
	for _, want := range p.SignatureHeaders {
		for name := range headers {
			if strings.EqualFold(name, want) {
				return strings.ToLower(want), true
			}
		}
	}
	return "", false
}

// LedgerParams builds the browser.ledger.open params of the wire contract,
// without tab_id (ledger.Open adds it). Zero or oversized budgets fall back to
// the extension's ceilings, and every list is a real JSON array.
func (p *Policy) LedgerParams(owner string, maxBody, ring int64, idleMS int) map[string]any {
	if maxBody <= 0 || maxBody > MaxBodyBytes {
		maxBody = MaxBodyBytes
	}
	if ring <= 0 || ring > MaxRingBytes {
		ring = MaxRingBytes
	}
	if idleMS <= 0 {
		idleMS = DefaultIdleMS
	}
	if idleMS > MaxIdleMS {
		idleMS = MaxIdleMS
	}
	ops := make([]map[string]any, 0, len(p.BodyOps))
	for _, op := range p.BodyOps {
		hosts := []string{op.Host}
		if op.Host == "" && len(p.Hosts) > 0 {
			hosts = p.Hosts
		}
		for _, host := range hosts {
			ops = append(ops, map[string]any{
				"op":      op.Op,
				"host":    strings.ToLower(host),
				"method":  strings.ToUpper(op.Method),
				"path_re": op.PathRe,
				"types":   append([]string{}, op.Types...),
			})
		}
	}
	return map[string]any{
		"owner":          owner,
		"policy_id":      p.ID,
		"body_ops":       ops,
		"private_paths":  append([]string{}, p.PrivatePaths...),
		"max_body_bytes": maxBody,
		"ring_bytes":     ring,
		"idle_ms":        idleMS,
	}
}

// Multi-part public suffixes; the ledger and the governor must agree
// on what "first-party" means.
var multiPartSuffixes = map[string]bool{"co.uk": true, "org.uk": true, "ac.uk": true, "com.au": true, "co.jp": true, "co.nz": true, "com.br": true, "co.in": true}

// RegistrableDomain reduces a host to its last two labels (three under a known
// multi-part suffix). IP literals and single labels are returned unchanged.
func RegistrableDomain(host string) string {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return host
	}
	labels := strings.Split(host, ".")
	n := len(labels)
	if n <= 2 {
		return host
	}
	if multiPartSuffixes[labels[n-2]+"."+labels[n-1]] {
		return strings.Join(labels[n-3:], ".")
	}
	return strings.Join(labels[n-2:], ".")
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

// Compiled patterns are cached because Admit runs per ledger entry; an invalid
// pattern (only reachable through caller mutation) admits nothing.
var reCache sync.Map

func compiled(pattern string) (*regexp.Regexp, error) {
	if v, ok := reCache.Load(pattern); ok {
		return v.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	reCache.Store(pattern, re)
	return re, nil
}

func (p *Policy) clone() *Policy {
	c := *p
	c.Hosts = append([]string(nil), p.Hosts...)
	c.SignatureHeaders = append([]string(nil), p.SignatureHeaders...)
	c.PrivatePaths = append([]string(nil), p.PrivatePaths...)
	c.Inputs = append([]string(nil), p.Inputs...)
	c.Continuations = append([]Continuation(nil), p.Continuations...)
	c.BodyOps = make([]BodyOp, len(p.BodyOps))
	for i, op := range p.BodyOps {
		c.BodyOps[i] = op
		c.BodyOps[i].Types = append([]string(nil), op.Types...)
	}
	if p.DOMKeys != nil {
		c.DOMKeys = make(map[string]string, len(p.DOMKeys))
		for k, v := range p.DOMKeys {
			c.DOMKeys[k] = v
		}
	}
	if p.Contracts != nil {
		c.Contracts = make(map[string]float64, len(p.Contracts))
		for k, v := range p.Contracts {
			c.Contracts[k] = v
		}
	}
	c.Stop.Statuses = append([]int(nil), p.Stop.Statuses...)
	c.Stop.Headers = append([]string(nil), p.Stop.Headers...)
	c.Stop.TitleContains = append([]string(nil), p.Stop.TitleContains...)
	c.Stop.RedirectPathPrefixes = append([]string(nil), p.Stop.RedirectPathPrefixes...)
	c.Stop.RedirectQueryKeys = append([]string(nil), p.Stop.RedirectQueryKeys...)
	return &c
}

// The file format is a separate type so the Policy struct can keep the exact
// shape of the shared contract (untagged anonymous structs included) while the
// JSON stays snake_case and strictly validated.
type policyFile struct {
	ID               string             `json:"id"`
	Version          string             `json:"version"`
	Hosts            []string           `json:"hosts"`
	TOSRisk          string             `json:"tos_risk"`
	Replay           string             `json:"replay"`
	SignatureHeaders []string           `json:"signature_headers"`
	BodyOps          []bodyOpFile       `json:"body_ops"`
	PrivatePaths     []string           `json:"private_paths"`
	Primary          primaryFile        `json:"primary"`
	Inputs           []string           `json:"inputs"`
	Continuations    []continuationFile `json:"continuations"`
	DOMKeys          map[string]string  `json:"dom_keys"`
	Contracts        map[string]float64 `json:"contracts"`
	Expected         string             `json:"expected"`
	Pacing           pacingFile         `json:"pacing"`
	Stop             stopFile           `json:"stop"`
}

type bodyOpFile struct {
	Op      string   `json:"op"`
	Method  string   `json:"method"`
	Host    string   `json:"host"`
	PathRe  string   `json:"path_re"`
	Types   []string `json:"types"`
	Decoder string   `json:"decoder"`
}

type primaryFile struct {
	Op     string `json:"op"`
	PathRe string `json:"path_re"`
}

type continuationFile struct {
	Kind     string `json:"kind"`
	Selector string `json:"selector"`
	Trigger  string `json:"trigger"`
}

type pacingFile struct {
	MinIntervalMS   int     `json:"min_interval_ms"`
	MaxInFlight     int     `json:"max_in_flight"`
	AdvancesPerRead int     `json:"advances_per_read"`
	ReserveMin      int     `json:"reserve_min"`
	ReserveFraction float64 `json:"reserve_fraction"`
	DailyRecords    int     `json:"daily_records"`
}

type stopFile struct {
	Statuses             []int    `json:"statuses"`
	Headers              []string `json:"headers"`
	TitleContains        []string `json:"title_contains"`
	RedirectPathPrefixes []string `json:"redirect_path_prefixes"`
	RedirectQueryKeys    []string `json:"redirect_query_keys"`
}

func (f policyFile) policy() *Policy {
	p := &Policy{
		ID: f.ID, Version: f.Version, Hosts: f.Hosts, TOSRisk: f.TOSRisk, Replay: f.Replay,
		SignatureHeaders: f.SignatureHeaders, PrivatePaths: f.PrivatePaths, Inputs: f.Inputs,
		DOMKeys: f.DOMKeys, Contracts: f.Contracts, Expected: f.Expected,
		Pacing: Pacing{MinIntervalMS: f.Pacing.MinIntervalMS, MaxInFlight: f.Pacing.MaxInFlight, AdvancesPerRead: f.Pacing.AdvancesPerRead, ReserveMin: f.Pacing.ReserveMin, ReserveFraction: f.Pacing.ReserveFraction, DailyRecords: f.Pacing.DailyRecords},
		Stop:   Stop{Statuses: f.Stop.Statuses, Headers: f.Stop.Headers, TitleContains: f.Stop.TitleContains, RedirectPathPrefixes: f.Stop.RedirectPathPrefixes, RedirectQueryKeys: f.Stop.RedirectQueryKeys},
	}
	p.Primary.Op, p.Primary.PathRe = f.Primary.Op, f.Primary.PathRe
	for _, op := range f.BodyOps {
		p.BodyOps = append(p.BodyOps, BodyOp{Op: op.Op, Method: op.Method, Host: op.Host, PathRe: op.PathRe, Types: op.Types, Decoder: op.Decoder})
	}
	for _, c := range f.Continuations {
		p.Continuations = append(p.Continuations, Continuation{Kind: c.Kind, Selector: c.Selector, Trigger: c.Trigger})
	}
	for i, h := range p.Hosts {
		p.Hosts[i] = strings.ToLower(h)
	}
	for i, h := range p.SignatureHeaders {
		p.SignatureHeaders[i] = strings.ToLower(h)
	}
	return p
}

var loadOnce struct {
	sync.Once
	policies []*Policy
	err      error
}

func loadAll() ([]*Policy, error) {
	loadOnce.Do(func() {
		loadOnce.policies, loadOnce.err = loadFrom(embedded, "policies")
	})
	return loadOnce.policies, loadOnce.err
}

// loadFrom decodes every *.json under dir strictly. Any single failure fails
// the whole set: a half-loaded policy table would silently change which bodies
// are kept.
func loadFrom(fsys fs.FS, dir string) ([]*Policy, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("sitepolicy: read policies: %w", err)
	}
	var policies []*Policy
	seenHosts := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := fs.ReadFile(fsys, path.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("sitepolicy: read %s: %w", name, err)
		}
		p, err := decodePolicy(data, strings.TrimSuffix(name, ".json"))
		if err != nil {
			return nil, fmt.Errorf("sitepolicy: %s: %w", name, err)
		}
		for _, h := range p.Hosts {
			if other, dup := seenHosts[h]; dup {
				return nil, fmt.Errorf("sitepolicy: host %q claimed by both %s and %s", h, other, p.ID)
			}
			seenHosts[h] = p.ID
		}
		policies = append(policies, p)
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].ID < policies[j].ID })
	return policies, nil
}

func decodePolicy(data []byte, stem string) (*Policy, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f policyFile
	if err := dec.Decode(&f); err != nil {
		return nil, err
	}
	if dec.Decode(new(any)) != io.EOF {
		return nil, errors.New("trailing data after policy object")
	}
	p := f.policy()
	if err := validate(p, stem); err != nil {
		return nil, err
	}
	return p, nil
}

func validate(p *Policy, stem string) error {
	if p.ID == "" || p.ID != stem {
		return fmt.Errorf("id %q must equal the file name %q", p.ID, stem)
	}
	if p.Version == "" {
		return errors.New("version is required")
	}
	if len(p.Hosts) == 0 {
		return errors.New("hosts must not be empty")
	}
	for _, h := range p.Hosts {
		if h == "" || strings.ContainsAny(h, "/:@ ") {
			return fmt.Errorf("host %q must be a bare host name", h)
		}
	}
	if !validRisk[p.TOSRisk] {
		return fmt.Errorf("tos_risk %q must be low|medium|high", p.TOSRisk)
	}
	if !validReplay[p.Replay] {
		return fmt.Errorf("replay %q must be never|read_only|allowed", p.Replay)
	}
	if len(p.BodyOps) == 0 {
		return errors.New("body_ops must not be empty")
	}
	ops := map[string]bool{}
	for i, op := range p.BodyOps {
		if op.Op == "" || op.Method == "" || op.Host == "" || op.PathRe == "" {
			return fmt.Errorf("body_ops[%d] needs op, method, host and path_re", i)
		}
		if op.Method != strings.ToUpper(op.Method) {
			return fmt.Errorf("body_ops[%d] method %q must be upper case", i, op.Method)
		}
		if _, err := compiled(op.PathRe); err != nil {
			return fmt.Errorf("body_ops[%d] path_re: %w", i, err)
		}
		if !validDecoder[op.Decoder] {
			return fmt.Errorf("body_ops[%d] decoder %q unknown", i, op.Decoder)
		}
		for _, t := range op.Types {
			if t == "" || staticTypes[t] {
				return fmt.Errorf("body_ops[%d] type %q is never admitted", i, t)
			}
		}
		ops[op.Op] = true
	}
	if !ops[p.Primary.Op] {
		return fmt.Errorf("primary op %q is not a body op", p.Primary.Op)
	}
	if _, err := compiled(p.Primary.PathRe); err != nil {
		return fmt.Errorf("primary path_re: %w", err)
	}
	if len(p.Inputs) == 0 {
		return errors.New("inputs must not be empty")
	}
	for i, c := range p.Continuations {
		if c.Kind == "" || c.Selector == "" || !validTrigger[c.Trigger] {
			return fmt.Errorf("continuations[%d] needs kind, selector and trigger click|scroll|navigate", i)
		}
	}
	for k, v := range p.Contracts {
		if !validContract[k] || v < 0 || v > 1 {
			return fmt.Errorf("contracts[%q]=%v must be a known field with a rate in [0,1]", k, v)
		}
	}
	if p.Pacing.MinIntervalMS <= 0 || p.Pacing.MaxInFlight < 1 || p.Pacing.AdvancesPerRead < 1 {
		return errors.New("pacing needs min_interval_ms > 0, max_in_flight >= 1, advances_per_read >= 1")
	}
	if p.Pacing.ReserveFraction < 0 || p.Pacing.ReserveFraction >= 1 || p.Pacing.ReserveMin < 0 || p.Pacing.DailyRecords < 0 {
		return errors.New("pacing reserve_fraction must be in [0,1) and counters non-negative")
	}
	for _, s := range p.Stop.Statuses {
		if s < 100 || s > 599 {
			return fmt.Errorf("stop status %d out of range", s)
		}
	}
	return nil
}
