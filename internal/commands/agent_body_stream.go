package commands

import (
	"encoding/base64"
	"fmt"
	"github.com/repplus/rep-cli/internal/store"
	"unicode/utf8"
)

// Core body and the legacy stream adapter share evidence validation.
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
