package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTransportTracingMeasuresActualConnectionReuse(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, validResponse)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := server.Client().Transport
	first, second := NewClient(Config{APIKey: "local-test-key"}), NewClient(Config{APIKey: "local-test-key"})
	if first.httpClient.Transport != second.httpClient.Transport {
		t.Fatal("new clients do not share persistent transport")
	}
	local := transportFunc(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		target := *r.URL
		target.Scheme = endpoint.Scheme
		target.Host = endpoint.Host
		copy.URL = &target
		copy.Host = endpoint.Host
		return transport.RoundTrip(copy)
	})
	first.httpClient.Transport = local
	second.httpClient.Transport = local
	a, err := first.Evaluate(context.Background(), map[string]string{"text": "local"}, choiceQuestions())
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Evaluate(context.Background(), map[string]string{"text": "local"}, choiceQuestions())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Timing) != 1 || len(b.Timing) != 1 || a.Timing[0].Reused || !b.Timing[0].Reused || a.Timing[0].Protocol != "HTTP/2.0" || a.Timing[0].TLSMS <= 0 || b.Timing[0].DurationMS <= 0 {
		t.Fatalf("incorrect traces: first=%+v second=%+v", a.Timing, b.Timing)
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func choiceQuestions() map[string]Question {
	return map[string]Question{"pick": {Type: "choice", Instructions: "Choose the matching option", Criteria: map[string]string{"yes": "Matches", "no": "Does not match"}}}
}

const validResponse = `{"model":"jev-1.13.0","answers":{"pick":{"type":"choice","choice":"yes","probabilities":{"yes":0.9,"no":0.1},"confidence":0.8}},"usage":{"input_tokens":20,"output_tokens":10}}`

func httpResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}
}

