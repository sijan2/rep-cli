package commands

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/bodyview"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var coreBodyOut string

func init() {
	legacy := bodyCmd.RunE
	bodyCmd.Use = "body h_ID [POINTER|#EVENT|p_CURSOR]"
	bodyCmd.Args = cobra.RangeArgs(1, 2)
	bodyCmd.Flags().StringVar(&coreBodyOut, "out", "", "Write decoded raw bytes privately; refuse an existing file")
	bodyCmd.RunE = func(cmd *cobra.Command, args []string) error {
		// Only explicit old spellings enter the compatibility resolver. A canonical
		// new handle must never fall through on missing/corrupt registry evidence.
		index, err := coreStore()
		if err != nil {
			return coreFailure(cmd, err)
		}
		_, known := index.Owner(args[0])
		if known == nil || coreRequestHandle.MatchString(args[0]) || len(args) > 1 || coreBodyOut != "" {
			if err = coreBody(cmd, args, bodyRequest, bodyView.Find, coreBodyOut); err != nil {
				return coreFailure(cmd, err)
			}
			return nil
		}
		return legacy(cmd, args)
	}
}
func coreBody(command *cobra.Command, args []string, requestSide bool, find, out string) error {
	capture, request, err := coreDataRequest(args[0])
	if err != nil {
		return err
	}
	index, err := coreStore()
	if err != nil {
		return err
	}
	offset, recordOffset := 0, 0
	selector := ""
	digest := ""
	if len(args) > 1 {
		selector = args[1]
	}
	if strings.HasPrefix(selector, "p_") {
		cursor, e := index.Cursor(selector)
		if e != nil {
			return e
		}
		if cursor.Source != request.ID || cursor.Kind != "body" {
			return coreArgument("cursor belongs to a different body")
		}
		if (cursor.Attempt == 1) != requestSide {
			return coreArgument("cursor body side does not match --request")
		}
		if find != "" && find != cursor.Find {
			return coreArgument("cursor already includes its --find selection")
		}
		offset, recordOffset, selector, find, digest = cursor.Offset, cursor.Controls, cursor.Selector, cursor.Find, cursor.Digest
	}
	if offset < 0 || recordOffset < 0 {
		return coreArgument("invalid body cursor offset")
	}
	event := -1
	if strings.HasPrefix(selector, "#") {
		event, err = strconv.Atoi(selector[1:])
		if err != nil || event < 0 {
			return coreArgument("event selectors use #0, #1, and so on")
		}
	} else if selector != "" && !strings.HasPrefix(selector, "/") {
		return coreArgument("body selection must be a JSON pointer, #EVENT, or p_ cursor")
	}
	if find != "" && selector != "" {
		return coreArgument("--find cannot be combined with a pointer or event selector")
	}
	if out != "" && (selector != "" || find != "" || offset != 0 || recordOffset != 0) {
		return coreArgument("--out exports the full body; omit selection and --find")
	}
	data, contentType, evidence, err := bodyEvidence(request, requestSide)
	streamEvent := false
	if !requestSide && store.IsStreamKind(request.RecordKind) {
		data, contentType, evidence, err = coreStreamEvidence(request, event)
		streamEvent = event >= 0
	}
	if err != nil {
		return agentstate.Failure("body_unavailable", "captured body bytes failed evidence validation", coreNext("net", capture.ID))
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	if digest != "" && digest != hash {
		return agentstate.Failure("archive_changed", "the retained body changed; read it again from its handle", coreNext("body", request.ID))
	}
	sourceComplete := evidence.State == "complete" || evidence.State == "not_applicable"
	status := 0
	responseHeaders := store.HeaderMap(nil)
	if request.Response != nil {
		status = request.Response.Status
		responseHeaders = request.Response.Headers
	}
	meta := map[string]any{"id": request.ID, "source_archive": capture.ID, "bytes": len(data), "sha256": hash, "body_state": evidence.State, "source_complete": sourceComplete, "request": requestSide, "method": request.Method, "status_code": status, "body_capture": map[string]any{"state": evidence.State, "captured_bytes": len(data), "sha256": hash}}
	headers := responseHeaders
	if requestSide {
		headers = request.Headers
	}
	untrusted := map[string]any{"url": output.RedactURL(request.URL), "content_type": contentType, "headers": output.RedactHeaders(headers), "request_headers": output.RedactHeaders(request.Headers), "response_headers": output.RedactHeaders(responseHeaders), "initiator": output.RedactURL(request.Initiator)}
	if evidence.Reason != "" {
		untrusted["capture_reason"] = evidence.Reason
	}
	result := coreResult{Browser: capture.Browser.Name, Profile: capture.Browser.Profile, Complete: boolPointer(sourceComplete), Status: "ready", Meta: meta, Untrusted: untrusted}
	// Headers and URLs may themselves be arbitrarily large. Retain redacted
	// metadata in a private artifact rather than exceeding the inline budget.
	encoded, _ := output.Marshal(output.RedactValue(untrusted))
	if len(encoded) > 2200 {
		artifact, e := coreArtifact("", ".metadata.json", encoded)
		if e != nil {
			return e
		}
		meta["metadata_artifact"], meta["metadata_complete"] = artifact, false
		untrusted = map[string]any{"url": coreByteClip(output.RedactURL(request.URL), 512), "content_type": coreByteClip(contentType, 128), "metadata_omitted": true}
		result.Untrusted = untrusted
	}
	if out != "" {
		artifact, e := coreArtifact(out, ".bin", data)
		if e != nil {
			return e
		}
		meta["artifact"], meta["artifact_path"] = artifact, artifact["path"]
		return emitCore(command, result)
	}
	safe := []byte(output.RedactBody(string(data), contentType))
	options := bodyview.Options{Format: "auto", ContentType: contentType, Find: find, Complete: sourceComplete, RecordOffset: recordOffset, Records: 32}
	if event >= 0 && !streamEvent {
		ct := strings.ToLower(contentType)
		if !strings.Contains(ct, "event-stream") && !strings.Contains(ct, "ndjson") && !strings.Contains(ct, "jsonl") {
			return coreArgument("#EVENT requires an SSE, NDJSON, or captured stream body")
		}
		options.RecordOffset, options.Records = event, 1
	} else if strings.HasPrefix(selector, "/") {
		options.Pointer, options.HasPointer = selector, true
	}
	if len(safe) == 0 {
		if selector != "" {
			return agentstate.Failure("body_selection_failed", "the selected body is empty", coreNext("body", request.ID))
		}
		untrusted["data"] = ""
		return emitCore(command, result)
	}
	if (len(data) > 8<<20 || !utf8.Valid(safe)) && selector == "" && find == "" {
		artifact, e := coreArtifact("", ".body", safe)
		if e != nil {
			return e
		}
		meta["artifact"], meta["artifact_path"], meta["redacted"] = artifact, artifact["path"], true
		return emitCore(command, result)
	}
	selection, e := bodyview.Select(safe, options)
	if e != nil {
		if selector != "" {
			return agentstate.Failure("body_selection_failed", "body is not valid for the requested selection", coreNext("body", request.ID))
		}
		// A partial JSON capture is still evidence. Return its bounded text and
		// truthful completeness, not a selection error that suggests no content.
		selection = bodyview.Selection{Data: safe, Format: "raw"}
		meta["structured_valid"] = false
		result.Complete = boolPointer(false)
	}
	if event >= 0 && !streamEvent && selection.Records == 0 {
		return agentstate.Failure("event_not_found", "the requested event was not captured", coreNext("body", request.ID))
	}
	if selection.PendingBytes > 0 {
		meta["pending_bytes"] = selection.PendingBytes
		result.Complete = boolPointer(false)
	}
	selected := selection.Data
	if offset > len(selected) {
		return coreArgument("cursor offset is outside this body")
	}
	// Bound actual JSON-escaped bytes rather than just the raw substring.
	end := coreBodyWindow(selected, offset, 5500)
	moreRecords := selection.More && event < 0
	more := end < len(selected) || moreRecords
	meta["format"], meta["offset"], meta["selected_bytes"] = selection.Format, offset, len(selected)
	meta["offset_basis"] = "redacted_body"
	if offset == 0 && end == len(selected) && json.Valid(selected) && selection.Format != "raw" {
		untrusted["data"] = json.RawMessage(selected)
	} else {
		untrusted["data"] = string(selected[offset:end])
	}
	if more {
		attempt := 0
		if requestSide {
			attempt = 1
		}
		nextOffset, nextRecord := end, options.RecordOffset
		if end == len(selected) {
			nextOffset, nextRecord = 0, selection.NextRecord
		}
		cursor, e := index.SaveCursor(agentstate.Cursor{Kind: "body", Owner: capture.Owner, Tab: capture.Tab, Source: request.ID, Offset: nextOffset, Controls: nextRecord, Selector: selector, Find: find, Attempt: attempt, Budget: 5500, Digest: hash})
		if e != nil {
			return e
		}
		result.Next = coreNext("body", request.ID, cursor)
		if requestSide {
			result.Next += " --request"
		}
		result.Complete = boolPointer(false)
	}
	return emitCore(command, result)
}
func coreBodyWindow(data []byte, offset, budget int) int {
	end := min(len(data), offset+budget)
	for end > offset && end < len(data) && !utf8.RuneStart(data[end]) {
		end--
	}
	for end > offset {
		encoded, _ := output.Marshal(string(data[offset:end]))
		if len(encoded) <= budget {
			break
		}
		end = offset + (end-offset)*budget/len(encoded)
		for end > offset && end < len(data) && !utf8.RuneStart(data[end]) {
			end--
		}
	}
	return end
}
func coreStreamEvidence(request *store.Request, event int) ([]byte, string, store.BodyCapture, error) {
	stream := request.Stream
	if stream == nil || stream.Version != 1 || stream.Protocol != request.RecordKind {
		return nil, "", store.BodyCapture{}, fmt.Errorf("invalid stream")
	}
	if err := validateStreamCoverage(stream); err != nil {
		return nil, "", store.BodyCapture{}, err
	}
	evidence := store.BodyCapture{State: stream.Capture.State, Reason: stream.Capture.Reason}
	if event >= 0 {
		for _, entry := range stream.Events {
			if entry.Sequence != int64(event) {
				continue
			}
			if stream.PayloadSemantics == "unavailable" || (entry.Kind != "message" && entry.Kind != "chunk" && entry.Kind != "datagram") {
				return nil, "", evidence, fmt.Errorf("event has no payload")
			}
			data, err := streamPayload(entry)
			if entry.Truncated || entry.Error != "" {
				evidence.State = "partial"
			}
			return data, "text/plain", evidence, err
		}
		return nil, "", evidence, fmt.Errorf("event was not captured")
	}
	// Preserve connection coverage and event descriptors; payloads are read by
	// sequence using #EVENT, not eagerly dumped into the metadata observation.
	events := make([]store.StreamEvent, len(stream.Events))
	copy(events, stream.Events)
	for i := range events {
		events[i].Payload = ""
	}
	data, err := json.Marshal(map[string]any{"protocol": stream.Protocol, "state": stream.State, "capture": stream.Capture, "payload_semantics": stream.PayloadSemantics, "events": events})
	if stream.PayloadSemantics == "unavailable" {
		evidence.State = "unavailable"
	}
	return data, "application/json", evidence, err
}
