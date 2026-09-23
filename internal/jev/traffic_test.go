package jev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTrafficSanitizationCannotSendURLCredentialsOrArbitraryPathValues(t *testing.T) {
	input := TrafficInput{Method: "GET", URL: "https://alice:password@example.com/api/users/123/email%40example.com/opaque-secret/assets/private-filename.js?token=secret#private", ResourceType: "script", ContentType: "application/javascript; private=secret", Status: 200}
	metadata, err := SanitizeTraffic(input)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(metadata)
	for _, value := range []string{"alice", "password", "123", "email", "opaque-secret", "private", "token", "secret"} {
		if strings.Contains(string(encoded), value) {
			t.Fatalf("metadata retained sensitive value %q", value)
		}
	}
	if metadata.Path != "/api/users/[redacted]/[redacted]/[redacted]/assets/[asset].js" || metadata.Host != "example.com" {
		t.Fatalf("unexpected route shape: %+v", metadata)
	}
	metadata, err = SanitizeTraffic(TrafficInput{URL: "https://example.com", Method: "secret", ResourceType: "secret", ContentType: "secret", Status: 999})
	if err != nil || metadata.Method != "OTHER" || metadata.ResourceType != "other" || metadata.ContentType != "other" || metadata.Status != 0 {
		t.Fatal("untrusted categorical metadata survived")
	}
}

func TestClassifyPreservesDistributionAndReviewThreshold(t *testing.T) {
	client := NewClient(Config{APIKey: "unit-test-key"})
	client.httpClient.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "query-secret") || strings.Contains(string(body), "path-secret") {
			t.Fatal("private request data sent to provider")
		}
		return httpResponse(r, 200, `{"model":"jev-1.13.0","answers":{"category":{"type":"choice","choice":"api","probabilities":{"api":0.8,"document":0.05,"static":0.05,"analytics":0.05,"other":0.05},"confidence":0.75}},"usage":{"input_tokens":40,"output_tokens":15}}`), nil
	})
	result, err := client.Classify(context.Background(), TrafficInput{Method: "GET", URL: "https://example.com/api/path-secret?token=query-secret", ResourceType: "fetch"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Category != "api" || !result.NeedsReview || result.Confidence != 0.75 || result.Probabilities["api"] != 0.8 {
		t.Fatalf("classification details lost: %+v", result)
	}
}
