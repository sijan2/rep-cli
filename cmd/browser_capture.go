package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
)

// browserCapturedRequest is the stable, secret-free handle returned by every
// browser command that seals a capture. URLs, headers, and bodies deliberately
// stay in live.json; callers can retrieve one body by ID when they need it.
type browserCapturedRequest struct {
	Sequence                int    `json:"sequence"`
	ID                      string `json:"id"`
	Method                  string `json:"method"`
	Status                  int    `json:"status"`
	BodyBytes               int    `json:"body_bytes"`
	BodyTruncated           bool   `json:"body_truncated,omitempty"`
	IntentionalCancellation string `json:"intentional_cancellation,omitempty"`
}

// browserTerminalOutcome describes an observed redirect/download graph using
// only stable request handles and transport metadata. The URLs, headers,
// request bodies, response bodies, and redirect locations remain sealed in
// live.json.
type browserTerminalOutcome struct {
	Kind                     string   `json:"kind"`
	Completed                bool     `json:"completed"`
	TerminalResponseReceived bool     `json:"terminal_response_received"`
	DownloadHandoffStarted   bool     `json:"download_handoff_started,omitempty"`
	SourceRequestID          string   `json:"source_request_id"`
	TerminalRequestID        string   `json:"terminal_request_id"`
	TerminalStatus           int      `json:"terminal_status"`
	RedirectHops             int      `json:"redirect_hops"`
	RequestIDs               []string `json:"request_ids"`
	LaterFormFailures        int      `json:"later_form_failures,omitempty"`
	LaterFormFailureIDs      []string `json:"later_form_failure_ids,omitempty"`
}

type browserActionEnvelope struct {
	Type     string `json:"type"`
	Status   int    `json:"status"`
	Location string `json:"location"`
}

type browserCaptureSnapshot struct {
	SessionID string          `json:"session_id,omitempty"`
	Requests  []store.Request `json:"requests"`
}

func enrichBrowserCaptureResult(result map[string]interface{}) error {
	livePath, err := store.GetLiveFilePath()
	if err != nil {
		return fmt.Errorf("resolve browser capture: %w", err)
	}
	data, err := os.ReadFile(livePath)
	if err != nil {
		return fmt.Errorf("read browser capture: %w", err)
	}
	var snapshot browserCaptureSnapshot
	if err := sonic.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("decode browser capture: %w", err)
	}

	resultSession, _ := result["session_id"].(string)
	if resultSession != "" && snapshot.SessionID != "" && resultSession != snapshot.SessionID {
		return fmt.Errorf("browser capture session mismatch")
	}
	if expected, ok := browserResultRequestCount(result["requests"]); ok && expected != len(snapshot.Requests) {
		return fmt.Errorf("browser capture request count mismatch: result=%d live=%d", expected, len(snapshot.Requests))
	}

	descriptors := browserCapturedRequestDescriptors(snapshot.Requests)
	if len(descriptors) != len(snapshot.Requests) {
		return fmt.Errorf("browser capture contains a request without a stable id")
	}
	result["captured_requests"] = descriptors
	if outcome := browserTerminalRedirectOutcome(snapshot.Requests); outcome != nil {
		result["terminal_outcome"] = outcome
	}
	reconcileBrowserFailureCount(result, snapshot.Requests)
	return nil
}

func browserCapturedRequestDescriptors(requests []store.Request) []browserCapturedRequest {
	descriptors := make([]browserCapturedRequest, 0, len(requests))
	for i := range requests {
		request := &requests[i]
		if strings.TrimSpace(request.ID) == "" {
			continue
		}
		descriptor := browserCapturedRequest{
			Sequence:                i + 1,
			ID:                      request.ID,
			Method:                  strings.ToUpper(strings.TrimSpace(request.Method)),
			BodyTruncated:           request.ResponseBodyTruncated,
			IntentionalCancellation: browserIntentionalCancellationKind(request.IntentionalCancellation),
		}
		if request.Response != nil {
			descriptor.Status = request.Response.Status
			descriptor.BodyBytes = len(request.Response.Body)
		}
		descriptors = append(descriptors, descriptor)
	}
	return descriptors
}

