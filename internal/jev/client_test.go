package jev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

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
	if _, err := client.Evaluate(context.Background(), strings.Repeat("x", maxQuestionContextBytes), choiceQuestions()); err == nil || calls != 0 {
		t.Fatal("oversize context reached API")
	}
	if _, err := client.Evaluate(context.Background(), "synthetic", choiceQuestions()); err == nil || calls != 1 {
		t.Fatal("accepted oversized response")
	}
}
