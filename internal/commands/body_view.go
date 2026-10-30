package commands

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	"fmt"

	"github.com/repplus/rep-cli/internal/store"
)

// bodyEvidence never infers complete from nonempty content or Content-Length:
// legacy capture omissions and compressed wire lengths make that unreliable.
func bodyEvidence(req *store.Request, request bool) ([]byte, string, store.BodyCapture, error) {
	value, contentType, encoding := req.Body, store.HeaderFirst(req.Headers, "content-type"), ""
	metadata := req.RequestBodyCapture
	if !request {
		value = ""
		contentType = ""
		metadata = req.ResponseBodyCapture
		encoding = req.ResponseEncoding
		if req.Response != nil {
			value = req.Response.Body
			contentType = store.HeaderFirst(req.Response.Headers, "content-type")
		}
	}
	if metadata != nil && encoding == "" {
		encoding = metadata.Encoding
	}
	data := []byte(value)
	if encoding == "base64" {
		var err error
		data, err = base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, "", store.BodyCapture{}, fmt.Errorf("captured base64 body is corrupt")
		}
	} else if encoding != "" && encoding != "utf8" && encoding != "utf-8" && encoding != "text" {
		return nil, "", store.BodyCapture{}, fmt.Errorf("unsupported captured body encoding %q", encoding)
	}
	evidence := store.BodyCapture{State: "unknown", Reason: "legacy_capture_has_no_completeness_metadata", CapturedBytes: int64(len(data))}
	if metadata != nil {
		evidence = *metadata
	}
	if !request && metadata == nil {
		switch {
		case req.ResponseBodyTruncated:
			evidence.State = "partial"
			evidence.Reason = req.ResponseBodyError
			if evidence.Reason == "" {
				evidence.Reason = "legacy_body_truncated"
			}
		case req.ResponseBodyError != "" || req.ErrorText != "":
			evidence.State = "unavailable"
			if len(data) > 0 {
				evidence.State = "partial"
			}
			evidence.Reason = req.ResponseBodyError
			if evidence.Reason == "" {
				evidence.Reason = "network_failed"
			}
		}
	}
	if evidence.CapturedBytes != int64(len(data)) {
		return nil, "", evidence, fmt.Errorf("captured body size does not match its evidence metadata")
	}
	digest := sha256.Sum256(data)
	actual := hex.EncodeToString(digest[:])
	if evidence.SHA256 != "" && evidence.SHA256 != actual {
		return nil, "", evidence, fmt.Errorf("captured body digest mismatch")
	}
	evidence.SHA256 = actual
	return data, contentType, evidence, nil
}
