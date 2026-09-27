package cmd

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

type streamOptions struct {
	Saved                          string
	Info, Save, RequireComplete    bool
	After, Event                   int64
	Events, Offset, Head, MaxBytes int
}

func newStreamCommand() *cobra.Command {
	options := streamOptions{After: -1, Event: -1, Events: 20, Head: 4096, MaxBytes: 8192}
	cmd := &cobra.Command{
		Use: "stream <record-id>", Short: "Read bounded browser stream evidence",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			request, err := findBodyWebRequest(args[0], options.Saved)
			if err != nil {
				return err
			}
			if request == nil {
				return fmt.Errorf("connection not found in this task or selected archive")
			}
			return renderStream(cmd, request, options)
		},
	}
	cmd.Flags().StringVar(&options.Saved, "saved", "", "Read one immutable archive (full hash, unique prefix, or latest)")
	cmd.Flags().BoolVar(&options.Info, "info", false, "Read connection coverage without event content")
	cmd.Flags().Int64Var(&options.After, "after", -1, "Exclusive event sequence cursor for metadata pages")
	cmd.Flags().Int64Var(&options.Event, "event", -1, "Read the exact payload of one event sequence")
	cmd.Flags().IntVar(&options.Events, "events", 20, "Maximum event descriptors in a page (1–1000)")
	cmd.Flags().IntVar(&options.Offset, "offset", 0, "Byte offset within the decoded event payload")
	cmd.Flags().IntVar(&options.Head, "head", 4096, "Maximum payload bytes; 0 selects all (output remains bounded)")
	cmd.Flags().BoolVar(&options.Save, "save", false, "Save the selected event byte range as a private artifact")
	cmd.Flags().BoolVar(&options.RequireComplete, "require-complete", false, "Require complete evidence within the declared capture scope")
	cmd.Flags().IntVar(&options.MaxBytes, "max-bytes", 8192, "Maximum JSON output bytes (at least 1024)")
	return cmd
}

