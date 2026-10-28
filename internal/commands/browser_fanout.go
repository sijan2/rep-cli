package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

// browser capture fanout replays one captured request N times with a value
// matrix, from inside a tab on the request's own origin, using the page's own
// session. Requests run in parallel inside the page; the ordinary action
// capture archives every response body. It is the generic form of the
// Marketplace tool's in-page worker pool: no navigation per query, no sleeps.

const maxFanoutVariants = 256
const maxFanoutConcurrency = 32

type fanoutVariant struct {
	Index   int               `json:"index"`
	Label   string            `json:"label"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    *string           `json:"body,omitempty"`
}

type fanoutOutcome struct {
	Index       int    `json:"index"`
	Label       string `json:"label"`
	Method      string `json:"method"`
	URL         string `json:"url"`
	Status      int    `json:"status,omitempty"`
	OK          bool   `json:"ok"`
	Bytes       int64  `json:"bytes,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	ElapsedMS   int64  `json:"elapsed_ms,omitempty"`
	Error       string `json:"error,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	BodyState   string `json:"body_state,omitempty"`
	Match       string `json:"match"`
}

// Browsers ignore forbidden request headers in fetch; cookies and identity
// headers come from the page session instead of the captured bytes.
var fanoutDroppedHeaders = map[string]bool{"cookie": true, "cookie2": true, "host": true, "content-length": true, "connection": true, "keep-alive": true, "transfer-encoding": true, "te": true, "trailer": true, "upgrade": true, "origin": true, "referer": true, "user-agent": true, "accept-encoding": true, "accept-charset": true, "date": true, "dnt": true, "expect": true, "via": true, "set-cookie": true, "priority": true}

func fanoutForwardHeaders(headers store.HeaderMap) map[string]string {
	result := map[string]string{}
	for name, values := range headers {
		lower := strings.ToLower(strings.TrimSpace(name))
		if lower == "" || strings.HasPrefix(lower, ":") || strings.HasPrefix(lower, "sec-") || strings.HasPrefix(lower, "proxy-") || fanoutDroppedHeaders[lower] || len(values) == 0 {
			continue
		}
		result[lower] = values[0]
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func fanoutOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", errors.New("the captured request has no HTTP(S) origin")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

// fanoutVariants applies the constant mutations and one matrix value per variant.
// Every variant must stay on the source request's target origin, and the replay
// runs from the document that originally issued the request: the page session
// is the credential, and a request the page itself makes cross-origin (a
// GraphQL host beside the site) is replayable from that page under the same
// CORS grant. The returned origin is the initiating document's origin.
func fanoutVariants(source *store.Request, matrixKey string, values []string, constant []mutation) ([]fanoutVariant, string, error) {
	if source == nil {
		return nil, "", errors.New("captured request was not found")
	}
	if len(values) == 0 || len(values) > maxFanoutVariants {
		return nil, "", fmt.Errorf("provide 1 to %d matrix values", maxFanoutVariants)
	}
	origin, err := fanoutOrigin(source.URL)
	if err != nil {
		return nil, "", err
	}
	initiator := origin
	if source.PageURL != "" {
		if pageOrigin, err := fanoutOrigin(source.PageURL); err == nil {
			initiator = pageOrigin
		}
	}
	variants := make([]fanoutVariant, 0, len(values))
	for index, value := range values {
		mutated, err := applyMutations(source, append(append([]mutation{}, constant...), mutation{kind: strings.SplitN(matrixKey, ".", 2)[0], key: strings.SplitN(matrixKey, ".", 2)[1], value: value}))
		if err != nil {
			return nil, "", fmt.Errorf("variant %d: %w", index, err)
		}
		variantOrigin, err := fanoutOrigin(mutated.URL)
		if err != nil || variantOrigin != origin {
			return nil, "", fmt.Errorf("variant %d leaves the origin %s", index, origin)
		}
		method := strings.ToUpper(strings.TrimSpace(mutated.Method))
		if method == "" {
			method = "GET"
		}
		variant := fanoutVariant{Index: index, Label: value, Method: method, URL: mutated.URL, Headers: fanoutForwardHeaders(mutated.Headers)}
		if method != "GET" && method != "HEAD" && mutated.Body != "" {
			body := mutated.Body
			variant.Body = &body
		}
		variants = append(variants, variant)
	}
	return variants, initiator, nil
}

// fanoutScript runs the variants in the page with a bounded worker pool. Each
// response body is read to completion so the capture holds the full bytes; the
// evaluation itself returns only small per-variant metadata.
func fanoutScript(variants []fanoutVariant, concurrency int) (string, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(map[string]any{"variants": variants, "concurrency": concurrency}); err != nil {
		return "", err
	}
	// encoding/json escapes U+2028 and U+2029 unconditionally, so the embedded
	// job is also a valid classic JavaScript string literal; nothing else is needed.
	job := strings.TrimSpace(encoded.String())
	return `(async (JOB) => {
  const results = new Array(JOB.variants.length);
  let next = 0;
  const started = performance.now();
  const run = async (variant) => {
    const t0 = performance.now();
    const outcome = { index: variant.index };
    try {
      const init = { method: variant.method, headers: variant.headers || {}, credentials: 'include', redirect: 'follow', cache: 'no-store' };
      if (variant.body != null) init.body = variant.body;
      const response = await fetch(variant.url, init);
      outcome.status = response.status;
      outcome.ok = response.ok;
      outcome.content_type = response.headers.get('content-type') || '';
      try {
        outcome.bytes = (await response.arrayBuffer()).byteLength;
      } catch (error) {
        outcome.error = 'body_read_failed: ' + String(error && error.message || error).slice(0, 160);
      }
    } catch (error) {
      outcome.error = String(error && error.message || error).slice(0, 200);
    }
    outcome.elapsed_ms = Math.round(performance.now() - t0);
    return outcome;
  };
  const lanes = Array.from({ length: Math.max(1, Math.min(JOB.concurrency, JOB.variants.length)) }, async () => {
    while (next < JOB.variants.length) {
      const index = next++;
      results[index] = await run(JOB.variants[index]);
    }
  });
  await Promise.all(lanes);
  return { variants: results, duration_ms: Math.round(performance.now() - started) };
})(` + job + `)`, nil
}

func fanoutNormalizeURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	parsed.Fragment = ""
	return parsed.String()
}

// fanoutOutcomes joins in-page results with the sealed capture. Same method and
// URL identify a request; identical URLs (body mutations) pair in start order.
// Anything else is reported as unmatched rather than guessed.
func fanoutOutcomes(variants []fanoutVariant, evaluation json.RawMessage, requests []store.Request) []fanoutOutcome {
	outcomes := make([]fanoutOutcome, len(variants))
	type pageResult struct {
		Index       int    `json:"index"`
		Status      int    `json:"status"`
		OK          bool   `json:"ok"`
		Bytes       int64  `json:"bytes"`
		ContentType string `json:"content_type"`
		ElapsedMS   int64  `json:"elapsed_ms"`
		Error       string `json:"error"`
	}
	var page struct {
		Variants []pageResult `json:"variants"`
	}
	_ = json.Unmarshal(evaluation, &page)
	byIndex := map[int]pageResult{}
	for _, item := range page.Variants {
		byIndex[item.Index] = item
	}
	type key struct{ method, url string }
	captured := map[key][]*store.Request{}
	for i := range requests {
		request := &requests[i]
		if request.RecordKind != "" && request.RecordKind != "http" {
			continue
		}
		k := key{strings.ToUpper(strings.TrimSpace(request.Method)), fanoutNormalizeURL(request.URL)}
		captured[k] = append(captured[k], request)
	}
	for _, list := range captured {
		sort.SliceStable(list, func(i, j int) bool { return list[i].StartOrdinal < list[j].StartOrdinal })
	}
	consumed := map[key]int{}
	for i, variant := range variants {
		outcome := fanoutOutcome{Index: variant.Index, Label: variant.Label, Method: variant.Method, URL: variant.URL, Match: "unmatched"}
		if item, ok := byIndex[variant.Index]; ok {
			outcome.Status, outcome.OK, outcome.Bytes, outcome.ContentType, outcome.ElapsedMS, outcome.Error = item.Status, item.OK, item.Bytes, item.ContentType, item.ElapsedMS, item.Error
		} else {
			outcome.Error = "no in-page result"
		}
		k := key{variant.Method, fanoutNormalizeURL(variant.URL)}
		list := captured[k]
		if position := consumed[k]; position < len(list) {
			request := list[position]
			consumed[k] = position + 1
			outcome.RequestID, outcome.Match = request.ID, "method_url"
			if len(list) > 1 {
				outcome.Match = "method_url_order"
			}
			outcome.BodyState = "unknown"
			if request.ResponseBodyCapture != nil {
				outcome.BodyState = browserBodyState(request.ResponseBodyCapture.State)
			}
		}
		outcomes[i] = outcome
	}
	return outcomes
}

func newBrowserFanoutCommand(shared *browserCaptureOptions) *cobra.Command {
	var matrix string
	var sets []string
	var concurrency, tab int
	var keepTab bool
	command := &cobra.Command{
		Use:   "fanout <captured-id> --matrix 'query.NAME=[v1,v2,...]'",
		Short: "Replay one captured request in parallel with a value matrix, inside its origin, archiving every response",
		Long: `Replay a captured request many times from a tab on the request's own origin.

The page session supplies cookies; the matrix supplies one changed value per
variant (query.NAME, body.KEY, or header.NAME, like rep replay). Variants run
in parallel inside the page with a bounded worker pool and every response body
is archived by the ordinary action capture. The result pairs each variant with
its captured request ID so bodies can be read with rep body ID --saved HASH.
Without --tab, an owned inactive tab is opened on the origin and closed after.`,
		Args: cobra.ExactArgs(1),
	}
	command.RunE = func(cmd *cobra.Command, args []string) (returnErr error) {
		started := time.Now()
		commandName := "browser capture fanout"
		fail := func(code, message string, suggest ...string) error {
			return output.EmitAgentError(os.Stdout, output.NewAgentError(code, commandName, message, suggest...), getOutputMode() == "json")
		}
		if matrix == "" || concurrency < 1 || concurrency > maxFanoutConcurrency || tab < -1 {
			return fail(output.ErrCodeInvalidArgument, fmt.Sprintf("provide --matrix 'query.NAME=[v1,v2]' and a concurrency of 1 to %d", maxFanoutConcurrency), "rep describe fanout")
		}
		matrixKey, values, err := parseMatrixSpec(matrix)
		if err != nil {
			return fail(output.ErrCodeInvalidArgument, err.Error(), "rep describe fanout")
		}
		constant, err := parseMutationSpecs(sets)
		if err != nil {
			return fail(output.ErrCodeInvalidArgument, err.Error(), "rep describe fanout")
		}
		source := findRequestByID(args[0])
		if source == nil {
			return fail(output.ErrCodeRequestNotFound, "captured request was not found in this task", "rep list --select api", "rep summary --max-bytes 4096")
		}
		variants, origin, err := fanoutVariants(source, matrixKey, values, constant)
		if err != nil {
			return fail(output.ErrCodeInvalidArgument, err.Error(), "rep describe fanout")
		}
		script, err := fanoutScript(variants, concurrency)
		if err != nil {
			return fail(output.ErrCodeInvalidArgument, err.Error())
		}
		timeout := shared.Timeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		record, err := beginBrowserEvidence("browser.fanout", map[string]any{"intent": "parallel in-origin replay of a captured request", "source_request_id": source.ID, "variants": len(variants), "concurrency": concurrency, "matrix_key": matrixKey}, browserSelector, tab)
		if err != nil {
			return emitBrowserCallError(commandName, err)
		}
		defer record.finishOnReturn(&returnErr)
		ctx, cancel := context.WithTimeout(cmd.Context(), timeout+15*time.Second)
		defer cancel()
		client, err := selectBrowserBridge(ctx, browserSelector, commandName)
		if err != nil {
			return err
		}
		phases := map[string]int64{}
		last := started
		phase := func(name string) {
			now := time.Now()
			phases[name] = now.Sub(last).Milliseconds()
			last = now
		}
		phase("connect")
		created := false
		if tab >= 0 {
			var tabs struct {
				Tabs []struct {
					ID  int    `json:"id"`
					URL string `json:"url"`
				} `json:"tabs"`
			}
			if err := client.Call(ctx, "browser.tabs", nil, &tabs); err != nil {
				return emitBrowserCallError(commandName, err)
			}
			found := false
			for _, item := range tabs.Tabs {
				if item.ID == tab {
					found = true
					if tabOrigin, err := fanoutOrigin(item.URL); err != nil || tabOrigin != origin {
						return fail("origin_mismatch", fmt.Sprintf("tab %d is not on %s, the document that issued the captured request; replay runs from that page's session", tab, origin), "rep browser create "+origin+"/ --raw-json", "omit --tab to use an owned tab on that origin")
					}
				}
			}
			if !found {
				return fail("tab_not_found", fmt.Sprintf("tab %d was not found", tab), "rep browser tabs")
			}
		} else {
			var allocated struct {
				Tab *int `json:"tab_id"`
			}
			if err := client.Call(ctx, "browser.create", map[string]any{"url": "about:blank", "active": false}, &allocated); err != nil {
				return emitBrowserCallError(commandName, err)
			}
			if allocated.Tab == nil || *allocated.Tab < 0 {
				return emitBrowserCallError(commandName, errors.New("browser creation returned no tab"))
			}
			tab, created = *allocated.Tab, true
			var navigated map[string]any
			if err := client.Call(ctx, "browser.navigate", map[string]any{"tab_id": tab, "url": origin + "/", "wait": "load", "timeout_ms": timeout.Milliseconds(), "active": false}, &navigated); err != nil {
				closeFanoutTab(client, tab)
				return emitBrowserCallError(commandName, err)
			}
		}
		phase("tab")
		handoff, err := prepareBrowserCapture(ctx, client)
		if err != nil {
			if created && !keepTab {
				closeFanoutTab(client, tab)
			}
			return emitBrowserCallError(commandName, err)
		}
		params := map[string]any{
			"tab_id": tab, "expression": script, "await_promise": true, "return_by_value": true, "user_gesture": false, "repl_mode": false,
			// Every variant awaits its full response in the page, so the capture
			// is complete after the settle window; waiting for network idle would
			// stall on pages that keep long polls or streams open.
			"timeout_ms": timeout.Milliseconds(), "idle_ms": 300, "settle_ms": 250, "complete_after_settle": true, "max_body_bytes": shared.MaxBodyBytes, "max_result_bytes": 128 * 1024,
			"protocol_payloads": shared.ProtocolPayloads, "webrtc_media": shared.WebRTCMedia,
		}
		handoff.applyLimits(params)
		if err := requireBrowserCollectors(ctx, client, shared.ProtocolPayloads, shared.WebRTCMedia); err != nil {
			if created && !keepTab {
				closeFanoutTab(client, tab)
			}
			return emitBrowserCallError(commandName, err)
		}
		var result map[string]any
		record.dispatch()
		callErr := client.Call(ctx, "browser.action", params, &result)
		phase("fanout")
		if callErr == nil {
			callErr = finishBrowserCapture(ctx, client, result, handoff, "", false)
			if callErr != nil {
				callErr = fmt.Errorf("capture could not be sealed: %w", callErr)
			}
		}
		if created && !keepTab {
			closeFanoutTab(client, tab)
		}
		if callErr != nil {
			var rpcErr *bridge.RPCError
			if errors.As(callErr, &rpcErr) {
				return emitBrowserCallError(commandName, callErr)
			}
			return emitBrowserCaptureReadError(commandName, callErr, false)
		}
		record.capture(result)
		evaluation := fanoutEvaluationValue(result["evaluation"])
		var requests []store.Request
		if livePath, err := store.GetLiveFilePath(); err == nil {
			if export, err := loadLiveExport(livePath); err == nil {
				requests = export.Requests
			}
		}
		outcomes := fanoutOutcomes(variants, evaluation, requests)
		okCount, matched := 0, 0
		for _, outcome := range outcomes {
			if outcome.OK {
				okCount++
			}
			if outcome.RequestID != "" {
				matched++
			}
		}
		phase("seal")
		selected, _ := scope.Current()
		result["version"] = 1
		result["workspace"], result["task"] = selected.Workspace, selected.Task
		result["source_request_id"], result["origin"], result["matrix_key"] = source.ID, origin, matrixKey
		result["tab_id"], result["created_tab"], result["kept_tab"] = tab, created, created && keepTab
		result["concurrency"], result["variants"] = concurrency, outcomes
		result["fanout_summary"] = map[string]any{"total": len(outcomes), "ok": okCount, "failed": len(outcomes) - okCount, "matched_request_ids": matched}
		result["phases_ms"], result["duration_ms"] = phases, time.Since(started).Milliseconds()
		delete(result, "evaluation")
		summary := captureEvidenceSummary(result)
		summary["variants"], summary["ok"] = len(outcomes), okCount
		if err := record.finish(summary, "completed", "unverified", "", nil); err != nil {
			return emitBrowserCallError(commandName, err)
		}
		attachOperationEvidence(result, record)
		return emitBrowserResult(result, func() {
			fmt.Printf("origin: %s  tab: %d  variants: %d  ok: %d  matched: %d  saved: %v\n", origin, tab, len(outcomes), okCount, matched, result["saved_hash_id"])
			for _, outcome := range outcomes {
				fmt.Printf("  %d\t%s\t%d\t%dB\t%dms\t%s\t%s\n", outcome.Index, outcome.Label, outcome.Status, outcome.Bytes, outcome.ElapsedMS, outcome.RequestID, outcome.Error)
			}
		})
	}
	command.Flags().StringVar(&matrix, "matrix", "", "Matrix 'query.NAME=[v1,v2,...]', 'body.KEY=[...]' or 'header.NAME=[...]': one variant per value")
	command.Flags().StringArrayVar(&sets, "set", nil, "Constant mutation 'path=value' applied to every variant (repeatable)")
	command.Flags().IntVar(&concurrency, "concurrency", 8, "Parallel in-page requests (1 to 32)")
	command.Flags().IntVar(&tab, "tab", -1, "Existing tab on the request's origin; otherwise an owned tab is opened")
	command.Flags().BoolVar(&keepTab, "keep-tab", false, "Keep the owned origin tab warm for further fanouts")
	_ = command.MarkFlagRequired("matrix")
	return command
}

// fanoutEvaluationValue extracts the page script's return value from the action
// result. The bridge returns the raw Runtime.evaluate reply ({result:{value}}),
// a bare {value} in older shapes, or only a summary when the result was bounded.
func fanoutEvaluationValue(raw any) json.RawMessage {
	encoded, err := json.Marshal(raw)
	if err != nil || len(encoded) == 0 || string(encoded) == "null" {
		return nil
	}
	var remote struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Value    json.RawMessage `json:"value"`
		Variants json.RawMessage `json:"variants"`
	}
	if json.Unmarshal(encoded, &remote) != nil {
		return nil
	}
	switch {
	case len(remote.Result.Value) > 0 && string(remote.Result.Value) != "null":
		return remote.Result.Value
	case len(remote.Value) > 0 && string(remote.Value) != "null":
		return remote.Value
	case len(remote.Variants) > 0:
		return encoded
	}
	return nil
}

func closeFanoutTab(client *bridge.Client, tab int) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var ignored map[string]any
	_ = client.Call(ctx, "browser.close", map[string]any{"tab_id": tab}, &ignored)
}