func browserTerminalRedirectOutcome(requests []store.Request) *browserTerminalOutcome {
	for sourceIndex := range requests {
		source := &requests[sourceIndex]
		envelope, ok := decodeBrowserActionEnvelope(source)
		if !ok || envelope.Type != "redirect" || envelope.Status < 300 || envelope.Status >= 400 || strings.TrimSpace(envelope.Location) == "" {
			continue
		}
		if source.CompletionOrdinal <= 0 {
			continue
		}

		targetURL, ok := resolveBrowserRedirectURL(source.URL, envelope.Location)
		if !ok {
			continue
		}
		firstHop := findBrowserRequestByURL(requests, sourceIndex+1, targetURL, source.CompletionOrdinal, "", "")
		if firstHop < 0 {
			continue
		}

		chainIndexes := []int{sourceIndex}
		redirectHops := 1
		current := firstHop
		for current >= 0 && current < len(requests) {
			request := &requests[current]
			chainIndexes = append(chainIndexes, current)
			status := browserRequestStatus(request)
			if status >= 200 && status < 300 {
				requestIDs := make([]string, 0, len(chainIndexes))
				for _, index := range chainIndexes {
					requestIDs = append(requestIDs, requests[index].ID)
				}
				failureIDs := browserLaterFormFailureIDs(requests, sourceIndex, source)
				downloadHandoff := browserIntentionalCancellationKind(request.IntentionalCancellation) == "browser-download"
				completed := request.CompletionOrdinal > 0 && !request.Canceled && strings.TrimSpace(request.ErrorText) == ""
				kind := "redirect_chain"
				if downloadHandoff {
					kind = "download_handoff"
				}
				return &browserTerminalOutcome{
					Kind:                     kind,
					Completed:                completed && !downloadHandoff,
					TerminalResponseReceived: true,
					DownloadHandoffStarted:   downloadHandoff,
					SourceRequestID:          source.ID,
					TerminalRequestID:        request.ID,
					TerminalStatus:           status,
					RedirectHops:             redirectHops,
					RequestIDs:               requestIDs,
					LaterFormFailures:        len(failureIDs),
					LaterFormFailureIDs:      failureIDs,
				}
			}
			if status < 300 || status >= 400 {
				break
			}

			location := store.HeaderFirst(request.Response.Headers, "location")
			nextURL, resolved := resolveBrowserRedirectURL(request.URL, location)
			if !resolved {
				break
			}
			if request.CompletionOrdinal <= 0 {
				break
			}
			lineage := browserOriginalRequestRoot(request.OriginalID)
			if lineage == "" {
				break
			}
			nextMethod := browserRedirectSuccessorMethod(request.Method, status)
			next := findBrowserRequestByURL(requests, current+1, nextURL, request.CompletionOrdinal, lineage, nextMethod)
			if next < 0 {
				break
			}
			redirectHops++
			current = next
		}
	}
	return nil
}

func decodeBrowserActionEnvelope(request *store.Request) (browserActionEnvelope, bool) {
	if request == nil || request.Response == nil || request.ResponseEncoding == "base64" || strings.TrimSpace(request.Response.Body) == "" {
		return browserActionEnvelope{}, false
	}
	body := strings.TrimSpace(request.Response.Body)
	if len(body) > 64*1024 || !strings.HasPrefix(body, "{") {
		return browserActionEnvelope{}, false
	}
	var envelope browserActionEnvelope
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		return browserActionEnvelope{}, false
	}
	envelope.Type = strings.ToLower(strings.TrimSpace(envelope.Type))
	return envelope, envelope.Type != ""
}

func resolveBrowserRedirectURL(baseURL, location string) (string, bool) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", false
	}
	reference, err := url.Parse(strings.TrimSpace(location))
	if err != nil || strings.TrimSpace(location) == "" {
		return "", false
	}
	resolved := base.ResolveReference(reference)
	resolved.Fragment = ""
	return resolved.String(), true
}

func findBrowserRequestByURL(requests []store.Request, start int, target string, afterOrdinal int64, lineage, expectedMethod string) int {
	for index := max(start, 0); index < len(requests); index++ {
		if requests[index].StartOrdinal <= afterOrdinal {
			continue
		}
		if lineage != "" && browserOriginalRequestRoot(requests[index].OriginalID) != lineage {
			continue
		}
		method := strings.ToUpper(strings.TrimSpace(requests[index].Method))
		if expectedMethod != "" && method != expectedMethod {
			continue
		}
		candidate, err := url.Parse(strings.TrimSpace(requests[index].URL))
		if err == nil {
			candidate.Fragment = ""
		}
		if err == nil && candidate.String() == target {
			return index
		}
	}
	return -1
}

func browserRedirectSuccessorMethod(method string, status int) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	switch status {
	case 303:
		if method == "HEAD" {
			return "HEAD"
		}
		return "GET"
	case 301, 302:
		if method == "POST" {
			return "GET"
		}
	}
	return method
}

