package cmd

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/noise"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	auditSaved       string
	auditTop         int
	auditPrimaryOnly bool
	auditForce       bool
)

// auditCmd is the one-shot "what should I investigate" primitive — folds
// ~15 exploratory list/body/curl calls into a single ranked findings table
// so the agent can skip reconnaissance and jump to tamper targets.
var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Rank captured requests by tamper-worthiness",
	Long: `Scan captured HTTP traffic and return a ranked findings table.

Computes per-request signals (auth, mutation, status, noise, client-asserted
values, input reflection) and scores each request by offensive-security
interest. Full value-graph + per-request flags are spilled to
~/.local/share/rep-cli/audit/<hash>.json so the agent can load the raw
data without blowing the token budget.

Examples:
  rep audit                    Top 20 findings from live.json
  rep audit --top 50           Top 50 findings
  rep audit --saved latest     Audit most recent saved session
  rep audit --primary-only     Restrict to primary-domain requests
  rep audit --force            Bypass cache and recompute`,
	RunE: runAudit,
}

func init() {
	rootCmd.AddCommand(auditCmd)
	auditCmd.Flags().StringVar(&auditSaved, "saved", "", "Read from saved session (ID, prefix, or 'latest')")
	auditCmd.Flags().IntVar(&auditTop, "top", 20, "Number of findings to return")
	auditCmd.Flags().BoolVar(&auditPrimaryOnly, "primary-only", false, "Restrict to primary-domain requests")
	auditCmd.Flags().BoolVar(&auditForce, "force", false, "Bypass cache and recompute")
}

// Finding is the per-request row the agent consumes.
type Finding struct {
	ID           string   `json:"id"`
	SemanticID   string   `json:"semantic_id"`
	Score        int      `json:"score"`
	Method       string   `json:"method"`
	URL          string   `json:"url"`
	Status       int      `json:"status"`
	Signals      []string `json:"signals"`
	LinkedValues []string `json:"linked_values,omitempty"`
}

// requestFlags is the full boolean-predicate record we spill to disk so
// downstream tools can consume raw flags without re-running analysis.
type requestFlags struct {
	ID                     string `json:"id"`
	SemanticID             string `json:"semantic_id"`
	Method                 string `json:"method"`
	URL                    string `json:"url"`
	Domain                 string `json:"domain"`
	Status                 int    `json:"status"`
	HasAuth                bool   `json:"has_auth"`
	IsMutation             bool   `json:"is_mutation"`
	StatusInteresting      bool   `json:"status_interesting"`
	IsNoise                bool   `json:"is_noise"`
	CarriesClientAssertion bool   `json:"carries_client_assertion"`
	ReflectsInput          bool   `json:"reflects_input"`
	Score                  int    `json:"score"`
}

// spillPayload is the full audit artifact persisted under audit/<hash>.json.
type spillPayload struct {
	SessionHash string                    `json:"session_hash"`
	Source      string                    `json:"source"`
	Flags       []requestFlags            `json:"flags"`
	ValueGraph  map[string]valueGraphNode `json:"value_graph"`
}

// valueGraphNode captures provenance for a single observed string. A value
// consumed without ever being produced is client-asserted — prime tamper bait.
type valueGraphNode struct {
	Value          string   `json:"value"`
	Producers      []string `json:"producers"`
	Consumers      []string `json:"consumers"`
	ClientAsserted bool     `json:"client_asserted"`
}

// Literal keys that signal a client-asserted pricing/quantity/offer field.
var clientAssertionKeys = []string{
	"price", "amount", "quantity", "currency", "offertype", "promocode",
}

