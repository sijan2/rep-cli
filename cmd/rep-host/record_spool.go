package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// A capture keeps a compact index in memory and serialized records on disk.
// The final snapshot copies validated records without rehydrating their bodies.
// The caller holds liveMu for every method.
type captureRecordSpool struct {
	file      *os.File
	writer    *bufio.Writer
	entries   map[string]spooledRecord
	positions map[string]int
	bytes     int64
}

type spooledRecord struct{ offset, length int64 }

func newCaptureRecordSpool() (*captureRecordSpool, error) {
	captureSnapshots.Lock()
	dir := captureSnapshots.dir
	captureSnapshots.Unlock()
	if dir == "" {
		return nil, fmt.Errorf("incremental capture storage is unavailable")
	}
	file, err := os.CreateTemp(dir, ".records-*.partial")
	if err != nil {
		return nil, err
	}
	return &captureRecordSpool{file: file, writer: bufio.NewWriterSize(file, 256<<10), entries: make(map[string]spooledRecord), positions: make(map[string]int)}, nil
}

func (s *captureRecordSpool) close() {
	if s == nil {
		return
	}
	_ = s.file.Close()
	_ = os.Remove(s.file.Name())
}

func (s *captureRecordSpool) read(id string) (Request, error) {
	var value Request
	if err := s.writer.Flush(); err != nil {
		return value, err
	}
	ref, ok := s.entries[id]
	if !ok {
		return value, fmt.Errorf("missing spooled request %q", id)
	}
	err := json.NewDecoder(io.NewSectionReader(s.file, ref.offset, ref.length)).Decode(&value)
	return value, err
}

func (s *captureRecordSpool) append(value Request) error {
	if value.ID == "" {
		return fmt.Errorf("incremental record requires an ID")
	}
	if _, exists := s.entries[value.ID]; !exists && len(s.entries) >= activeCaptureLimits.requests {
		return fmt.Errorf("capture exceeds REP_CAPTURE_MAX_REQUESTS (%d)", activeCaptureLimits.requests)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if int64(len(data)) > activeCaptureLimits.requestBytes {
		return fmt.Errorf("record exceeds REP_CAPTURE_MAX_REQUEST_BYTES")
	}
	// Include obsolete revisions in the disk budget. Repeated updates cannot
	// grow an unbounded private spool behind a small final snapshot.
	if int64(len(data)) > activeCaptureLimits.snapshotBytes-s.bytes {
		return fmt.Errorf("incremental capture exceeds REP_CAPTURE_MAX_SNAPSHOT_BYTES")
	}
	offset := s.bytes
	// Intermediate ACKs mean accepted, not durable. Batch small writes in a
	// bounded buffer; flush before read/seal, and fsync the completed snapshot.
	n, err := s.writer.Write(data)
	s.bytes += int64(n)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	s.entries[value.ID] = spooledRecord{offset: offset, length: int64(n)}
	return nil
}

func upsertSpooledRequestLocked(incoming Request) error {
	s := liveData.spool
	position, exists := s.positions[incoming.ID]
	if exists {
		old, err := s.read(incoming.ID)
		if err != nil {
			return err
		}
		incoming = mergeRequest(old, incoming)
	}
	if err := s.append(incoming); err != nil {
		return err
	}
	metadata := incoming
	metadata.Body, metadata.Headers = "", nil
	metadata.InitiatorDetails = nil
	if incoming.Response != nil {
		response := *incoming.Response
		response.Body, response.Headers, response.SecurityDetails, response.Timing = "", nil, nil, nil
		metadata.Response = &response
	}
	if incoming.Stream != nil {
		stream := *incoming.Stream
		stream.Events = nil
		metadata.Stream = &stream
	}
	if exists {
		liveData.Requests[position] = metadata
	} else {
		s.positions[incoming.ID] = len(liveData.Requests)
		liveData.Requests = append(liveData.Requests, metadata)
	}
	return nil
}

func captureAcknowledgement(message *Message, response map[string]interface{}) map[string]interface{} {
	if message.CaptureSequence == nil {
		return response
	}
	ack := map[string]interface{}{"action": "capture_ack", "session_id": message.SessionID, "capture_sequence": *message.CaptureSequence, "success": response["success"] == true}
	for _, field := range []string{"aborted", "already_ended", "persistence_error"} {
		if value, ok := response[field]; ok {
			ack[field] = value
		}
	}
	if response["success"] != true {
		ack["error"] = &RPCError{Code: "capture_rejected", Message: fmt.Sprint(response["error"])}
	}
	return ack
}

func validateCaptureSequenceLocked(message *Message) error {
	if message.Action == "session_abort" && message.SessionID != "" && message.SessionID == liveData.SessionID && liveData.Browser != nil {
		return nil
	}
	if message.CaptureSequence == nil {
		if liveData.spool != nil && browserCaptureActive && message.SessionID == liveData.SessionID && message.Action != "ping" {
			return fmt.Errorf("incremental capture requires acknowledged message sequence")
		}
		return nil
	}
	if message.Action == "session_begin" {
		if *message.CaptureSequence != 0 {
			return fmt.Errorf("capture sequence must begin at zero")
		}
		return nil
	}
	if message.SessionID != liveData.SessionID || !browserCaptureActive {
		return fmt.Errorf("capture acknowledgement session mismatch")
	}
	if *message.CaptureSequence != liveData.nextCaptureSequence {
		return fmt.Errorf("capture sequence mismatch: expected %d", liveData.nextCaptureSequence)
	}
	liveData.nextCaptureSequence++
	return nil
}
