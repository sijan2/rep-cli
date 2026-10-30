package commands

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

//go:embed agent_transport.js
var coreTransportJS string

var coreHTTPToken = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

func coreRequestURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", coreArgument("use an absolute HTTP(S) URL without credentials")
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return parsed.Scheme + "://" + host, nil
}
func coreSafeMethod(method string) bool { return method == "GET" || method == "HEAD" }
func coreMethod(raw string) (string, error) {
	method := strings.ToUpper(strings.TrimSpace(raw))
	if !coreHTTPToken.MatchString(method) || method == "CONNECT" || method == "TRACE" || method == "TRACK" {
		return "", coreArgument("unsupported HTTP method")
	}
	return method, nil
}
func coreTransportScript(variants []fanoutVariant, timeout time.Duration, write bool, unsafeSource ...bool) (string, error) {
	if len(variants) < 1 || len(variants) > maxFanoutVariants {
		return "", coreArgument("provide between 1 and 256 variants")
	}
	safe := len(unsafeSource) == 0 || !unsafeSource[0]
	for _, variant := range variants {
		if !coreSafeMethod(variant.Method) {
			safe = false
		}
	}
	if !safe && !write {
		return "", agentstate.Failure("write_required", "unsafe replay requires --write", coreNext("replay", "--help"))
	}
	job, err := json.Marshal(map[string]any{"variants": variants, "safe": safe, "timeout_ms": timeout.Milliseconds()})
	if err != nil {
		return "", err
	}
	return strings.Replace(coreTransportJS, "__REP_TRANSPORT_JOB__", string(job), 1), nil
}

func newCoreFetchCommand() *cobra.Command {
	var method, data string
	var headers []string
	command := &cobra.Command{Use: "fetch URL", Short: "Fetch in a private origin tab using browser credentials", Args: cobra.ExactArgs(1)}
	command.Flags().StringVarP(&method, "X", "X", "", "HTTP method (GET; POST with -d)")
	command.Flags().StringArrayVarP(&headers, "H", "H", nil, "Request header, repeatable")
	command.Flags().StringVarP(&data, "d", "d", "", "Request body or @FILE")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := coreFetch(cmd, args[0], method, headers, data, cmd.Flags().Changed("d")); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}
func coreFetch(command *cobra.Command, rawURL, method string, headers []string, data string, hasData bool) error {
	origin, err := coreRequestURL(rawURL)
	if err != nil {
		return err
	}
	if method == "" {
		method = "GET"
		if hasData {
			method = "POST"
		}
	}
	method, err = coreMethod(method)
	if err != nil {
		return err
	}
	headerMap := store.HeaderMap{}
	for _, header := range headers {
		name, value, ok := strings.Cut(header, ":")
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if !ok || !coreHTTPToken.MatchString(name) || strings.ContainsAny(value, "\r\n\x00") {
			return coreArgument("headers must be Name: value without control characters")
		}
		headerMap[strings.ToLower(name)] = append(headerMap[strings.ToLower(name)], value)
	}
	variant := fanoutVariant{Index: 0, Method: method, URL: rawURL, Headers: fanoutForwardHeaders(headerMap)}
	if hasData {
		if coreSafeMethod(method) {
			return coreArgument("GET and HEAD cannot carry -d")
		}
		body, e := coreSource(command, data)
		if e != nil {
			return e
		}
		variant.Body = &body
	}
	return coreRunTransport(command, []fanoutVariant{variant}, origin, nil, nil, 30*time.Second, true, "fetch")
}

type coreTransport struct {
	client  *bridge.Client
	index   *agentstate.Store
	tab     *agentstate.Tab
	lease   string
	session *browserfast.Session
}

