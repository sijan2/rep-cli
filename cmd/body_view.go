package cmd

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/repplus/rep-cli/internal/bodyview"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

type bodyViewFlags struct {
	Info, RequireComplete bool
	Offset, MaxBytes      int
	Format, Pointer, Find string
	RecordOffset, Records int
}

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

func renderCapturedBody(cmd *cobra.Command, req *store.Request) error {
	flags := bodyView
	if flags.Offset < 0 || bodyHead < 0 || flags.MaxBytes < 1024 || flags.RecordOffset < 0 || flags.Records < 1 {
		return fmt.Errorf("offset/head/record-offset must be nonnegative, records positive, and max-bytes at least 1024")
	}
	if flags.Info && bodySave {
		return fmt.Errorf("--info and --save are mutually exclusive")
	}
	if flags.Find != "" && cmd.Flags().Changed("pointer") {
		return fmt.Errorf("--find and --pointer are mutually exclusive")
	}
	data, contentType, evidence, err := bodyEvidence(req, bodyRequest)
	if err != nil {
		return err
	}
	if flags.RequireComplete && evidence.State != "complete" && evidence.State != "not_applicable" {
		return fmt.Errorf("body is %s (%s); a new authorized observation is required to obtain missing bytes", evidence.State, evidence.Reason)
	}
	out := map[string]interface{}{"id": req.ID, "url": req.URL, "method": req.Method, "body_capture": evidence, "body_bytes": len(data), "content_type": contentType, "network_state": req.NetworkState}
	if req.Response != nil && !bodyRequest {
		out["status"] = req.Response.Status
	}
	if bodySaved != "" {
		out["saved"] = bodySaved
	}
	if flags.Info {
		return writeBodyView(cmd, out, nil, false, flags.MaxBytes)
	}
	selection, err := bodyview.Select(data, bodyview.Options{Format: flags.Format, ContentType: contentType, Pointer: flags.Pointer, HasPointer: cmd.Flags().Changed("pointer"), RecordOffset: flags.RecordOffset, Records: flags.Records, Find: flags.Find, Complete: evidence.State == "complete" || evidence.State == "not_applicable"})
	if err != nil {
		return err
	}
	out["selection"] = selection
	out["record_offset"] = flags.RecordOffset
	selected := selection.Data
	if flags.Offset > len(selected) {
		return fmt.Errorf("offset exceeds selected body size %d", len(selected))
	}
	end := len(selected)
	if bodyHead > 0 && bodyHead < end-flags.Offset {
		end = flags.Offset + bodyHead
	}
	window := selected[flags.Offset:end]
	out["selected_bytes"] = len(selected)
	out["offset"] = flags.Offset
	out["returned_bytes"] = len(window)
	out["next_offset"] = end
	out["view_complete"] = end == len(selected) && flags.Offset == 0
	binary := !utf8.Valid(window) || (selection.Format == "raw" && isBodyBinaryType(contentType))
	if bodySave {
		path, err := saveBodyArtifact(req.ID, string(window), bodyArtifactExtension(contentType, selection.Format, binary))
		if err != nil {
			return err
		}
		out["path"] = path
		out["artifact_bytes"] = len(window)
		out["artifact_encoding"] = "raw"
		artifactDigest := sha256.Sum256(window)
		out["artifact_sha256"] = hex.EncodeToString(artifactDigest[:])
		if getOutputMode() != "json" {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), path)
			return err
		}
		return writeBodyView(cmd, out, nil, false, flags.MaxBytes)
	}
	// Save a full, private selection whenever the projection cannot fit inline.
	// This is separate from capture completeness: a view can be shortened while
	// the original response is complete and fully available in its archive.
	if len(window) > flags.MaxBytes/8 {
		path, err := saveBodyArtifact(req.ID, string(selected), bodyArtifactExtension(contentType, selection.Format, binary))
		if err != nil {
			return err
		}
		out["artifact_path"] = path
		out["artifact_bytes"] = len(selected)
		out["artifact_encoding"] = "raw"
		artifactDigest := sha256.Sum256(selected)
		out["artifact_sha256"] = hex.EncodeToString(artifactDigest[:])
	}
	return writeBodyView(cmd, out, window, binary, flags.MaxBytes)
}

