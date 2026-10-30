package commands

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/bodyview"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/spf13/cobra"
)

var coreBodyOut string

func init() {
	legacy := bodyCmd.RunE
	bodyCmd.Use = "body h_ID [POINTER|#EVENT|p_CURSOR]"
	bodyCmd.Args = cobra.RangeArgs(1, 2)
	bodyCmd.Flags().StringVar(&coreBodyOut, "out", "", "Write decoded raw bytes privately; refuse an existing file")
	bodyCmd.RunE = func(cmd *cobra.Command, args []string) error {
		index, err := coreStore()
		if err != nil {
			return coreFailure(cmd, err)
		}
		if _, err = index.Owner(args[0]); err == nil {
			if err = coreBody(cmd, args, bodyRequest, bodyView.Find, coreBodyOut); err != nil {
				return coreFailure(cmd, err)
			}
			return nil
		}
		if len(args) > 1 || coreBodyOut != "" {
			return coreFailure(cmd, agentstate.Failure("request_not_found", "use an exact captured h_ handle", coreNext("net")))
		}
		return legacy(cmd, args)
	}
}
func coreBody(command *cobra.Command, args []string, requestSide bool, find, out string) error {
	capture, request, err := coreCapturedRequest(args[0])
	if err != nil {
		return err
	}
	index, err := coreStore()
	if err != nil {
		return err
	}
	data, contentType, evidence, err := bodyEvidence(request, requestSide)
	if err != nil {
		return err
	}
	complete := evidence.State == "complete" || evidence.State == "not_applicable"
	result := coreResult{Browser: capture.Browser.Name, Profile: capture.Browser.Profile, Complete: boolPointer(complete), Status: "ready", Meta: map[string]any{"id": request.ID, "source_archive": capture.ID, "bytes": len(data), "sha256": fmt.Sprintf("%x", sha256.Sum256(data)), "body_state": evidence.State}}
	headers := request.Headers
	if !requestSide && request.Response != nil {
		headers = request.Response.Headers
	}
	untrusted := map[string]any{"url": request.URL, "content_type": contentType, "headers": output.RedactHeaders(headers), "initiator": request.Initiator}
	result.Untrusted = untrusted
	if out != "" {
		if len(args) > 1 || find != "" {
			return coreArgument("--out exports the full body; omit selection and --find")
		}
		artifact, e := coreArtifact(out, ".bin", data)
		if e != nil {
			return e
		}
		result.Meta.(map[string]any)["artifact"] = artifact
		return emitCore(command, result)
	}
	safe := []byte(output.RedactBody(string(data), contentType))
	offset, recordOffset := 0, 0
	selector := ""
	if len(args) > 1 {
		selector = args[1]
	}
	if agentstate.CursorPattern.MatchString(selector) {
		cursor, e := index.Cursor(selector)
		if e != nil {
			return e
		}
		if cursor.Source != request.ID || cursor.Kind != "body" {
			return coreArgument("cursor belongs to a different body")
		}
		offset, recordOffset = cursor.Offset, cursor.Controls
		selector, find = cursor.Selector, cursor.Find
		requestCursor := cursor.Attempt == 1
		if requestCursor != requestSide {
			return coreArgument("cursor body side does not match --request")
		}
	}
	options := bodyview.Options{Format: "auto", ContentType: contentType, Find: find, Complete: complete, RecordOffset: recordOffset, Records: 100}
	if strings.HasPrefix(selector, "#") {
		event, e := strconv.Atoi(selector[1:])
		if e != nil || event < 0 {
			return coreArgument("event selectors use #0, #1, and so on")
		}
		options.RecordOffset, options.Records = event, 1
	} else if selector != "" {
		if !strings.HasPrefix(selector, "/") {
			return coreArgument("body selection must be a JSON pointer, #EVENT, or p_ cursor")
		}
		options.Pointer, options.HasPointer = selector, true
	}
	if len(safe) == 0 {
		untrusted["data"] = ""
		return emitCore(command, result)
	}
	if (len(data) > 8<<20 || !utf8.Valid(safe)) && selector == "" && find == "" {
		artifact, e := coreArtifact("", ".body", safe)
		if e != nil {
			return e
		}
		result.Meta.(map[string]any)["artifact"] = artifact
		return emitCore(command, result)
	}
	selection, e := bodyview.Select(safe, options)
	if e != nil {
		return agentstate.Failure("body_selection_failed", "body is not valid for the requested selection", coreNext("body", request.ID))
	}
	selected := selection.Data
	if offset > len(selected) {
		return coreArgument("cursor offset is outside this body")
	}
	end := min(len(selected), offset+8000)
	for end > offset && !utf8.Valid(selected[offset:end]) && end < len(selected) {
		end--
	}
	more := end < len(selected) || selection.More
	if offset == 0 && end == len(selected) && (selection.Format == "json" || selection.Format == "sse" || selection.Format == "ndjson" || selection.Format == "search") {
		value := decodeObject(selected)
		if value != nil {
			untrusted["data"] = value
		} else {
			untrusted["data"] = string(selected)
		}
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
		id, e := index.SaveCursor(agentstate.Cursor{Kind: "body", Owner: capture.Owner, Tab: capture.Tab, Source: request.ID, Offset: nextOffset, Controls: nextRecord, Selector: selector, Find: find, Attempt: attempt, Budget: 8000})
		if e != nil {
			return e
		}
		result.Next = coreNext("body", request.ID, id)
		if requestSide {
			result.Next += " --request"
		}
		result.Complete = boolPointer(false)
	}
	return emitCore(command, result)
}