func (t *coreTransport) close() {
	if t.session != nil {
		_ = t.session.CloseRead()
	}
	if t.tab != nil {
		_ = t.index.Release(t.tab.Handle, t.lease)
	}
}
func openCoreTransport(ctx context.Context, index *agentstate.Store, browser agentstate.Browser, client *bridge.Client, origin string, duration time.Duration) (_ *coreTransport, err error) {
	selected, _ := scope.Current()
	tab, lease, err := index.ReserveTransport(browser, selected.Identity(), selected.SessionKey, origin, duration+20*time.Second)
	if err != nil {
		return nil, err
	}
	transport := &coreTransport{client: client, index: index, tab: tab, lease: lease}
	defer func() {
		if err != nil {
			transport.close()
		}
	}()
	if tab.ChromeID < 0 {
		var created struct {
			TabID *int `json:"tab_id"`
		}
		if err = client.Call(ctx, "browser.create", map[string]any{"url": "about:blank", "active": false}, &created); err != nil {
			return nil, err
		}
		if created.TabID == nil || *created.TabID < 0 {
			return nil, fmt.Errorf("browser returned no background tab")
		}
		tab.ChromeID = *created.TabID
		if err = index.Change(tab.Handle, lease, func(t *agentstate.Tab) error { t.ChromeID = tab.ChromeID; return nil }); err != nil {
			return nil, err
		}
	}
	transport.session = &browserfast.Session{Browser: client, Tab: tab.ChromeID, Owner: tab.Owner}
	if err = transport.session.Acquire(ctx); err != nil {
		return nil, err
	}
	// Navigate only this origin's dedicated tab. A redirect or later page-script
	// navigation is checked before every request, never silently used as context.
	var observed struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err = transport.session.Inspect(ctx, "", "Runtime.evaluate", map[string]any{"expression": "location.origin", "returnByValue": true}, &observed); err != nil {
		return nil, err
	}
	if observed.Result.Value != origin {
		var ignored any
		if err = transport.session.Inspect(ctx, "", "Page.navigate", map[string]any{"url": origin + "/"}, &ignored); err != nil {
			return nil, err
		}
		for {
			if err = transport.session.Inspect(ctx, "", "Runtime.evaluate", map[string]any{"expression": "document.readyState !== 'loading' ? location.origin : ''", "returnByValue": true}, &observed); err != nil {
				return nil, err
			}
			if observed.Result.Value == origin {
				break
			}
			select {
			case <-ctx.Done():
				return nil, agentstate.Failure("origin_unavailable", "background page did not reach the request origin", coreNext("tabs"))
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	tab.URL = origin + "/"
	if err = index.Change(tab.Handle, lease, func(t *agentstate.Tab) error { t.URL = tab.URL; return nil }); err != nil {
		return nil, err
	}
	return transport, nil
}

func coreRunTransport(command *cobra.Command, variants []fanoutVariant, origin string, source *store.Request, capture *agentstate.Capture, timeout time.Duration, write bool, operation string) error {
	if timeout < time.Second || timeout > 5*time.Minute {
		return coreArgument("timeout must be between 1s and 5m")
	}
	script, err := coreTransportScript(variants, timeout, write, source != nil && !coreSafeMethod(source.Method))
	if err != nil {
		return err
	}
	index, err := coreStore()
	if err != nil {
		return err
	}
	ctx, cancel := coreTimeout(command, timeout+15*time.Second)
	defer cancel()
	var addressed *agentstate.Tab
	if capture != nil {
		addressed = &agentstate.Tab{Browser: capture.Browser}
	}
	client, browser, err := resolveCoreBrowser(ctx, index, addressed)
	if err != nil {
		return err
	}
	transport, err := openCoreTransport(ctx, index, browser, client, origin, timeout)
	if err != nil {
		return err
	}
	defer transport.close()
	recording, err := beginCoreCapture(ctx, command, client, transport.session, transport.tab, origin+"/")
	if err != nil {
		return err
	}
	defer recording.finish()
	if err = index.Journal(transport.tab.Owner, transport.tab.Handle, operation, "requested"); err != nil {
		return err
	}
	outcome := "outcome_unknown"
	defer func() { _ = index.Journal(transport.tab.Owner, transport.tab.Handle, operation, outcome) }()
	result := coreTabResult(transport.tab)
	result.Meta = map[string]any{"operation": operation, "transport": "in-page", "fidelity": "browser-session"}
	var evaluated struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	if err = transport.session.Inspect(ctx, "", "Runtime.evaluate", map[string]any{"expression": script, "awaitPromise": true, "returnByValue": true, "timeout": timeout.Milliseconds()}, &evaluated); err != nil || len(evaluated.Exception) > 0 || len(evaluated.Result.Value) == 0 {
		result.Status = "performed"
		result.Complete = boolPointer(false)
		result.Meta.(map[string]any)["observation_error"] = "outcome_unknown; requests were not retried"
		result.Next = coreNext("scope")
		return emitCore(command, result)
	}
	recording.finish()
	if recording.err != nil {
		result.Status = "performed"
		result.Complete = boolPointer(false)
		return emitCore(command, result)
	}
	return emitCoreTransport(command, result, recording, variants, evaluated.Result.Value, source, &outcome)
}

func emitCoreTransport(command *cobra.Command, result coreResult, recording *coreRecording, variants []fanoutVariant, evaluation json.RawMessage, source *store.Request, outcome *string) error {
	var execution struct {
		Attempted int  `json:"attempted"`
		Retries   int  `json:"retries"`
		Stopped   bool `json:"stopped"`
	}
	if err := json.Unmarshal(evaluation, &execution); err != nil {
		return err
	}
	// Retry duplicates are paired with the final observation for each URL.
	requests := recording.snapshot.Requests
	outcomes := fanoutOutcomes(variants, evaluation, requests)
	rows := []map[string]any{}
	allComplete := true
	succeeded := 0
	var singleBody any
	for _, item := range outcomes {
		row := map[string]any{"index": item.Index, "status": item.Status, "bytes": item.Bytes, "ok": item.OK}
		var req *store.Request
		for i := len(requests) - 1; i >= 0; i-- {
			r := &requests[i]
			if r.Method == item.Method && fanoutNormalizeURL(r.URL) == fanoutNormalizeURL(item.URL) && r.Response != nil && r.Response.Status == item.Status {
				req = r
				break
			}
		}
		// Identical-URL body variants must use the ordinal match, not the last URL.
		if execution.Retries == 0 {
			for i := range requests {
				if requests[i].ID == item.RequestID {
					req = &requests[i]
					break
				}
			}
		}
		if req != nil {
			row["request_id"] = req.ID
			bytes, contentType, evidence, e := bodyEvidence(req, false)
			complete := e == nil && (evidence.State == "complete" || evidence.State == "not_applicable")
			row["complete"] = complete
			row["body_state"] = evidence.State
			if !complete {
				allComplete = false
			}
			if source != nil {
				original, originalEvidence, originalErr := originalReplayEvidence(source)
				state := "inconclusive"
				if complete && originalErr == nil && (originalEvidence.State == "complete" || originalEvidence.State == "not_applicable") {
					state = "different"
					if string(original) == string(bytes) {
						state = "equal"
					}
				}
				row["body_comparison"] = state
			}
			safe := []byte(output.RedactBody(string(bytes), contentType))
			if len(variants) > 1 || len(safe) > 8000 {
				artifact, e := coreArtifact("", ".body", safe)
				if e != nil {
					return e
				}
				row["artifact"] = artifact
			} else {
				singleBody = map[string]any{"body": string(safe), "headers": output.RedactHeaders(req.Response.Headers), "content_type": contentType}
			}
			if len(variants) == 1 {
				result.Next = coreNext("body", req.ID)
				result.Meta.(map[string]any)["request_id"] = req.ID
			}
		} else {
			allComplete = false
			row["complete"] = false
		}
		if item.Error != "" {
			row["error"] = item.Error
			allComplete = false
		}
		if item.OK {
			succeeded++
		} else {
			allComplete = false
		}
		rows = append(rows, row)
	}
	result.Status = "complete"
	*outcome = "complete"
	if !allComplete {
		result.Status = "partial"
		*outcome = "partial"
	}
	result.Complete = boolPointer(allComplete)
	meta := result.Meta.(map[string]any)
	meta["summary"] = map[string]any{"total": len(variants), "succeeded": succeeded, "attempted": execution.Attempted, "retries": execution.Retries, "stopped": execution.Stopped}
	encoded, _ := json.Marshal(rows)
	if len(encoded) > 8000 {
		artifact, e := coreArtifact("", ".json", encoded)
		if e != nil {
			return e
		}
		meta["variants_artifact"] = artifact
		meta["variants"] = rows[:min(8, len(rows))]
	} else {
		meta["variants"] = rows
	}
	result.Untrusted = singleBody
	return emitCore(command, result)
}
func init() { rootCmd.AddCommand(newCoreFetchCommand()) }
