package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/repplus/rep-cli/internal/bodyview"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

func TestBodyEvidenceDistinguishesMissingEmptyAndCorrupt(t *testing.T) {
	request := &store.Request{Response: &store.Response{}}
	_, _, evidence, err := bodyEvidence(request, false)
	if err != nil || evidence.State != "unknown" {
		t.Fatalf("legacy empty invented completeness: %+v %v", evidence, err)
	}
	request.ResponseBodyCapture = &store.BodyCapture{State: "complete", CapturedBytes: 0}
	_, _, evidence, err = bodyEvidence(request, false)
	if err != nil || evidence.State != "complete" {
		t.Fatal(err)
	}
	request.ResponseEncoding = "base64"
	request.Response.Body = base64.StdEncoding.EncodeToString([]byte{0, 255, 1})
	request.ResponseBodyCapture.CapturedBytes = 3
	data, _, _, err := bodyEvidence(request, false)
	if err != nil || !bytes.Equal(data, []byte{0, 255, 1}) {
		t.Fatalf("binary decode: %v %v", data, err)
	}
	request.ResponseBodyCapture.SHA256 = strings.Repeat("0", 64)
	if _, _, _, err := bodyEvidence(request, false); err == nil {
		t.Fatal("digest corruption accepted")
	}
	request.ResponseBodyCapture = nil
	request.ResponseBodyTruncated = true
	_, _, evidence, err = bodyEvidence(request, false)
	if err != nil || evidence.State != "partial" {
		t.Fatalf("legacy loss hidden: %+v %v", evidence, err)
	}
}

func TestBodyViewBudgetPreservesUTF8AndRecoveryOffset(t *testing.T) {
	buffer := new(bytes.Buffer)
	command := &cobra.Command{}
	command.SetOut(buffer)
	body := []byte(strings.Repeat("é\"\n", 1000))
	out := map[string]interface{}{"id": "fixture", "offset": 0, "view_complete": true}
	if err := writeBodyView(command, out, body, false, 1024); err != nil {
		t.Fatal(err)
	}
	if buffer.Len() > 1024 || !json.Valid(bytes.TrimSpace(buffer.Bytes())) {
		t.Fatal("budget or JSON contract violated")
	}
	var view struct {
		Body     string `json:"body"`
		Returned int    `json:"returned_bytes"`
		Next     int    `json:"next_offset"`
		Complete bool   `json:"view_complete"`
	}
	if err := json.Unmarshal(buffer.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(view.Body) || view.Returned != len(view.Body) || view.Next != view.Returned || view.Complete || !bytes.Equal([]byte(view.Body), body[:view.Returned]) {
		t.Fatalf("lossy preview: %+v", view)
	}
}

func TestBodyViewRequestHeadWithoutResponseAndBinarySave(t *testing.T) {
	summaryTestScope(t, "bodies", "agent")
	previousFlags, previousHead, previousRequest, previousSave := bodyView, bodyHead, bodyRequest, bodySave
	t.Cleanup(func() {
		bodyView, bodyHead, bodyRequest, bodySave = previousFlags, previousHead, previousRequest, previousSave
	})
	bodyView = bodyViewFlags{Format: "raw", MaxBytes: 2048, Records: 20}
	bodyHead = 3
	bodyRequest = true
	bodySave = false
	command := &cobra.Command{}
	command.Flags().String("pointer", "", "")
	buffer := new(bytes.Buffer)
	command.SetOut(buffer)
	if err := renderCapturedBody(command, &store.Request{ID: "request-only", Method: "POST", Body: "abcdef"}); err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(buffer.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["body"] != "abc" || out["next_offset"] != float64(3) {
		t.Fatalf("wrong request head: %s", buffer.Bytes())
	}
	bodyRequest = false
	bodySave = true
	bodyHead = 0
	buffer.Reset()
	request := &store.Request{ID: "binary", ResponseEncoding: "base64", Response: &store.Response{Body: base64.StdEncoding.EncodeToString([]byte{0, 255, 1})}, ResponseBodyCapture: &store.BodyCapture{State: "complete", CapturedBytes: 3}}
	if err := renderCapturedBody(command, request); err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(buffer.String())
	if strings.HasPrefix(path, "{") {
		_ = json.Unmarshal(buffer.Bytes(), &out)
		path, _ = out["path"].(string)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, []byte{0, 255, 1}) {
		t.Fatalf("saved base64 instead of bytes: %v %v", data, err)
	}
}

func TestBodyRecordPaginationDoesNotAcknowledgeAnOmittedPage(t *testing.T) {
	buffer := new(bytes.Buffer)
	command := &cobra.Command{}
	command.SetOut(buffer)
	body := []byte(strings.Repeat("x", 2<<20))
	out := map[string]interface{}{"offset": 0, "selected_bytes": len(body), "selection": bodyview.Selection{Format: "sse", Records: 20, NextRecord: 20}}
	if err := writeBodyView(command, out, body, false, 1024); err != nil {
		t.Fatal(err)
	}
	var result struct {
		PageComplete bool `json:"record_page_complete"`
		Selection    struct {
			NextRecord *int `json:"next_record"`
		} `json:"selection"`
	}
	if err := json.Unmarshal(buffer.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if buffer.Len() > 1024 || result.PageComplete || result.Selection.NextRecord != nil {
		t.Fatalf("omitted records were acknowledged: %s", buffer.Bytes())
	}
}