func browserOriginalRequestRoot(originalID string) string {
	value := strings.TrimSpace(originalID)
	if value == "" {
		return ""
	}
	marker := strings.LastIndex(value, ":redirect-")
	if marker <= 0 {
		return value
	}
	if _, err := strconv.Atoi(value[marker+len(":redirect-"):]); err != nil {
		return value
	}
	return value[:marker]
}

func browserLaterFormFailureIDs(requests []store.Request, sourceIndex int, source *store.Request) []string {
	ids := []string{}
	method := strings.ToUpper(strings.TrimSpace(source.Method))
	for index := sourceIndex + 1; index < len(requests); index++ {
		request := &requests[index]
		if request.StartOrdinal <= source.CompletionOrdinal {
			continue
		}
		if strings.ToUpper(strings.TrimSpace(request.Method)) != method || request.URL != source.URL {
			continue
		}
		envelope, ok := decodeBrowserActionEnvelope(request)
		if ok && envelope.Type == "failure" && strings.TrimSpace(request.ID) != "" {
			ids = append(ids, request.ID)
		}
	}
	return ids
}

func browserRequestStatus(request *store.Request) int {
	if request == nil || request.Response == nil {
		return 0
	}
	return request.Response.Status
}

func reconcileBrowserFailureCount(result map[string]interface{}, requests []store.Request) {
	observed, ok := browserResultRequestCount(result["failed_requests"])
	if !ok {
		return
	}
	reportedIgnored, hasReportedIgnored := browserResultRequestCount(result["ignored_cancellations"])
	if !hasReportedIgnored {
		reportedIgnored = 0
	}
	headersOnly := browserResultResponseBool(result, "body_omitted")
	requestURL := browserResultRequestURL(result)
	ignored := 0
	legacyFallbackUsed := false
	for index := range requests {
		request := &requests[index]
		if browserIntentionalCancellationKind(request.IntentionalCancellation) != "" {
			ignored++
			continue
		}
		if !legacyFallbackUsed && browserRequestMatchesLegacyHeadersOnlyCancellation(request, headersOnly, requestURL, len(requests)) {
			ignored++
			legacyFallbackUsed = true
		}
	}
	additionalIgnored := ignored - reportedIgnored
	if additionalIgnored <= 0 {
		return
	}
	corrected := observed - additionalIgnored
	if corrected < 0 {
		corrected = 0
	}
	result["failed_requests"] = corrected
	result["ignored_cancellations"] = ignored
}

func browserRequestMatchesLegacyHeadersOnlyCancellation(request *store.Request, headersOnly bool, requestURL string, requestCount int) bool {
	if request == nil || request.Response == nil || request.Response.Status < 200 || request.Response.Status >= 300 {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(request.ErrorText), "net::ERR_ABORTED") {
		return false
	}
	return headersOnly && (requestCount == 1 || (requestURL != "" && request.URL == requestURL))
}

func browserIntentionalCancellationKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "headers-only":
		return "headers-only"
	case "browser-download":
		return "browser-download"
	default:
		return ""
	}
}

func browserResultResponseBool(result map[string]interface{}, key string) bool {
	response, _ := result["response"].(map[string]interface{})
	value, _ := response[key].(bool)
	return value
}

func browserResultRequestURL(result map[string]interface{}) string {
	request, _ := result["request"].(map[string]interface{})
	value, _ := request["url"].(string)
	return value
}

func browserResultRequestCount(value interface{}) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, number >= 0
	case int32:
		return int(number), number >= 0
	case int64:
		return int(number), number >= 0
	case float64:
		integer := int(number)
		return integer, number >= 0 && float64(integer) == number
	case json.Number:
		integer, err := number.Int64()
		return int(integer), err == nil && integer >= 0
	default:
		return 0, false
	}
}

func emitBrowserCaptureReadError(command string, err error, redact bool) error {
	if redact {
		return output.EmitAgentError(os.Stdout, output.NewAgentError(
			output.ErrCodeStoreRead,
			command,
			"sealed browser capture could not be read; sensitive details were omitted",
			"rep browser status",
			"rep summary",
		), getOutputMode() == "json")
	}
	return output.EmitAgentError(os.Stdout, output.WrapError(
		err,
		output.ErrCodeStoreRead,
		command,
		"rep browser status",
		"rep summary",
	), getOutputMode() == "json")
}

func printBrowserCapturedRequests(result map[string]interface{}) {
	values, ok := result["captured_requests"].([]browserCapturedRequest)
	if !ok || len(values) == 0 {
		fmt.Println("captured_requests: none")
		return
	}
	fmt.Println("captured_requests:")
	for _, request := range values {
		fmt.Printf("  %s\t%s\t%d\t%dB\n", request.ID, request.Method, request.Status, request.BodyBytes)
	}
}
