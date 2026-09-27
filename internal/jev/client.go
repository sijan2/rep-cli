package jev

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const RequestTimeout = 30 * time.Second
const maxResponseBytes = 1024 * 1024
const maxAttempts = 3

// retryBaseDelay doubles per attempt; Retry-After can only lengthen it.
var retryBaseDelay = 500 * time.Millisecond

// Documented jev-1.13 limits are 64k tokens per request and 32k tokens for state
// plus the longest question (https://docs.typesafe.ai/models). Budgets use a
// deliberately high token estimate (see EstimateTokens) instead of option counts;
// observed Rep candidate JSON used 2.3-3.1 bytes per token. Callers that still
// receive a provider overflow can retry with a smaller Budget.
const MaxQuestionContextTokens = 30000
const MaxRequestTokens = 60000

// MaxRequestBytes is an absolute encoded-size guard independent of tokenization.
const MaxRequestBytes = 128 * 1024
const MaxQuestions = 32
const MaxChoiceOptions = 255

// ReportedProbabilityStep is the precision of reported Choice probabilities:
// jev-1.13.0 returns hundredths. The API documents totals of approximately one.
const ReportedProbabilityStep = 0.01

var sharedTransport = newSharedTransport()

// The persistent host keeps provider connections between decisions. Agents
// often pause longer than Go's 90-second idle default, and the provider keeps
// idle HTTP/2 connections open beyond that, so closing them locally turned each
// longer pause into a new TCP/TLS handshake on the decision's critical path.
// HTTP/2 PINGs detect a half-dead connection before a decision is written to it.
func newSharedTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.IdleConnTimeout = 10 * time.Minute
	transport.HTTP2 = &http.HTTP2Config{SendPingTimeout: 30 * time.Second, PingTimeout: 10 * time.Second}
	return transport
}

type Question struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type ChoiceAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type Evaluation struct {
	Model   string                  `json:"model"`
	Answers map[string]ChoiceAnswer `json:"answers"`
	Usage   Usage                   `json:"usage"`
	Timing  []RequestTiming         `json:"timing,omitempty"`
}

// RequestTiming describes observed transport work, including retries. Zero
// DNS/connect/TLS durations on a reused connection do not imply a zero RTT.
type RequestTiming struct {
	Attempt     int     `json:"attempt"`
	Protocol    string  `json:"protocol,omitempty"`
	Reused      bool    `json:"reused"`
	DNSMS       float64 `json:"dns_ms"`
	ConnectMS   float64 `json:"connect_ms"`
	TLSMS       float64 `json:"tls_ms"`
	FirstByteMS float64 `json:"first_byte_ms"`
	DurationMS  float64 `json:"duration_ms"`
}

type Client struct {
	key        string
	model      string
	httpClient *http.Client
}

