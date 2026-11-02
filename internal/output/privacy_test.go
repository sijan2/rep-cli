package output

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
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

func TestStructuredStreamRedactionPreservesRecordBoundaries(t *testing.T) {
	for _, sample := range []struct{ body, contentType string }{
		{"{\"credentials\":{\"value\":\"stream-fake\"},\"ok\":1}\n{\"password\":\"prefix\\\"stream-fake\"}\n", "application/x-ndjson"},
		{"event: message\r\ndata: {\"credentials\":\r\ndata: {\"value\":\"stream-fake\"}}\r\n\r\n", "text/event-stream"},
	} {
		got := RedactBody(sample.body, sample.contentType)
		if strings.Contains(got, "stream-fake") || !strings.Contains(got, "REDACTED") {
			t.Fatalf("stream leaked: %s", got)
		}
	}
	plain := "data: {\"count\":1}\r\n\r\n"
	if RedactBody(plain, "text/event-stream") != plain {
		t.Fatal("non-sensitive SSE lost exact bytes")
	}
}

func TestPartialBodiesDoNotLeakEscapedCredentialTails(t *testing.T) {
	for _, body := range []string{
		`{"password":"prefix\"partial-secret",`,
		`{"password":"prefix\"partial-secret`,
		`request authorization: Bearer partial-secret`,
	} {
		if got := RedactBody(body, "text/plain"); strings.Contains(got, "partial-secret") {
			t.Fatalf("partial credential leaked: %s", got)
		}
	}
}

func TestURLCredentialFragmentsAreFingerprinted(t *testing.T) {
	for _, raw := range []string{"https://fixture.test/#access_token=fragment-secret", "https://fixture.test/#/route?refresh_token=fragment-secret"} {
		if got := RedactURL(raw); strings.Contains(got, "fragment-secret") || !strings.Contains(got, "REDACTED") {
			t.Fatal("fragment credential leaked", got)
		}
	}
	raw := "https://fixture.test/path?q=literal#section"
	if RedactURL(raw) != raw {
		t.Fatal("ordinary URL lost fidelity")
	}
}

func TestMultipartFormCredentialsAreRedactedWithoutLosingOtherParts(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("password", "multipart-secret"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("description", "ordinary text"); err != nil {
		t.Fatal(err)
	}
	file, err := writer.CreateFormFile("attachment", "fixture.bin")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte{0, 255, 254, 10}
	if _, err = file.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	safe := RedactBody(body.String(), writer.FormDataContentType())
	if strings.Contains(safe, "multipart-secret") || !strings.Contains(safe, "REDACTED") {
		t.Fatal("multipart password leaked")
	}
	reader := multipart.NewReader(strings.NewReader(safe), writer.Boundary())
	values := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		values[part.FormName()] = string(data)
	}
	if values["description"] != "ordinary text" || values["attachment"] != string(payload) {
		t.Fatal("redaction changed non-sensitive parts")
	}
	if got := RedactBody(body.String()[:len(body.String())-20], writer.FormDataContentType()); strings.Contains(got, "multipart-secret") {
		t.Fatal("truncated multipart credential leaked")
	}
	if got := RedactBody(body.String(), "multipart/form-data"); strings.Contains(got, "multipart-secret") {
		t.Fatal("multipart credential leaked when boundary metadata was missing")
	}
}

func TestMultipartWithoutCredentialsPreservesExactBytes(t *testing.T) {
	body := "--fixture\r\nContent-Disposition: form-data; name=description\r\n\r\nordinary text\r\n--fixture--\r\n"
	if got := RedactBody(body, "multipart/form-data; boundary=fixture"); got != body {
		t.Fatal("non-sensitive multipart lost byte fidelity")
	}
}