func isBodyBinaryType(contentType string) bool {
	value := strings.ToLower(contentType)
	return strings.HasPrefix(value, "image/") || strings.HasPrefix(value, "audio/") || strings.HasPrefix(value, "video/") || strings.HasPrefix(value, "font/") || strings.Contains(value, "octet-stream") || strings.Contains(value, "pdf") || strings.Contains(value, "zip") || strings.Contains(value, "wasm")
}

func bodyArtifactExtension(contentType, format string, binary bool) string {
	if binary {
		return ".bin"
	}
	if format != "raw" || strings.Contains(contentType, "json") {
		return ".json"
	}
	if strings.Contains(contentType, "html") {
		return ".html"
	}
	if strings.Contains(contentType, "javascript") {
		return ".js"
	}
	return ".txt"
}

func writeBodyView(cmd *cobra.Command, out map[string]interface{}, body []byte, binary bool, budget int) error {
	baseOffset, _ := out["offset"].(int)
	selectedBytes, hasSelectedBytes := out["selected_bytes"].(int)
	originalSelection, hasSelection := out["selection"].(bodyview.Selection)
	encode := func(n int) ([]byte, error) {
		if body != nil {
			if binary {
				out["body"] = base64.StdEncoding.EncodeToString(body[:n])
				out["encoding"] = "base64"
			} else {
				out["body"] = string(body[:n])
				out["encoding"] = "utf8"
			}
			out["returned_bytes"] = n
			out["next_offset"] = baseOffset + n
			complete := baseOffset == 0 && n == len(body) && (!hasSelectedBytes || n == selectedBytes)
			out["view_complete"] = complete
			if hasSelection && originalSelection.Format != "raw" && originalSelection.Format != "json" {
				selection := originalSelection
				if !complete {
					selection.NextRecord = 0
				}
				out["selection"] = selection
				out["record_page_complete"] = complete
			}
		}
		var payload interface{} = out
		if forceEnvelope && !rawJSON {
			source := "live-or-saved"
			if bodySaved != "" {
				source = "saved"
			}
			payload = output.WrapData("body", source, out)
		}
		value, err := json.Marshal(payload)
		return append(value, '\n'), err
	}
	// A response can be hundreds of MiB. Encode only a budget-sized candidate;
	// never marshal the entire body merely to discover that it cannot fit.
	inlineLimit := min(len(body), budget)
	if !binary {
		for inlineLimit > 0 && inlineLimit < len(body) && !utf8.RuneStart(body[inlineLimit]) {
			inlineLimit--
		}
	}
	value, err := encode(inlineLimit)
	if err != nil {
		return err
	}
	if len(value) > budget {
		if body == nil {
			return fmt.Errorf("body metadata exceeds --max-bytes; increase the budget")
		}
		low, high := 0, inlineLimit
		for low < high {
			mid := (low + high + 1) / 2
			candidate, err := encode(mid)
			if err != nil {
				return err
			}
			if len(candidate) <= budget {
				low = mid
			} else {
				high = mid - 1
			}
		}
		if !binary {
			for low > 0 && low < len(body) && !utf8.RuneStart(body[low]) {
				low--
			}
		}
		value, err = encode(low)
		if err != nil {
			return err
		}
		if len(value) > budget {
			return fmt.Errorf("body metadata exceeds --max-bytes; increase the budget")
		}
	}
	// JSON for every mode gives agents one loss/completeness contract, including
	// --head. Explicit --save in text mode preserves the convenient path-only API.
	_, err = cmd.OutOrStdout().Write(value)
	return err
}