func NewClient(config Config) *Client {
	model := config.Model
	if model == "" {
		model = Model
	}
	return &Client{key: config.APIKey, model: model, httpClient: &http.Client{
		Transport:     sharedTransport,
		Timeout:       RequestTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Budget bounds the estimated tokens of one question context and one request.
// Values outside (0, default] use the default; a budget can only tighten.
type Budget struct {
	QuestionTokens int
	RequestTokens  int
}

func DefaultBudget() Budget { return Budget{MaxQuestionContextTokens, MaxRequestTokens} }

func (budget Budget) normalized() Budget {
	if budget.QuestionTokens <= 0 || budget.QuestionTokens > MaxQuestionContextTokens {
		budget.QuestionTokens = MaxQuestionContextTokens
	}
	if budget.RequestTokens <= 0 || budget.RequestTokens > MaxRequestTokens {
		budget.RequestTokens = MaxRequestTokens
	}
	return budget
}

// EstimateTokens overestimates provider tokens for budget checks: two ASCII
// bytes per token, and one token per non-ASCII byte (the byte-fallback bound).
func EstimateTokens(data []byte) int {
	ascii, other := 0, 0
	for _, value := range data {
		if value < 0x80 {
			ascii++
		} else {
			other++
		}
	}
	return (ascii+1)/2 + other
}

// ValidateRequest uses the same encoded budgets as Evaluate. Callers can pack
// independent questions without assuming that an option count fits.
func ValidateRequest(state any, questions map[string]Question) error {
	return ValidateRequestForModel(Model, state, questions)
}

func ValidateRequestForModel(model string, state any, questions map[string]Question) error {
	return ValidateRequestWithin(model, state, questions, DefaultBudget())
}

func ValidateRequestWithin(model string, state any, questions map[string]Question, budget Budget) error {
	if model == "" {
		model = Model
	}
	if !modelName.MatchString(model) {
		return &Error{Code: CodeInvalidRequest, Message: "invalid Jev model identifier"}
	}
	_, err := requestBody(model, state, questions, budget)
	return err
}

func requestBody(model string, state any, questions map[string]Question, budget Budget) ([]byte, error) {
	budget = budget.normalized()
	invalid := func(message string) error { return &Error{Code: CodeInvalidRequest, Message: message} }
	if len(questions) == 0 || len(questions) > MaxQuestions {
		return nil, invalid("Jev requires between 1 and 32 questions")
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return nil, invalid("invalid Jev state")
	}
	stateTokens := EstimateTokens(stateBytes)
	for _, question := range questions {
		if question.Type != "choice" || strings.TrimSpace(question.Instructions) == "" || len(question.Criteria) < 2 || len(question.Criteria) > MaxChoiceOptions {
			return nil, invalid("invalid Jev choice question")
		}
		questionBytes, err := json.Marshal(question)
		if err != nil {
			return nil, invalid("invalid Jev choice question")
		}
		if estimated := stateTokens + EstimateTokens(questionBytes); estimated > budget.QuestionTokens {
			return nil, invalid(fmt.Sprintf("Jev state plus a question needs about %d tokens, over the %d-token context budget", estimated, budget.QuestionTokens))
		}
	}
	body, err := json.Marshal(struct {
		Model     string              `json:"model"`
		State     any                 `json:"state"`
		Questions map[string]Question `json:"questions"`
	}{model, state, questions})
	if err != nil {
		return nil, invalid("invalid Jev request")
	}
	if estimated := EstimateTokens(body); len(body) > MaxRequestBytes || estimated > budget.RequestTokens {
		return nil, invalid(fmt.Sprintf("Jev request needs about %d tokens and %d bytes, over the %d-token or %d-byte request budget", estimated, len(body), budget.RequestTokens, MaxRequestBytes))
	}
	return body, nil
}

// Evaluate makes bounded, typed choice decisions. It does not execute decisions.
// Failures are *Error values whose messages never include response bodies.
func (c *Client) Evaluate(ctx context.Context, state any, questions map[string]Question) (Evaluation, error) {
	if c == nil || strings.TrimSpace(c.key) == "" {
		return Evaluation{}, &Error{Code: CodeNotConfigured, Message: "Jev key is not configured; set JEV or run rep jev config --env-file PATH"}
	}
	if !modelName.MatchString(c.model) {
		return Evaluation{}, &Error{Code: CodeInvalidRequest, Message: "invalid Jev model identifier"}
	}
	body, err := requestBody(c.model, state, questions, DefaultBudget())
	if err != nil {
		return Evaluation{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	var timings []RequestTiming
	var last *Error
	var delay time.Duration
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			if err := waitRetry(ctx, delay, last); err != nil {
				return Evaluation{}, err
			}
		}
		delay = time.Duration(1<<attempt) * retryBaseDelay
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, Endpoint, bytes.NewReader(body))
		if err != nil {
			return Evaluation{}, &Error{Code: CodeInvalidRequest, Message: "cannot construct Jev request"}
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+c.key)
		started := time.Now()
		var mu sync.Mutex
		timing := RequestTiming{Attempt: attempt + 1}
		var dns, connect, handshake time.Time
		elapsed := func(start time.Time) float64 {
			if start.IsZero() {
				return 0
			}
			return float64(time.Since(start)) / float64(time.Millisecond)
		}
		trace := &httptrace.ClientTrace{
			DNSStart: func(httptrace.DNSStartInfo) { mu.Lock(); dns = time.Now(); mu.Unlock() }, DNSDone: func(httptrace.DNSDoneInfo) { mu.Lock(); timing.DNSMS += elapsed(dns); mu.Unlock() },
			ConnectStart: func(string, string) { mu.Lock(); connect = time.Now(); mu.Unlock() }, ConnectDone: func(string, string, error) { mu.Lock(); timing.ConnectMS += elapsed(connect); mu.Unlock() },
			TLSHandshakeStart: func() { mu.Lock(); handshake = time.Now(); mu.Unlock() }, TLSHandshakeDone: func(tls.ConnectionState, error) { mu.Lock(); timing.TLSMS += elapsed(handshake); mu.Unlock() },
			GotConn: func(info httptrace.GotConnInfo) { mu.Lock(); timing.Reused = info.Reused; mu.Unlock() }, GotFirstResponseByte: func() { mu.Lock(); timing.FirstByteMS = elapsed(started); mu.Unlock() },
		}
		request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
		response, err := c.httpClient.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return Evaluation{}, contextError(ctx)
			}
			// A decision request has no side effects, so transport loss is retried.
			last = &Error{Code: CodeConnection, Message: "Jev connection failed: " + transportSummary(err), Retryable: true}
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
		response.Body.Close()
		mu.Lock()
		timing.Protocol = response.Proto
		timing.DurationMS = elapsed(started)
		timings = append(timings, timing)
		mu.Unlock()
		if readErr != nil {
			if ctx.Err() != nil {
				return Evaluation{}, contextError(ctx)
			}
			last = &Error{Code: CodeConnection, Message: "Jev connection failed while reading the response", Retryable: true}
			continue
		}
		if len(data) > maxResponseBytes {
			return Evaluation{}, &Error{Code: CodeInvalidResponse, Status: response.StatusCode, Message: "Jev response exceeds 1 MiB"}
		}
		if response.StatusCode == http.StatusOK {
			result, err := decodeEvaluation(data, questions)
			result.Timing = timings
			return result, err
		}
		last = statusError(response.StatusCode, providerErrorType(data))
		if !last.Retryable {
			return Evaluation{}, last
		}
		if attempt == maxAttempts-1 {
			break
		}
		retry := response.Header.Get("Retry-After")
		if seconds, err := strconv.ParseInt(retry, 10, 64); err == nil && seconds >= 0 {
			if seconds > int64(RequestTimeout/time.Second) {
				return Evaluation{}, retryLater(last)
			}
			delay = max(delay, time.Duration(seconds)*time.Second)
		} else if when, err := http.ParseTime(retry); err == nil {
			delay = max(delay, time.Until(when))
		}
	}
	failure := *last
	failure.Message += fmt.Sprintf(" after %d attempts; retry after a short wait", maxAttempts)
	return Evaluation{}, &failure
}

func statusError(status int, providerType string) *Error {
	failure := &Error{Status: status, ProviderType: providerType}
	label := fmt.Sprintf("Jev API returned HTTP %d", status)
	if providerType != "" {
		label += " (" + providerType + ")"
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden || providerType == "authentication_error":
		failure.Code, failure.Message = CodeUnauthorized, label+": the configured key was rejected; check it with 'rep jev status' and 'rep jev doctor'"
	case providerType == "max_tokens_exceeded":
		failure.Code, failure.Message = CodeContextExceeded, label+": the request exceeds the model context limit"
	case status == http.StatusTooManyRequests:
		failure.Code, failure.Message, failure.Retryable = CodeRateLimited, label+": rate limited", true
	case status == 529 || status == http.StatusServiceUnavailable:
		failure.Code, failure.Message, failure.Retryable = CodeOverloaded, label+": TypeSafe is temporarily overloaded", true
	case status >= 500:
		failure.Code, failure.Message, failure.Retryable = CodeServerError, label+": provider server error", true
	case status >= 300 && status < 400:
		failure.Code, failure.Message = CodeRejected, label+": redirects are never followed"
	default:
		failure.Code, failure.Message = CodeRejected, label+": the provider rejected the request"
	}
	return failure
}

// retryLater refuses a delay that cannot finish before the request deadline,
// instead of sleeping into a certain timeout.
func retryLater(last *Error) *Error {
	failure := *last
	failure.Message += "; retry delay exceeds request deadline; retry later"
	return &failure
}

func waitRetry(ctx context.Context, delay time.Duration, last *Error) error {
	if deadline, ok := ctx.Deadline(); ok && delay >= time.Until(deadline) {
		return retryLater(last)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return contextError(ctx)
	case <-timer.C:
		return nil
	}
}

func contextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout, Retryable: true, Message: "Jev request timed out before the provider answered"}
	}
	return &Error{Code: CodeTimeout, Message: "Jev request was canceled"}
}