func runAudit(cmd *cobra.Command, args []string) error {
	requests, source, rawBytes, err := auditLoadRequests()
	if err != nil {
		return err
	}
	if len(requests) == 0 {
		emitLiveEmpty("audit")
		return nil
	}

	primarySet := map[string]bool{}
	if s, err := store.Get(); err == nil {
		for d := range s.PrimaryDomains {
			primarySet[strings.ToLower(d)] = true
		}
	}

	// Filter to in-scope requests.
	scoped := make([]store.Request, 0, len(requests))
	for i := range requests {
		r := &requests[i]
		if r.Domain == "" {
			store.ComputeRequestFields(r)
		}
		if auditPrimaryOnly && !primarySet[strings.ToLower(r.Domain)] {
			continue
		}
		scoped = append(scoped, *r)
	}

	// Cache key must invalidate when the scope flag flips.
	sessionHash := auditSessionHash(rawBytes, auditPrimaryOnly)
	spillPath, err := auditSpillPath(sessionHash)
	if err != nil {
		return fmt.Errorf("spill path: %w", err)
	}

	var payload spillPayload
	if !auditForce {
		if cached, ok := auditLoadSpill(spillPath); ok {
			payload = cached
		}
	}
	if payload.SessionHash == "" {
		graph := buildValueGraph(scoped)
		flags := make([]requestFlags, 0, len(scoped))
		for i := range scoped {
			flags = append(flags, computeFlags(&scoped[i], graph))
		}
		payload = spillPayload{SessionHash: sessionHash, Source: source, Flags: flags, ValueGraph: graph}
		if err := auditWriteSpill(spillPath, payload); err != nil {
			fmt.Fprintf(os.Stderr, "warn: failed to write audit spill: %v\n", err)
		}
	}

	// Rank: keep positive-score rows, sort by (score desc, id asc).
	findings := make([]Finding, 0, len(payload.Flags))
	for _, f := range payload.Flags {
		if f.Score <= 0 {
			continue
		}
		findings = append(findings, Finding{
			ID: f.ID, SemanticID: f.SemanticID, Score: f.Score,
			Method: f.Method, URL: f.URL, Status: f.Status,
			Signals:      signalsFor(f),
			LinkedValues: linkedValuesFor(f.ID, payload.ValueGraph),
		})
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Score != findings[j].Score {
			return findings[i].Score > findings[j].Score
		}
		return findings[i].ID < findings[j].ID
	})

	requestedTop := auditTop
	if requestedTop <= 0 {
		requestedTop = 20
	}
	top := requestedTop
	if top > len(findings) {
		top = len(findings)
	}
	trimmed, truncation := shrinkToBudget(findings[:top], len(findings))

	jsonMode := getOutputMode() == "json" || !isTTY()
	if jsonMode {
		return emitAuditJSON(source, spillPath, payload, trimmed, truncation, len(findings))
	}
	return emitAuditTable(source, trimmed, len(scoped), len(findings), spillPath)
}

// auditLoadRequests forks on --saved vs live.json and returns raw bytes so
// we can hash for the cache key without re-reading the file.
func auditLoadRequests() ([]store.Request, string, []byte, error) {
	if auditSaved != "" {
		s, err := store.Get()
		if err != nil {
			return nil, "", nil, fmt.Errorf("load store: %w", err)
		}
		var session *store.Session
		if auditSaved == "latest" || auditSaved == "last" {
			session = s.GetLatestSession()
		} else {
			session = s.GetSession(auditSaved)
		}
		if session == nil {
			ae := output.NewAgentError(
				output.ErrCodeSessionNotFound, "audit",
				fmt.Sprintf("no session matched %q", auditSaved),
				"rep sessions  # list available sessions",
			)
			return nil, "", nil, output.EmitAgentError(os.Stdout, ae, getOutputMode() == "json")
		}
		raw, _ := sonic.Marshal(session.Requests)
		return session.Requests, "saved/" + session.ID, raw, nil
	}
	livePath, err := store.GetLiveFilePath()
	if err != nil {
		return nil, "", nil, fmt.Errorf("live path: %w", err)
	}
	raw, readErr := os.ReadFile(livePath)
	if readErr != nil {
		return nil, "", nil, emitLiveUnavailable("audit", readErr)
	}
	var export store.Export
	if err := sonic.Unmarshal(raw, &export); err != nil {
		return nil, "", nil, emitLiveUnavailable("audit", err)
	}
	return export.Requests, "live.json", raw, nil
}

func auditSessionHash(raw []byte, primaryOnly bool) string {
	h := sha256.New()
	h.Write(raw)
	if primaryOnly {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func auditSpillPath(sessionHash string) (string, error) {
	base, err := store.GetStorePath()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "audit")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionHash+".json"), nil
}

func auditLoadSpill(path string) (spillPayload, bool) {
	var p spillPayload
	data, err := os.ReadFile(path)
	if err != nil {
		return p, false
	}
	if err := sonic.Unmarshal(data, &p); err != nil || p.SessionHash == "" || len(p.Flags) == 0 {
		return p, false
	}
	return p, true
}