func TestEvaluateUsesOfficialContractAndKeepsConfidenceSeparate(t *testing.T) {
	client := NewClient(Config{APIKey: "unit-test-key", Model: "jev-1.13.0"})
	client.httpClient.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != Endpoint || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer unit-test-key" {
			t.Fatal("wrong API request")
		}
		var body struct {
			Model     string              `json:"model"`
			State     map[string]string   `json:"state"`
			Questions map[string]Question `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "jev-1.13.0" || body.State["text"] != "example" || body.Questions["pick"].Criteria["yes"] != "Matches" {
			t.Fatalf("wrong body: %+v", body)
		}
		return httpResponse(r, 200, validResponse), nil
	})
	result, err := client.Evaluate(context.Background(), map[string]string{"text": "example"}, choiceQuestions())
	if err != nil {
		t.Fatal(err)
	}
	answer := result.Answers["pick"]
	if answer.Confidence != 0.8 || answer.Probabilities["yes"] != 0.9 || result.Usage.InputTokens != 20 {
		t.Fatalf("lost decision details: %+v", result)
	}
}

func TestEvaluateRejectsMalformedDecisions(t *testing.T) {
	cases := map[string]string{
		"unknown choice":          strings.Replace(validResponse, `"choice":"yes"`, `"choice":"maybe"`, 1),
		"missing confidence":      strings.Replace(validResponse, `,"confidence":0.8`, "", 1),
		"out of range":            strings.Replace(validResponse, `"confidence":0.8`, `"confidence":1.1`, 1),
		"null probability":        strings.Replace(validResponse, `"no":0.1`, `"no":null`, 1),
		"incomplete distribution": strings.Replace(validResponse, `,"no":0.1`, "", 1),
		"bad sum":                 strings.Replace(validResponse, `"yes":0.9`, `"yes":0.7`, 1),
		"not winner":              strings.Replace(validResponse, `"choice":"yes"`, `"choice":"no"`, 1),
		"missing usage":           strings.Replace(validResponse, `"input_tokens":20,`, "", 1),
		"negative usage":          strings.Replace(validResponse, `"output_tokens":10`, `"output_tokens":-1`, 1),
		"unsafe model":            strings.Replace(validResponse, `jev-1.13.0`, "untrusted response text", 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeEvaluation([]byte(body), choiceQuestions()); err == nil {
				t.Fatal("accepted malformed response")
			}
		})
	}
}

func TestRoundedProbabilityTotalsPreserveReportedValues(t *testing.T) {
	questions := choiceQuestions()
	question := questions["pick"]
	probabilities := map[string]float64{"yes": 0.97, "no": 0.02}
	for index := range 39 {
		id := "other_" + strconv.Itoa(index)
		question.Criteria[id] = "Another observed option"
		probabilities[id] = 0
	}
	questions["pick"] = question
	decode := func(values map[string]float64) (Evaluation, error) {
		response := Evaluation{Model: "jev-1.13.0", Answers: map[string]ChoiceAnswer{"pick": {Type: "choice", Choice: "yes", Confidence: 0.83, Probabilities: values}}, Usage: Usage{InputTokens: 20, OutputTokens: 10}}
		data, _ := json.Marshal(response)
		return decodeEvaluation(data, questions)
	}
	// Hundredths rounding: 39 options reported as zero can carry unreported mass,
	// but two nonzero values can add at most one hundredth.
	for _, other := range []float64{0.02, 0.04, 0.01} {
		probabilities["no"] = other // provider totals 0.99, 1.01, and 0.98
		got, err := decode(probabilities)
		if err != nil {
			t.Fatalf("rounded distribution rejected: %v", err)
		}
		answer := got.Answers["pick"]
		if answer.Confidence != 0.83 || answer.Probabilities["yes"] != 0.97 || answer.Probabilities["no"] != other || len(answer.Probabilities) != 41 {
			t.Fatal("reported distribution or independent confidence was altered")
		}
	}
	probabilities["no"] = 0.05 // 1.02 cannot come from rounding two nonzero values
	_, err := decode(probabilities)
	if err == nil || CodeOf(err) != CodeInvalidResponse || !strings.Contains(err.Error(), "total 1.0200") || !strings.Contains(err.Error(), "2 nonzero") {
		t.Fatalf("excess mass was not rejected with a specific reason: %v", err)
	}
}

func TestDistributionBoundsFollowReportingPrecision(t *testing.T) {
	cases := []struct {
		values []float64
		valid  bool
	}{
		{[]float64{0.93, 0.05, 0.01, 0}, true}, // 0.99 over four options
		{[]float64{0.56, 0.23, 0.04, 0.04, 0.03, 0.02, 0.02, 0.02, 0.02, 0.01, 0, 0}, true},
		{[]float64{0.9, 0.05, 0.02}, false}, // 0.97 exceeds three options' rounding loss
		{[]float64{0.97, 0.02}, true},       // small sets keep 0.01
		{[]float64{0.97, 0.05}, false},      // 1.02 with two nonzero values
		{[]float64{0, 0, 0, 0}, false},      // no probability mass
		{[]float64{0.5, math.NaN()}, false},
		{[]float64{1.2, 0}, false},
	}
	for index, c := range cases {
		probabilities := map[string]float64{}
		best, bestValue := "", -1.0
		for i, value := range c.values {
			id := "option_" + strconv.Itoa(i)
			probabilities[id] = value
			if value > bestValue {
				best, bestValue = id, value
			}
		}
		if problem := DistributionProblem(probabilities, best); (problem == "") != c.valid {
			t.Fatalf("case %d: valid=%v problem=%q", index, c.valid, problem)
		}
	}
	for _, spread := range []int{3, 12, 40} {
		probabilities := map[string]float64{}
		for i := range 254 {
			probabilities["option_"+strconv.Itoa(i)] = 0
		}
		probabilities["none"] = 0
		// A flat top-k distribution whose hundredths lose mass to rounding.
		for i := range spread {
			probabilities["option_"+strconv.Itoa(i)] = math.Floor(100/float64(spread)) / 100
		}
		if problem := DistributionProblem(probabilities, "option_0"); problem != "" {
			t.Fatalf("spread %d rejected: %s", spread, problem)
		}
	}
}

func TestEvaluateDoesNotLeakErrorBodyAndDoesNotRetry401(t *testing.T) {
	client := NewClient(Config{APIKey: "unit-test-key"})
	calls := 0
	client.httpClient.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return httpResponse(r, 401, "echo unit-test-key private body"), nil
	})
	_, err := client.Evaluate(context.Background(), "synthetic", choiceQuestions())
	if err == nil || strings.Contains(err.Error(), "unit-test-key") || strings.Contains(err.Error(), "private") || calls != 1 {
		t.Fatalf("unsafe error or retry: %v, %d calls", err, calls)
	}
}

func TestEvaluateRetriesTransientResponse(t *testing.T) {
	client := NewClient(Config{APIKey: "unit-test-key"})
	calls := 0
	client.httpClient.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return httpResponse(r, 529, "overloaded"), nil
		}
		return httpResponse(r, 200, validResponse), nil
	})
	if _, err := client.Evaluate(context.Background(), "synthetic", choiceQuestions()); err != nil || calls != 2 {
		t.Fatalf("retry failed: %v, %d calls", err, calls)
	}
}

func TestEvaluateRespectsRetryAfterAndCancellation(t *testing.T) {
	for _, retry := range []string{"120", time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)} {
		client := NewClient(Config{APIKey: "unit-test-key"})
		calls := 0
		client.httpClient.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			response := httpResponse(r, 429, "limited")
			response.Header.Set("Retry-After", retry)
			return response, nil
		})
		if _, err := client.Evaluate(context.Background(), "synthetic", choiceQuestions()); err == nil || calls != 1 {
			t.Fatal("retried before Retry-After deadline")
		}
	}
	client := NewClient(Config{APIKey: "unit-test-key"})
	client.httpClient.Transport = transportFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Evaluate(ctx, "synthetic", choiceQuestions()); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestEvaluateNeverFollowsRedirects(t *testing.T) {
	client := NewClient(Config{APIKey: "unit-test-key"})
	calls := 0
	client.httpClient.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		response := httpResponse(r, 307, "redirect")
		response.Header.Set("Location", "https://different.example.invalid/")
		return response, nil
	})
	if _, err := client.Evaluate(context.Background(), "synthetic", choiceQuestions()); err == nil || calls != 1 {
		t.Fatalf("followed redirect: %v %d", err, calls)
	}
}

func TestEvaluateBoundsRequestAndResponse(t *testing.T) {
	client := NewClient(Config{APIKey: "unit-test-key"})
	calls := 0
	client.httpClient.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return httpResponse(r, 200, strings.Repeat("x", maxResponseBytes+1)), nil
	})
	if _, err := client.Evaluate(context.Background(), strings.Repeat("x", 2*MaxQuestionContextTokens), choiceQuestions()); err == nil || calls != 0 || CodeOf(err) != CodeInvalidRequest {
		t.Fatal("oversize context reached API")
	}
	if _, err := client.Evaluate(context.Background(), strings.Repeat("é", MaxQuestionContextTokens/2), choiceQuestions()); err == nil || calls != 0 {
		t.Fatal("non-ASCII context was budgeted as if it tokenized like ASCII")
	}
	if _, err := client.Evaluate(context.Background(), "synthetic", choiceQuestions()); err == nil || calls != 1 {
		t.Fatal("accepted oversized response")
	}
}

func fastRetries(t *testing.T) {
	t.Helper()
	previous := retryBaseDelay
	retryBaseDelay = time.Millisecond
	t.Cleanup(func() { retryBaseDelay = previous })
}

func TestProviderFailuresAreClassifiedWithoutEchoingResponseText(t *testing.T) {
	fastRetries(t)
	cases := []struct {
		status        int
		body          string
		code          string
		calls         int
		providerType  string
		mustNotAppear string
	}{
		{401, `{"detail":{"error_type":"authentication_error","message":"private provider text"}}`, CodeUnauthorized, 1, "authentication_error", "private provider text"},
		{400, `{"detail":{"error_type":"max_tokens_exceeded"}}`, CodeContextExceeded, 1, "max_tokens_exceeded", ""},
		{400, `{"detail":"Too many choices echoing request text"}`, CodeRejected, 1, "", "echoing request text"},
		{422, `{"detail":{"error_type":"Bad Type With Spaces"}}`, CodeRejected, 1, "", "Bad Type"},
		{429, `{"detail":"slow down"}`, CodeRateLimited, 3, "", "slow down"},
		{529, `overloaded`, CodeOverloaded, 3, "", ""},
		{502, `bad gateway`, CodeServerError, 3, "", ""},
	}
	for _, c := range cases {
		t.Run(strconv.Itoa(c.status)+"/"+c.code, func(t *testing.T) {
			client := NewClient(Config{APIKey: "unit-test-key"})
			calls := 0
			client.httpClient.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return httpResponse(r, c.status, c.body), nil
			})
			_, err := client.Evaluate(context.Background(), "synthetic", choiceQuestions())
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != c.code || failure.Status != c.status || failure.ProviderType != c.providerType || calls != c.calls {
				t.Fatalf("err=%v failure=%+v calls=%d", err, failure, calls)
			}
			if !strings.Contains(err.Error(), "HTTP "+strconv.Itoa(c.status)) || (c.mustNotAppear != "" && strings.Contains(err.Error(), c.mustNotAppear)) || strings.Contains(err.Error(), "unit-test-key") {
				t.Fatalf("unsafe or unspecific message: %v", err)
			}
			if c.calls == 3 && !strings.Contains(err.Error(), "after 3 attempts") {
				t.Fatalf("retry exhaustion not reported: %v", err)
			}
		})
	}
}

func TestTransportFailureIsRetriedAndReportedSpecifically(t *testing.T) {
	fastRetries(t)
	client := NewClient(Config{APIKey: "unit-test-key"})
	calls := 0
	client.httpClient.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("connection reset by peer")
		}
		return httpResponse(r, 200, validResponse), nil
	})
	if _, err := client.Evaluate(context.Background(), "synthetic", choiceQuestions()); err != nil || calls != 2 {
		t.Fatalf("transient transport failure was not retried: %v, %d calls", err, calls)
	}
	calls = 0
	client.httpClient.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("dial tcp: lookup api.typesafe.ai: no such host")
	})
	_, err := client.Evaluate(context.Background(), "synthetic", choiceQuestions())
	if CodeOf(err) != CodeConnection || calls != 3 || !strings.Contains(err.Error(), "no such host") || !strings.Contains(err.Error(), "after 3 attempts") {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestInvalidDecisionsExplainWhichCheckFailed(t *testing.T) {
	cases := map[string]string{
		"not the documented JSON shape": `not json`,
		"missing or unsafe model":       strings.Replace(validResponse, `jev-1.13.0`, "untrusted", 1),
		"token usage":                   strings.Replace(validResponse, `"input_tokens":20,`, "", 1),
		"chose an option that was not":  strings.Replace(validResponse, `"choice":"yes"`, `"choice":"maybe"`, 1),
		"1 probabilities for 2 offered": strings.Replace(validResponse, `,"no":0.1`, "", 1),
		"total 0.8000":                  strings.Replace(validResponse, `"yes":0.9`, `"yes":0.7`, 1),
		"while another has 0.90":        strings.Replace(validResponse, `"choice":"yes"`, `"choice":"no"`, 1),
		"no confidence between 0 and 1": strings.Replace(validResponse, `"confidence":0.8`, `"confidence":1.1`, 1),
	}
	for want, body := range cases {
		t.Run(want, func(t *testing.T) {
			_, err := decodeEvaluation([]byte(body), choiceQuestions())
			if CodeOf(err) != CodeInvalidResponse || !strings.Contains(err.Error(), want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestTokenBudgetsAreConservativeAndCanOnlyTighten(t *testing.T) {
	if EstimateTokens([]byte("abcd")) != 2 || EstimateTokens([]byte("é")) != 2 || EstimateTokens([]byte("中")) != 3 {
		t.Fatal("token estimate is not conservative")
	}
	questions := choiceQuestions()
	state := strings.Repeat("x", MaxQuestionContextTokens)
	if err := ValidateRequestWithin(Model, state, questions, DefaultBudget()); err != nil {
		t.Fatalf("default budget rejected a fitting request: %v", err)
	}
	if err := ValidateRequestWithin(Model, state, questions, Budget{QuestionTokens: MaxQuestionContextTokens / 2}); err == nil {
		t.Fatal("tightened budget was ignored")
	}
	if err := ValidateRequestWithin(Model, strings.Repeat("x", 2*MaxQuestionContextTokens), questions, Budget{QuestionTokens: 10 * MaxQuestionContextTokens}); err == nil {
		t.Fatal("a caller budget exceeded the documented model limit")
	}
}

func TestSharedTransportKeepsProviderConnectionsWarm(t *testing.T) {
	transport := newSharedTransport()
	if transport.IdleConnTimeout < 5*time.Minute {
		t.Fatalf("idle provider connections close after %v; agent pauses would pay a new handshake", transport.IdleConnTimeout)
	}
	if !transport.ForceAttemptHTTP2 || transport.HTTP2 == nil || transport.HTTP2.SendPingTimeout <= 0 || transport.HTTP2.PingTimeout <= 0 {
		t.Fatalf("HTTP/2 health checks are not configured: %+v", transport.HTTP2)
	}
	if transport.Proxy == nil {
		t.Fatal("the default proxy configuration was dropped")
	}
}
