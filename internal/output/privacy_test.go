package output

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/store"
)

func TestEveryRequestProjectionRedactsCredentialsWithoutChangingCapture(t *testing.T) {
	request := store.Request{ID: "h_fixture", Method: "POST", URL: "https://fixture.invalid/?access_token=fake-query&view=1",
		Headers:  store.HeaderMap{"Cookie": {"fake-cookie"}, "Authorization": {"Bearer fake-auth"}, "X-API-Key": {"fake-api-key"}},
		Body:     `{"password":"fake-password","profile":{"accessToken":"fake-token"},"name":"fixture"}`,
		Response: &store.Response{Status: 200, Headers: store.HeaderMap{"Set-Cookie": {"fake-set-cookie"}, "Content-Type": {"application/json"}}, Body: `{"client_secret":"fake-secret","ok":true}`}}
	for _, mode := range []store.OutputMode{store.OutputJSON, store.OutputFull, store.OutputCompact, store.OutputMeta} {
		data, _ := json.Marshal(FormatRequest(&request, mode))
		for _, secret := range []string{"fake-query", "fake-cookie", "fake-auth", "fake-api-key", "fake-password", "fake-token", "fake-set-cookie", "fake-secret"} {
			if strings.Contains(string(data), secret) {
				t.Errorf("%s leaked %s: %s", mode, secret, data)
			}
		}
		if !strings.Contains(string(data), "sha256:") {
			t.Errorf("%s missing fingerprints", mode)
		}
	}
	if request.Headers["Cookie"][0] != "fake-cookie" || !strings.Contains(request.Body, "fake-password") {
		t.Fatal("redaction mutated retained evidence")
	}
}

func TestBodyCredentialProjectionAndFidelity(t *testing.T) {
	for _, sample := range []struct{ body, contentType string }{
		{`{"nested":{"refresh_token":"fake-secret"}}`, "application/json"},
		{"name=fixture&password=fake-secret", "application/x-www-form-urlencoded"},
		{"data: {\"access_token\":\"fake-secret\"}\n\n", "text/event-stream"},
		{"Authorization: fake-secret\n", "text/plain"},
		{"Authorization: Bearer fake-secret\n", "text/plain"},
		{"Cookie: a=fake-secret; b=fake-secret\n", "text/plain"},
	} {
		if got := RedactBody(sample.body, sample.contentType); strings.Contains(got, "fake-secret") || !strings.Contains(got, "REDACTED") {
			t.Fatalf("credential leaked: %s", got)
		}
	}
	for _, unchanged := range []string{"plain\n  text", "{ \"count\": 2, \"data\": [1, 2] }"} {
		if RedactBody(unchanged, "text/plain") != unchanged {
			t.Fatal("non-sensitive document lost fidelity")
		}
	}
}
