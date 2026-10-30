package commands

import (
	"bytes"
	"encoding/base64"

	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/store"
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