func auditWriteSpill(path string, p spillPayload) error {
	data, err := sonic.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// computeFlags runs every predicate for one request and sums to a score
// per the scoring table in the task spec. Noise domains zero out.
func computeFlags(r *store.Request, graph map[string]valueGraphNode) requestFlags {
	store.ComputeRequestFields(r)
	f := requestFlags{
		ID: r.ID, SemanticID: string(r.SemanticID),
		Method: r.Method, URL: r.URL, Domain: r.Domain,
	}
	if r.Response != nil {
		f.Status = r.Response.Status
	}
	f.HasAuth = hasAuthSignal(r)
	f.IsMutation = isMutation(r.Method)
	f.StatusInteresting = statusInteresting(r)
	f.IsNoise = isNoiseDomain(r.Domain)
	f.CarriesClientAssertion = carriesClientAssertion(r)
	f.ReflectsInput = reflectsInput(r)

	s := 0
	if f.HasAuth {
		s += 3
	}
	if f.IsMutation {
		s += 2
	}
	if f.CarriesClientAssertion {
		s += 4
	}
	if f.ReflectsInput {
		s += 3
	}
	if f.StatusInteresting {
		s += 1
	}
	if f.IsNoise {
		s = 0
	}
	f.Score = s
	return f
}

// hasAuthSignal: Authorization, Cookie, or common API-key/CSRF header.
func hasAuthSignal(r *store.Request) bool {
	if store.HeaderFirst(r.Headers, "authorization") != "" || store.HeaderFirst(r.Headers, "cookie") != "" {
		return true
	}
	for k := range r.Headers {
		kl := strings.ToLower(k)
		if kl == "x-csrf-token" || kl == "x-xsrf-token" || kl == "x-api-key" || kl == "x-auth-token" ||
			strings.Contains(kl, "api-key") || strings.Contains(kl, "apikey") || strings.Contains(kl, "csrf-token") {
			return true
		}
	}
	return false
}

func isMutation(method string) bool {
	m := strings.ToUpper(method)
	return m == "POST" || m == "PUT" || m == "PATCH" || m == "DELETE"
}

// statusInteresting: 4xx/5xx, or 3xx that redirects off-host.
func statusInteresting(r *store.Request) bool {
	if r.Response == nil {
		return false
	}
	s := r.Response.Status
	if s >= 400 {
		return true
	}
	if s >= 300 && s < 400 {
		loc := store.HeaderFirst(r.Response.Headers, "location")
		if loc == "" {
			return false
		}
		if u, err := url.Parse(loc); err == nil && u.Host != "" && !strings.EqualFold(u.Host, r.Domain) {
			return true
		}
	}
	return false
}

var extraNoisePatterns = []string{
	"doubleclick", "google-analytics", "facebook", "bing", "omtrdc",
	"demdex", "adobe-dtm", "rokt", "unagi", "fls-na",
	"cloudfront.net", "akamaihd.net", "akamaiedge.net",
	"images-amazon.com", "ssl-images-amazon", "media-amazon",
}

// isNoiseDomain: the curated noise package plus an inline fallback list.
func isNoiseDomain(domain string) bool {
	if domain == "" {
		return false
	}
	if noise.IsNoise(domain) {
		return true
	}
	dl := strings.ToLower(domain)
	for _, pat := range extraNoisePatterns {
		if strings.Contains(dl, pat) {
			return true
		}
	}
	return false
}

// carriesClientAssertion: literal price/amount/etc. in query, body form key,
// body JSON key, or a long base64 blob that decodes to JSON.
func carriesClientAssertion(r *store.Request) bool {
	if u, err := url.Parse(r.URL); err == nil {
		for k := range u.Query() {
			if isClientAssertionKey(k) {
				return true
			}
		}
	}
	if r.Body != "" && (bodyContainsAssertionKey(r.Body) || containsBase64JSON(r.Body)) {
		return true
	}
	return false
}

func isClientAssertionKey(k string) bool {
	kl := strings.ToLower(k)
	for _, c := range clientAssertionKeys {
		if kl == c {
			return true
		}
	}
	return false
}

func bodyContainsAssertionKey(body string) bool {
	bl := strings.ToLower(body)
	for _, k := range clientAssertionKeys {
		if strings.Contains(bl, k+"=") || strings.Contains(bl, "\""+k+"\"") {
			return true
		}
	}
	return false
}

// containsBase64JSON scans for long base64 runs and tries to decode them as
// JSON. Bounded to 32KB / 16 attempts to avoid O(n*m) on huge bodies.
func containsBase64JSON(s string) bool {
	if len(s) < 32 {
		return false
	}
	const maxScan = 32 * 1024
	if len(s) > maxScan {
		s = s[:maxScan]
	}
	isB64 := func(r rune) bool {
		return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') || r == '+' || r == '/' || r == '=' || r == '-' || r == '_'
	}
	attempts, start := 0, -1
	check := func(tok string) bool {
		if len(tok) < 32 || attempts >= 16 {
			return false
		}
		attempts++
		return tryDecodeJSON(tok)
	}
	for i, r := range s {
		if isB64(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 && check(s[start:i]) {
			return true
		}
		start = -1
	}
	return start >= 0 && check(s[start:])
}

func tryDecodeJSON(tok string) bool {
	for _, dec := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		raw, err := dec.DecodeString(tok)
		if err != nil || len(raw) < 2 {
			continue
		}
		trim := strings.TrimSpace(string(raw))
		if trim == "" || (trim[0] != '{' && trim[0] != '[') {
			continue
		}
		var v any
		if err := sonic.Unmarshal(raw, &v); err == nil {
			return true
		}
	}
	return false
}

// reflectsInput: any request-value ≥ 8 chars appears verbatim in response.
func reflectsInput(r *store.Request) bool {
	if r.Response == nil || r.Response.Body == "" {
		return false
	}
	reqVals := collectRequestValues(r)
	body := r.Response.Body
	for v := range reqVals {
		if len(v) >= 8 && strings.Contains(body, v) {
			return true
		}
	}
	return false
}

// buildValueGraph maps every observed string ≥ 8 chars to its producer +
// consumer request IDs. Values with no producer are client-asserted.
func buildValueGraph(requests []store.Request) map[string]valueGraphNode {
	producers := map[string]map[string]bool{}
	consumers := map[string]map[string]bool{}
	add := func(m map[string]map[string]bool, val, id string) {
		if len(val) < 8 || len(val) > 512 {
			return
		}
		set, ok := m[val]
		if !ok {
			set = map[string]bool{}
			m[val] = set
		}
		set[id] = true
	}
	for i := range requests {
		r := &requests[i]
		for v := range collectRequestValues(r) {
			add(consumers, v, r.ID)
		}
		if r.Response != nil {
			for v := range collectResponseValues(r) {
				add(producers, v, r.ID)
			}
		}
	}
	// Only keep values that showed up at least once as a consumer — that's
	// the set reflects_input / linked_values care about.
	graph := make(map[string]valueGraphNode, len(consumers))
	for val, cset := range consumers {
		node := valueGraphNode{Value: val}
		for id := range cset {
			node.Consumers = append(node.Consumers, id)
		}
		if pset, ok := producers[val]; ok {
			for id := range pset {
				node.Producers = append(node.Producers, id)
			}
		}
		sort.Strings(node.Producers)
		sort.Strings(node.Consumers)
		node.ClientAsserted = len(node.Producers) == 0
		graph[val] = node
	}
	return graph
}

// collectRequestValues pulls strings from query, form body, and JSON body.
func collectRequestValues(r *store.Request) map[string]bool {
	out := map[string]bool{}
	addLong := func(v string) {
		if len(v) >= 8 {
			out[v] = true
		}
	}
	if u, err := url.Parse(r.URL); err == nil {
		for _, vs := range u.Query() {
			for _, v := range vs {
				addLong(v)
			}
		}
	}
	if r.Body == "" {
		return out
	}
	ct := strings.ToLower(store.HeaderFirst(r.Headers, "content-type"))
	switch {
	case strings.Contains(ct, "x-www-form-urlencoded"):
		if vals, err := url.ParseQuery(r.Body); err == nil {
			for _, vs := range vals {
				for _, v := range vs {
					addLong(v)
				}
			}
		}
	case looksJSON(r.Body):
		collectJSONStrings(r.Body, out)
	default:
		collectLongTokens(r.Body, out)
	}
	return out
}

// collectResponseValues parses JSON bodies and falls back to token scan for
// HTML/text. Capped so we don't parse megabytes of unrelated markup.
func collectResponseValues(r *store.Request) map[string]bool {
	out := map[string]bool{}
	if r.Response == nil || r.Response.Body == "" {
		return out
	}
	body := r.Response.Body
	const maxBody = 128 * 1024
	if len(body) > maxBody {
		body = body[:maxBody]
	}
	if looksJSON(body) {
		collectJSONStrings(body, out)
	} else {
		collectLongTokens(body, out)
	}
	return out
}

func looksJSON(s string) bool {
	t := strings.TrimLeft(s, " \t\r\n")
	return strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")
}

func collectJSONStrings(body string, out map[string]bool) {
	var v any
	if err := sonic.UnmarshalString(body, &v); err != nil {
		return
	}
	walkJSON(v, out)
}

func walkJSON(v any, out map[string]bool) {
	switch vv := v.(type) {
	case string:
		if len(vv) >= 8 && len(vv) <= 512 {
			out[vv] = true
		}
	case map[string]any:
		for _, c := range vv {
			walkJSON(c, out)
		}
	case []any:
		for _, c := range vv {
			walkJSON(c, out)
		}
	}
}

// collectLongTokens: cheap fallback for HTML/text — slice on non-token chars
// and keep runs of 8–128 chars. Captures ASINs, UUIDs, session IDs.
func collectLongTokens(body string, out map[string]bool) {
	isTok := func(r rune) bool {
		return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.'
	}
	const maxScan = 64 * 1024
	if len(body) > maxScan {
		body = body[:maxScan]
	}
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		tok := body[start:end]
		if len(tok) >= 8 && len(tok) <= 128 {
			out[tok] = true
		}
		start = -1
	}
	for i, r := range body {
		if isTok(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		flush(i)
	}
	flush(len(body))
}

func signalsFor(f requestFlags) []string {
	var sigs []string
	add := func(b bool, s string) {
		if b {
			sigs = append(sigs, s)
		}
	}
	add(f.HasAuth, "has_auth")
	add(f.IsMutation, "is_mutation")
	add(f.CarriesClientAssertion, "carries_client_assertion")
	add(f.ReflectsInput, "reflects_input")
	add(f.StatusInteresting, "status_interesting")
	add(f.IsNoise, "is_noise")
	return sigs
}

// linkedValuesFor: up to 3 client-asserted values this request consumed,
// ranked by global consumer-count (shared-across-many-calls wins).
func linkedValuesFor(id string, graph map[string]valueGraphNode) []string {
	type scored struct {
		v     string
		count int
	}
	var cands []scored
	for val, node := range graph {
		if !node.ClientAsserted {
			continue
		}
		for _, c := range node.Consumers {
			if c == id {
				cands = append(cands, scored{val, len(node.Consumers)})
				break
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].count != cands[j].count {
			return cands[i].count > cands[j].count
		}
		return cands[i].v < cands[j].v
	})
	if len(cands) > 3 {
		cands = cands[:3]
	}
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.v
	}
	return out
}

