package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"regexp"
	"strings"
)

const requestChunkBytes = 192 << 10
const maxConcurrentRequestTransfers = 16

var transferPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type requestTransfer struct {
	file           *os.File
	digest         hash.Hash
	expectedDigest string
	expectedBytes  int64
	bytes          int64
	next           int
}

// Accessed only under liveMu. Partial records are never published in Requests.
var requestTransfers = map[string]*requestTransfer{}

func cleanupRequestTransfersLocked() {
	for id, transfer := range requestTransfers {
		_ = transfer.file.Close()
		_ = os.Remove(transfer.file.Name())
		delete(requestTransfers, id)
	}
}

func failCaptureLocked(reason string) {
	if liveData.Browser == nil {
		liveData.Browser = &BrowserSession{}
	}
	if liveData.Browser.CaptureError == "" {
		liveData.Browser.CaptureError = reason
	}
	rememberCaptureFailure(liveData.SessionID, liveData.Browser.CaptureError)
}

func acceptRequestChunkLocked(message *Message) error {
	if liveData.Browser != nil && liveData.Browser.CaptureError != "" {
		return fmt.Errorf("capture transport has already failed")
	}
	if !transferPattern.MatchString(message.TransferID) || !digestPattern.MatchString(message.SHA256) || message.TotalBytes < 1 || message.TotalBytes > activeCaptureLimits.requestBytes {
		return fmt.Errorf("invalid request transfer metadata or serialized request exceeds REP_CAPTURE_MAX_REQUEST_BYTES (%d)", activeCaptureLimits.requestBytes)
	}
	if len(message.Data) > base64.StdEncoding.EncodedLen(requestChunkBytes) {
		return fmt.Errorf("request chunk exceeds %d decoded bytes", requestChunkBytes)
	}
	chunk, err := base64.StdEncoding.Strict().DecodeString(message.Data)
	if err != nil || len(chunk) == 0 || len(chunk) > requestChunkBytes {
		return fmt.Errorf("invalid request chunk base64 or size")
	}
	transfer := requestTransfers[message.TransferID]
	if transfer == nil {
		if message.Sequence != 0 {
			return fmt.Errorf("request transfer must begin at sequence zero")
		}
		if len(requestTransfers) >= maxConcurrentRequestTransfers {
			return fmt.Errorf("too many incomplete request transfers")
		}
		var reserved int64
		for _, current := range requestTransfers {
			reserved += current.expectedBytes
		}
		if reserved+message.TotalBytes > activeCaptureLimits.snapshotBytes {
			return fmt.Errorf("pending request transfers exceed REP_CAPTURE_MAX_SNAPSHOT_BYTES (%d)", activeCaptureLimits.snapshotBytes)
		}
		captureSnapshots.Lock()
		dir := captureSnapshots.dir
		captureSnapshots.Unlock()
		if dir == "" {
			return fmt.Errorf("request transfer storage is unavailable")
		}
		file, err := os.CreateTemp(dir, ".request-*.partial")
		if err != nil {
			return fmt.Errorf("create request transfer file: %w", err)
		}
		transfer = &requestTransfer{file: file, digest: sha256.New(), expectedDigest: message.SHA256, expectedBytes: message.TotalBytes}
		requestTransfers[message.TransferID] = transfer
	}
	if transfer.next != message.Sequence || transfer.expectedBytes != message.TotalBytes || transfer.expectedDigest != message.SHA256 {
		return fmt.Errorf("request chunk sequence or metadata mismatch")
	}
	if transfer.bytes+int64(len(chunk)) > transfer.expectedBytes {
		return fmt.Errorf("request transfer contains more bytes than declared")
	}
	if _, err := transfer.file.Write(chunk); err != nil {
		return fmt.Errorf("write request transfer: %w", err)
	}
	_, _ = transfer.digest.Write(chunk)
	transfer.bytes += int64(len(chunk))
	transfer.next++
	return nil
}

func finishRequestTransferLocked(message *Message) error {
	if liveData.Browser != nil && liveData.Browser.CaptureError != "" {
		return fmt.Errorf("capture transport has already failed")
	}
	transfer := requestTransfers[message.TransferID]
	if transfer == nil {
		return fmt.Errorf("request end has no pending transfer")
	}
	defer func() {
		_ = transfer.file.Close()
		_ = os.Remove(transfer.file.Name())
		delete(requestTransfers, message.TransferID)
	}()
	if message.Chunks != transfer.next || message.TotalBytes != transfer.expectedBytes || transfer.bytes != transfer.expectedBytes || message.SHA256 != transfer.expectedDigest || hex.EncodeToString(transfer.digest.Sum(nil)) != transfer.expectedDigest {
		return fmt.Errorf("request transfer integrity mismatch: sequence, byte count, or SHA-256")
	}
	if _, err := transfer.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("read request transfer: %w", err)
	}
	decoder := json.NewDecoder(transfer.file)
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("request transfer contains invalid JSON")
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("request transfer contains trailing data")
	}
	if strings.TrimSpace(request.ID) == "" {
		return fmt.Errorf("request transfer has no request ID")
	}
	if shouldIgnoreRequestLocked(request) {
		return fmt.Errorf("request transfer source is not part of this capture")
	}
	upsertRequestLocked(request)
	return nil
}