func renderStream(cmd *cobra.Command, request *store.Request, options streamOptions) error {
	if options.MaxBytes < 1024 || options.Events < 1 || options.Events > 1000 || options.After < -1 || options.Event < -1 || options.Offset < 0 || options.Head < 0 {
		return fmt.Errorf("invalid stream bounds: max-bytes >= 1024, events 1–1000, cursors >= -1, offset/head >= 0")
	}
	if options.Info && (options.Event >= 0 || options.Save) {
		return fmt.Errorf("--info cannot be combined with --event or --save")
	}
	if options.Event < 0 && (options.Save || options.Offset != 0 || cmd.Flags().Changed("head")) {
		return fmt.Errorf("--save, --offset and --head require --event")
	}
	stream := request.Stream
	if !store.IsStreamKind(request.RecordKind) || stream == nil || stream.Protocol != request.RecordKind || stream.Version != 1 {
		return fmt.Errorf("record is not a supported browser stream; use rep body for HTTP bodies")
	}
	if err := validateStreamCoverage(stream); err != nil {
		return err
	}
	if options.RequireComplete && stream.Capture.State != "complete" {
		return fmt.Errorf("stream coverage is %s (%s)", stream.Capture.State, stream.Capture.Reason)
	}
	if options.RequireComplete && stream.PayloadSemantics == "unavailable" {
		return fmt.Errorf("stream payload capture is unavailable within the declared scope")
	}
	validation := "not_checked"
	if options.RequireComplete {
		if err := validateStreamPayloads(stream.Events); err != nil {
			return err
		}
		validation = "encoding_and_length_verified"
	}
	out := map[string]interface{}{
		"id": request.ID, "url": request.URL, "connection_id": stream.ConnectionID,
		"protocol": stream.Protocol, "version": stream.Version, "state": stream.State,
		"capture": stream.Capture, "source": request.CaptureSource,
		"clock_domain":       streamClockDomain(stream),
		"payload_validation": validation,
	}
	if stream.Source != "" {
		out["source"] = stream.Source
		if request.CaptureSource != "" {
			out["capture_source"] = request.CaptureSource
		}
	}
	if stream.Clock != "" {
		out["clock"] = stream.Clock
	}
	if stream.PayloadSemantics != "" {
		out["payload_semantics"] = stream.PayloadSemantics
	}
	if len(stream.Metadata) > 0 {
		out["metadata"] = stream.Metadata
	}
	if request.SourceSessionID != "" {
		out["source_session_id"] = request.SourceSessionID
	}
	if options.Saved != "" {
		out["saved"] = options.Saved
	}
	if options.Info {
		return writePayloadView(cmd, out, nil, false, options.MaxBytes, "stream", options.Saved)
	}
	if options.Event >= 0 {
		index := sort.Search(len(stream.Events), func(i int) bool { return stream.Events[i].Sequence >= options.Event })
		if index == len(stream.Events) || stream.Events[index].Sequence != options.Event {
			return fmt.Errorf("event sequence %d was not captured", options.Event)
		}
		event := stream.Events[index]
		if (event.Kind != "message" && event.Kind != "chunk" && event.Kind != "datagram") || stream.PayloadSemantics == "unavailable" {
			return fmt.Errorf("event %d has no captured payload; inspect its event descriptor and coverage", event.Sequence)
		}
		data, err := streamPayload(event)
		if err != nil {
			return err
		}
		if !options.RequireComplete {
			out["payload_validation"] = "selected_event_encoding_and_length_verified"
		}
		if options.Offset > len(data) {
			return fmt.Errorf("offset exceeds captured event size %d", len(data))
		}
		end := len(data)
		if options.Head > 0 && options.Head < end-options.Offset {
			end = options.Offset + options.Head
		}
		window := data[options.Offset:end]
		event.Payload = ""
		out["event"] = event
		out["selected_bytes"], out["offset"] = len(data), options.Offset
		out["payload_complete"] = !event.Truncated && event.Error == ""
		if options.Save {
			path, err := saveBodyArtifact(request.ID, string(window), ".bin")
			if err != nil {
				return err
			}
			digest := sha256.Sum256(window)
			out["path"], out["artifact_sha256"], out["artifact_bytes"] = path, hex.EncodeToString(digest[:]), len(window)
			out["next_offset"], out["view_complete"] = end, options.Offset == 0 && end == len(data)
			return writePayloadView(cmd, out, nil, false, options.MaxBytes, "stream", options.Saved)
		}
		return writePayloadView(cmd, out, window, event.PayloadEncoding == "base64" || !utf8.Valid(window), options.MaxBytes, "stream", options.Saved)
	}
	start := sort.Search(len(stream.Events), func(i int) bool { return stream.Events[i].Sequence > options.After })
	end := min(start+options.Events, len(stream.Events))
	events := make([]map[string]interface{}, 0, end-start)
	for _, event := range stream.Events[start:end] {
		// Payloads are fetched explicitly by sequence. Never copy them into a
		// metadata page merely to discover that JSON exceeds the output budget.
		item := map[string]interface{}{"sequence": event.Sequence, "kind": event.Kind, "bytes": event.Bytes}
		if event.Timestamp != 0 {
			item["timestamp"] = event.Timestamp
		}
		if event.Direction != "" {
			item["direction"] = event.Direction
		}
		if event.ChannelID != "" {
			item["channel_id"] = event.ChannelID
		}
		if len(event.Metadata) > 0 {
			item["metadata"] = event.Metadata
		}
		if event.Reason != "" {
			item["reason"] = event.Reason
		}
		if event.Kind == "message" || event.Kind == "chunk" || event.Kind == "datagram" || event.PayloadEncoding != "" || event.Payload != "" || event.Bytes > 0 {
			item["payload_omitted"] = true
		}
		if stream.Protocol == "websocket" && event.Kind == "message" {
			item["opcode"] = event.Opcode
		}
		if event.PayloadEncoding != "" {
			item["payload_encoding"] = event.PayloadEncoding
		}
		if event.Truncated {
			item["truncated"] = true
		}
		if event.Error != "" {
			item["error"] = event.Error
		}
		events = append(events, item)
	}
	encode := func(n int) ([]byte, error) {
		out["events"], out["returned_events"] = events[:n], n
		out["next_after"] = options.After
		if n > 0 {
			out["next_after"] = stream.Events[start+n-1].Sequence
		}
		out["remaining_events"], out["page_complete"] = len(stream.Events)-start-n, start+n == len(stream.Events)
		var payload interface{} = out
		if forceEnvelope && !rawJSON {
			source := "live-or-saved"
			if options.Saved != "" {
				source = "saved"
			}
			payload = output.WrapData("stream", source, out)
		}
		value, err := json.Marshal(payload)
		return append(value, '\n'), err
	}
	low, high := 0, len(events)
	for low < high {
		mid := (low + high + 1) / 2
		value, err := encode(mid)
		if err != nil {
			return err
		}
		if len(value) <= options.MaxBytes {
			low = mid
		} else {
			high = mid - 1
		}
	}
	value, err := encode(low)
	if err != nil {
		return err
	}
	if len(value) > options.MaxBytes || (low == 0 && len(events) > 0) {
		return fmt.Errorf("stream metadata exceeds --max-bytes; increase the budget")
	}
	_, err = cmd.OutOrStdout().Write(value)
	return err
}

