package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/store"
)

func TestDownloadCapturedRequestIsSecretSafeAtomicAndDropsRange(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl unavailable")
	}
	payload := []byte("PK\x03\x04captured-download-test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Rep-Unsafe-Curlrc") != "" {
			http.Error(w, "default curl config must be disabled", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Cookie") != "session=secret" || r.Header.Get("Referer") != "https://example.test/app" {
			http.Error(w, "missing browser context", http.StatusForbidden)
			return
		}
		if r.Header.Get("Range") != "" {
			http.Error(w, "range must be dropped", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	req := &store.Request{
		ID: "h_download", Method: "GET", URL: server.URL + "/file.zip?X-Amz-Signature=secret",
		Headers: store.HeaderMap{
			":path":  {"/file.zip?X-Amz-Signature=secret"},
			"Cookie": {"session=secret"}, "cookie": {"session=secret"},
			"Referer": {"https://example.test/app"}, "Range": {"bytes=0-0"},
		},
	}
	curlHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(curlHome, ".curlrc"), []byte("header = \"X-Rep-Unsafe-Curlrc: loaded\"\nlocation\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CURL_HOME", curlHome)
	destination := filepath.Join(t.TempDir(), "artifact.zip")
	result, err := downloadCapturedRequest(context.Background(), req, destination, capturedDownloadOptions{
		Retries: 0, RetryDelay: 0, Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) || result.Bytes != int64(len(payload)) || result.Status != http.StatusOK {
		t.Fatalf("unexpected download: result=%+v body=%q", result, got)
	}
	if result.ContentType != "application/zip" || result.DetectedContentType != "application/zip" {
		t.Fatalf("content types were not exposed: %+v", result)
	}
	wantHash := sha256.Sum256(payload)
	if result.SHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("hash mismatch: %s", result.SHA256)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if _, err := downloadCapturedRequest(context.Background(), req, destination, capturedDownloadOptions{Timeout: time.Second}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected no-overwrite error, got %v", err)
	}
}

func TestDownloadCapturedRequestRejectsQuotaHTMLBeforePublishingBinary(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl unavailable")
	}
	const secretBodyMarker = "super-secret-quota-token"
	body := "<!doctype html><html><head><title>Quota exceeded</title></head><body>" + secretBodyMarker + "</body></html>"
	body += strings.Repeat(" ", 672-len(body))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	outputDirectory := t.TempDir()
	destination := filepath.Join(outputDirectory, "OpenVPN.ipa")
	_, err := downloadCapturedRequest(context.Background(), &store.Request{
		ID: "h_quota", Method: "GET", URL: server.URL + "/private?signature=secret",
	}, destination, capturedDownloadOptions{Timeout: 10 * time.Second})
	if err == nil {
		t.Fatal("expected HTML validation failure")
	}
	message := err.Error()
	for _, wanted := range []string{"HTML response", "text/html; charset=utf-8", "672 bytes"} {
		if !strings.Contains(message, wanted) {
			t.Fatalf("validation error did not contain %q: %v", wanted, err)
		}
	}
	if strings.Contains(message, secretBodyMarker) || strings.Contains(message, "signature=secret") {
		t.Fatalf("validation error leaked request or body secrets: %v", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid artifact was published: %v", statErr)
	}
	entries, readErr := os.ReadDir(outputDirectory)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("part file survived validation failure: %v", entries)
	}
}

func TestArtifactValidatorChecksContentTypeSizeMagicAndHTMLPolicy(t *testing.T) {
	zipPayload := []byte("PK\x03\x04captured-download-test")
	zipPath := filepath.Join(t.TempDir(), "transfer.part")
	if err := os.WriteFile(zipPath, zipPayload, 0600); err != nil {
		t.Fatal(err)
	}

	t.Run("all explicit checks pass", func(t *testing.T) {
		validator, err := prepareArtifactValidator(artifactValidationOptions{
			MinimumBytes:         int64(len(zipPayload)),
			ExpectedContentTypes: []string{"application/zip", "application/octet-stream"},
			ExpectedMagic:        "zip",
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := validator.validateDownloadedArtifact(zipPath, "artifact.ipa", "application/octet-stream; charset=binary")
		if err != nil {
			t.Fatal(err)
		}
		if result.Size != int64(len(zipPayload)) || result.ContentType != "application/octet-stream; charset=binary" || result.DetectedContentType != "application/zip" {
			t.Fatalf("unexpected validation metadata: %+v", result)
		}
	})

	tests := []struct {
		name        string
		options     artifactValidationOptions
		contentType string
		want        string
	}{
		{name: "minimum bytes", options: artifactValidationOptions{MinimumBytes: int64(len(zipPayload) + 1)}, contentType: "application/zip", want: "minimum is"},
		{name: "content type", options: artifactValidationOptions{ExpectedContentTypes: []string{"application/zip"}}, contentType: "application/octet-stream", want: "did not match expected application/zip"},
		{name: "missing content type", options: artifactValidationOptions{ExpectedContentTypes: []string{"application/zip"}}, want: "Content-Type (missing)"},
		{name: "magic", options: artifactValidationOptions{ExpectedMagic: "pdf"}, contentType: "application/zip", want: "expected magic pdf"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validator, err := prepareArtifactValidator(test.options)
			if err != nil {
				t.Fatal(err)
			}
			_, err = validator.validateDownloadedArtifact(zipPath, "artifact.bin", test.contentType)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}

	htmlPath := filepath.Join(t.TempDir(), "quota.part")
	if err := os.WriteFile(htmlPath, []byte("\xef\xbb\xbf  <HTML><body>quota</body></HTML>"), 0600); err != nil {
		t.Fatal(err)
	}
	validator, err := prepareArtifactValidator(artifactValidationOptions{AllowHTML: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := validator.validateDownloadedArtifact(htmlPath, "artifact.ipa", "application/octet-stream")
	if err != nil {
		t.Fatalf("--allow-html did not override automatic binary-output guard: %v", err)
	}
	if result.DetectedContentType != "text/html; charset=utf-8" {
		t.Fatalf("HTML sniff was not exposed: %+v", result)
	}
}

func TestHasObviousHTMLPrefixSkipsXMLAndLeadingComments(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		want   bool
	}{
		{name: "BOM XML and comments", prefix: "\xef\xbb\xbf <?xml version=\"1.0\"?>\n<!-- proxy -->\n<!-- quota -->\n<HTML><body>quota</body></HTML>", want: true},
		{name: "comment before doctype", prefix: "\n<!-- generated -->\n<!DOCTYPE html><title>quota</title>", want: true},
		{name: "XML document is not HTML", prefix: "<?xml version=\"1.0\"?><response>quota</response>", want: false},
		{name: "commented marker only", prefix: "<!-- <html><body>not active</body></html> -->PK\x03\x04", want: false},
		{name: "unterminated comment", prefix: "<!-- <html>", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := hasObviousHTMLPrefix([]byte(test.prefix)); got != test.want {
				t.Fatalf("hasObviousHTMLPrefix() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestArtifactValidatorRejectsHTMLAfterXMLAndComments(t *testing.T) {
	htmlPath := filepath.Join(t.TempDir(), "quota.part")
	body := []byte("\xef\xbb\xbf<?xml version=\"1.0\"?>\n<!-- edge gateway -->\n<!-- quota page -->\n<html><body>quota</body></html>")
	if err := os.WriteFile(htmlPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	validator, err := prepareArtifactValidator(artifactValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := validator.validateDownloadedArtifact(htmlPath, "artifact.ipa", "application/octet-stream")
	if err == nil || !strings.Contains(err.Error(), "HTML response") {
		t.Fatalf("expected HTML rejection, got result=%+v err=%v", result, err)
	}
	if result.DetectedContentType != "text/html; charset=utf-8" {
		t.Fatalf("HTML sniff was not exposed: %+v", result)
	}
}

func TestPrepareArtifactValidatorNormalizesAndRejectsInvalidExpectations(t *testing.T) {
	validator, err := prepareArtifactValidator(artifactValidationOptions{
		ExpectedContentTypes: []string{"Application/ZIP; ignored=value", "application/*", "application/zip"},
		ExpectedMagic:        "hex:504B0304",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(validator.expectedContentTypes, []string{"application/zip", "application/*"}) {
		t.Fatalf("unexpected normalized content types: %v", validator.expectedContentTypes)
	}
	if validator.expectedMagic.label != "hex prefix (4 bytes)" || !matchesArtifactMagic([]byte("PK\x03\x04body"), validator.expectedMagic) {
		t.Fatalf("unexpected parsed magic: %+v", validator.expectedMagic)
	}

	tests := []artifactValidationOptions{
		{MinimumBytes: -1},
		{ExpectedContentTypes: []string{"not-a-media-type"}},
		{ExpectedContentTypes: []string{"application/**"}},
		{ExpectedMagic: "hex:123"},
		{ExpectedMagic: "hex:not-hex"},
		{ExpectedMagic: "unknown"},
	}
	for _, options := range tests {
		if _, err := prepareArtifactValidator(options); err == nil {
			t.Fatalf("expected invalid options to fail: %+v", options)
		}
	}
}

func TestCapturedReplayHeadersStripPseudoAndDedupe(t *testing.T) {
	input := store.HeaderMap{
		":authority": {"example.test"}, ":path": {"/secret?token=x"},
		"Range": {"bytes=0-0"}, "range": {"bytes=0-0"},
		"Cookie": {"old=value"}, "cookie": {"session=secret"},
	}
	headers, err := capturedReplayHeaders(input, false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(headers, "\n")
	if strings.Contains(joined, ":path") || strings.Contains(strings.ToLower(joined), "range:") {
		t.Fatalf("unsafe headers survived: %q", joined)
	}
	if strings.Count(strings.ToLower(joined), "cookie:") != 1 {
		t.Fatalf("cookie was not deduplicated: %q", joined)
	}
	if !strings.Contains(joined, "cookie: session=secret") {
		t.Fatalf("case-duplicate singleton selection was not deterministic: %q", joined)
	}
	for i := 0; i < 20; i++ {
		again, replayErr := capturedReplayHeaders(input, false)
		if replayErr != nil || !reflect.DeepEqual(headers, again) {
			t.Fatalf("replay headers changed across calls: first=%q again=%q err=%v", headers, again, replayErr)
		}
	}
}

func TestGenerateCurlAlwaysDropsHTTP2Pseudoheaders(t *testing.T) {
	command := generateCurl(&store.Request{
		Method: "GET",
		URL:    "https://example.test/file?signature=visible-once",
		Headers: store.HeaderMap{
			":authority": {"example.test"},
			":path":      {"/file?signature=must-not-repeat"},
			"accept":     {"*/*"},
		},
	}, true)
	if strings.Contains(command, ":authority") || strings.Contains(command, "must-not-repeat") {
		t.Fatalf("pseudoheader leaked into curl command: %q", command)
	}
}