// transportSummary keeps the network cause (DNS, refused, reset, TLS) and drops
// the request wrapper. Transport errors never contain headers or bodies.
func transportSummary(err error) string {
	var wrapped *url.Error
	if errors.As(err, &wrapped) && wrapped.Err != nil {
		err = wrapped.Err
	}
	summary := strings.Join(strings.Fields(err.Error()), " ")
	if len(summary) > 200 {
		summary = summary[:200]
	}
	return summary
}

func decodeEvaluation(data []byte, questions map[string]Question) (Evaluation, error) {
	invalid := func(format string, args ...any) error {
		return &Error{Code: CodeInvalidResponse, Status: http.StatusOK, Message: "Jev returned an invalid typed decision: " + fmt.Sprintf(format, args...)}
	}
	var wire struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Type          string              `json:"type"`
			Choice        string              `json:"choice"`
			Probabilities map[string]*float64 `json:"probabilities"`
			Confidence    *float64            `json:"confidence"`
		} `json:"answers"`
		Usage *struct {
			InputTokens  *int `json:"input_tokens"`
			OutputTokens *int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &wire) != nil {
		return Evaluation{}, invalid("response is not the documented JSON shape")
	}
	if !modelName.MatchString(wire.Model) {
		return Evaluation{}, invalid("missing or unsafe model identifier")
	}
	if wire.Usage == nil || wire.Usage.InputTokens == nil || wire.Usage.OutputTokens == nil || *wire.Usage.InputTokens < 0 || *wire.Usage.OutputTokens < 0 {
		return Evaluation{}, invalid("missing or negative token usage")
	}
	if len(wire.Answers) != len(questions) {
		return Evaluation{}, invalid("%d answers for %d questions", len(wire.Answers), len(questions))
	}
	result := Evaluation{Model: wire.Model, Answers: make(map[string]ChoiceAnswer), Usage: Usage{InputTokens: *wire.Usage.InputTokens, OutputTokens: *wire.Usage.OutputTokens}}
	for name, question := range questions {
		answer, exists := wire.Answers[name]
		switch {
		case !exists:
			return Evaluation{}, invalid("question %q was not answered", name)
		case answer.Type != "choice":
			return Evaluation{}, invalid("question %q is not a choice answer", name)
		case answer.Confidence == nil || !isProbability(*answer.Confidence):
			return Evaluation{}, invalid("question %q has no confidence between 0 and 1", name)
		case len(answer.Probabilities) != len(question.Criteria):
			return Evaluation{}, invalid("question %q has %d probabilities for %d offered options", name, len(answer.Probabilities), len(question.Criteria))
		}
		if _, exists := question.Criteria[answer.Choice]; !exists {
			return Evaluation{}, invalid("question %q chose an option that was not offered", name)
		}
		probabilities := make(map[string]float64, len(question.Criteria))
		for option := range question.Criteria {
			probability, exists := answer.Probabilities[option]
			if !exists || probability == nil || !isProbability(*probability) {
				return Evaluation{}, invalid("question %q lacks a probability between 0 and 1 for an offered option", name)
			}
			probabilities[option] = *probability
		}
		if problem := DistributionProblem(probabilities, answer.Choice); problem != "" {
			return Evaluation{}, invalid("question %q %s", name, problem)
		}
		result.Answers[name] = ChoiceAnswer{Type: answer.Type, Choice: answer.Choice, Probabilities: probabilities, Confidence: *answer.Confidence}
	}
	return result, nil
}