// shrinkToBudget halves --top until JSON fits the overflow threshold.
func shrinkToBudget(findings []Finding, totalScored int) ([]Finding, *output.TruncationInfo) {
	if len(findings) == 0 {
		return findings, nil
	}
	cur := findings
	for len(cur) > 1 {
		data, err := sonic.Marshal(cur)
		if err != nil || len(data) <= output.OverflowThreshold {
			break
		}
		cur = cur[:len(cur)/2]
	}
	if len(cur) < len(findings) {
		return cur, &output.TruncationInfo{Reason: "top-reduced", Returned: len(cur), Total: totalScored}
	}
	return cur, nil
}

func emitAuditJSON(source, spillPath string, payload spillPayload, findings []Finding, trunc *output.TruncationInfo, totalScored int) error {
	env := output.WrapData("audit", source, map[string]interface{}{
		"total_candidates": len(payload.Flags),
		"scored":           totalScored,
		"findings":         findings,
		"value_graph_path": spillPath,
	})
	env.Filters = map[string]interface{}{
		"primary_only": auditPrimaryOnly,
		"top":          auditTop,
	}
	if trunc != nil {
		env.Truncation = trunc
	}
	env.Suggest = []string{
		"rep body <id>  # inspect a finding's full request/response",
		"rep curl <id>  # rebuild as curl for tamper testing",
	}
	out, err := sonic.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

func emitAuditTable(source string, findings []Finding, totalCandidates, totalScored int, spillPath string) error {
	fmt.Println(output.FormatSourceLine(source))
	fmt.Printf("scanned %d requests, %d scored, %d shown\n\n", totalCandidates, totalScored, len(findings))
	if len(findings) == 0 {
		pterm.Info.Println("No findings. Try capturing more traffic or dropping --primary-only.")
		return nil
	}
	for i, f := range findings {
		shortURL := f.URL
		if len(shortURL) > 70 {
			shortURL = shortURL[:67] + "..."
		}
		fmt.Printf("%2d. [%2d] %-6s %-70s  %s\n", i+1, f.Score, f.Method, shortURL, strings.Join(shortSignals(f.Signals), ","))
	}
	fmt.Printf("\nvalue_graph: %s\n", spillPath)
	return nil
}

var signalShort = map[string]string{
	"has_auth":                 "auth",
	"is_mutation":              "mut",
	"carries_client_assertion": "cli-assert",
	"reflects_input":           "reflect",
	"status_interesting":       "status",
	"is_noise":                 "noise",
}

func shortSignals(sigs []string) []string {
	out := make([]string, 0, len(sigs))
	for _, s := range sigs {
		if short, ok := signalShort[s]; ok {
			out = append(out, short)
		}
	}
	return out
}

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