func validateStreamCoverage(stream *store.StreamCapture) error {
	coverage := stream.Capture
	if coverage.CapturedEvents < 0 || coverage.ObservedEvents < coverage.CapturedEvents || coverage.DroppedEvents < 0 || coverage.DroppedEvents != coverage.ObservedEvents-coverage.CapturedEvents || coverage.CapturedBytes < 0 || coverage.ObservedBytes < coverage.CapturedBytes {
		return fmt.Errorf("stream coverage has invalid byte or event counts")
	}
	if stream.Capture.CapturedEvents != int64(len(stream.Events)) {
		return fmt.Errorf("stream event count does not match its coverage metadata")
	}
	if coverage.State == "complete" && (stream.State != "closed" || coverage.Reason != "" || len(stream.Events) == 0 || stream.Events[0].Sequence != 1) {
		return fmt.Errorf("complete stream has incomplete lifecycle metadata")
	}
	var total int64
	previous := int64(-1)
	for _, event := range stream.Events {
		if event.Sequence <= previous || event.Bytes < 0 {
			return fmt.Errorf("stream has invalid event ordering or size")
		}
		if stream.Capture.State == "complete" && (event.Kind == "gap" || event.Truncated || (previous >= 0 && event.Sequence != previous+1)) {
			return fmt.Errorf("complete stream contains incomplete events")
		}
		previous = event.Sequence
		if event.Bytes > stream.Capture.CapturedBytes-total {
			return fmt.Errorf("stream byte count does not match its coverage metadata")
		}
		total += event.Bytes
	}
	if total != stream.Capture.CapturedBytes {
		return fmt.Errorf("stream byte count does not match its coverage metadata")
	}
	if stream.Capture.State == "complete" && (stream.Capture.DroppedEvents != 0 || stream.Capture.ObservedEvents != stream.Capture.CapturedEvents || stream.Capture.ObservedBytes != total) {
		return fmt.Errorf("complete stream has loss in coverage metadata")
	}
	return nil
}

func streamClockDomain(stream *store.StreamCapture) string {
	switch stream.Clock {
	case "performance_now_seconds":
		return "performance.now() seconds within one realm; origins differ across contexts; absent timestamps are unknown"
	case "cdp_monotonic_seconds":
		return "CDP monotonic seconds; absent timestamps are unknown"
	case "":
		if stream.Protocol == "websocket" && stream.Source == "" {
			return "CDP monotonic seconds; absent timestamps are unknown"
		}
	}
	return "unknown; timestamps must not be compared across contexts"
}

func streamPayload(event store.StreamEvent) ([]byte, error) {
	data := []byte(event.Payload)
	switch event.PayloadEncoding {
	case "", "utf-8", "utf8", "text":
		if !utf8.ValidString(event.Payload) {
			return nil, fmt.Errorf("event %d has invalid UTF-8 payload", event.Sequence)
		}
	case "base64":
		var err error
		data, err = base64.StdEncoding.DecodeString(event.Payload)
		if err != nil {
			return nil, fmt.Errorf("event %d has corrupt base64 payload", event.Sequence)
		}
	default:
		return nil, fmt.Errorf("unsupported payload encoding %q", event.PayloadEncoding)
	}
	if int64(len(data)) != event.Bytes {
		return nil, fmt.Errorf("event %d size does not match captured bytes", event.Sequence)
	}
	if data == nil {
		data = []byte{}
	}
	return data, nil
}

// Stronger validation is explicit. Base64 is streamed through a bounded decoder
// rather than allocating every retained binary message merely to count it.
func validateStreamPayloads(events []store.StreamEvent) error {
	for _, event := range events {
		var size int64
		switch event.PayloadEncoding {
		case "", "utf-8", "utf8", "text":
			if !utf8.ValidString(event.Payload) {
				return fmt.Errorf("event %d has invalid UTF-8 payload", event.Sequence)
			}
			size = int64(len(event.Payload))
		case "base64":
			var err error
			size, err = io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding, strings.NewReader(event.Payload)))
			if err != nil {
				return fmt.Errorf("event %d has corrupt base64 payload", event.Sequence)
			}
		default:
			return fmt.Errorf("unsupported payload encoding %q", event.PayloadEncoding)
		}
		if size != event.Bytes {
			return fmt.Errorf("event %d size does not match captured bytes", event.Sequence)
		}
	}
	return nil
}

func init() { rootCmd.AddCommand(newStreamCommand()) }