func isProbability(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

// DistributionBounds returns the totals a correct distribution can report after
// each probability is rounded to ReportedProbabilityStep. Rounding moves a value
// by at most half a step: every option can lose that much, and only nonzero
// values can gain it. A fixed tolerance breaks as probability spreads across
// candidates; this bound follows the provider's reporting precision instead.
// Totals within 0.01 of one remain accepted for very small option sets.
// https://docs.typesafe.ai/sdk/python/api/types/responses#probabilities
func DistributionBounds(options, nonzero int) (low, high float64) {
	half := ReportedProbabilityStep / 2
	low = min(1-half*float64(options), 0.99)
	high = max(1+half*float64(nonzero), 1.01)
	return low - 1e-9, high + 1e-9
}

// DistributionProblem describes why a complete reported Choice distribution is
// inconsistent, or returns "". Reported values and confidence are never changed.
func DistributionProblem(probabilities map[string]float64, choice string) string {
	total, highest, nonzero := 0.0, 0.0, 0
	for _, value := range probabilities {
		if !isProbability(value) {
			return "has a probability outside 0..1"
		}
		total += value
		highest = max(highest, value)
		if value > 0 {
			nonzero++
		}
	}
	if highest <= 0 {
		return "assigns no probability to any option"
	}
	if low, high := DistributionBounds(len(probabilities), nonzero); total < low || total > high {
		return fmt.Sprintf("probabilities total %.4f, outside the %.4f to %.4f range that rounding to hundredths explains for %d options (%d nonzero)", total, low, high, len(probabilities), nonzero)
	}
	chosen, exists := probabilities[choice]
	if !exists {
		return "chose an option that was not offered"
	}
	if chosen+0.000001 < highest {
		return fmt.Sprintf("chose an option with probability %.2f while another has %.2f", chosen, highest)
	}
	return ""
}
