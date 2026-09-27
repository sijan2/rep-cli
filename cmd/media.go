package cmd

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/repplus/rep-cli/internal/evidence"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

type mediaOptions struct {
	Saved, Save           string
	Info, RequireComplete bool
	MaxBytes              int
}

type mediaMetadata struct {
	MIMEType     string  `json:"mime_type"`
	MediaKind    string  `json:"media_kind"`
	Direction    string  `json:"direction"`
	TrackID      string  `json:"track_id,omitempty"`
	ParentPeerID string  `json:"parent_peer_id,omitempty"`
	TimeOriginMS float64 `json:"time_origin_ms,omitempty"`
}

type mediaAssembly struct {
	Metadata mediaMetadata
	Chunks   []store.StreamEvent
	Bytes    int64
	Complete bool
	Reasons  []string
}

func newMediaCommand() *cobra.Command {
	options := mediaOptions{MaxBytes: 8192}
	command := &cobra.Command{
		Use: "media <record-id>", Short: "Inspect or assemble a captured WebRTC audio/video recording",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (returnErr error) {
			if options.MaxBytes < 2048 || options.MaxBytes > 65536 {
				return fmt.Errorf("--max-bytes must be between 2048 and 65536")
			}
			if options.Info && options.Save != "" {
				return fmt.Errorf("--info cannot be combined with --save")
			}
			request, saved, err := findMediaRequest(args[0], options.Saved)
			if err != nil {
				return err
			}
			if request == nil {
				return fmt.Errorf("recording not found in this task or selected archive")
			}
			assembly, err := inspectMedia(request, options.RequireComplete)
			if err != nil {
				return err
			}
			result := mediaResult(request, assembly, saved)
			if options.Save == "" {
				if options.RequireComplete {
					if _, _, err := copyMedia(io.Discard, assembly.Chunks); err != nil {
						return err
					}
					result["payload_validation"] = "encoding_and_length_verified"
				}
				return writeMediaResult(cmd, result, options.MaxBytes)
			}
			path, err := filepath.Abs(options.Save)
			if err != nil {
				return err
			}
			// Reserve the final descriptor budget before creating an artifact.
			result["path"], result["artifact_bytes"], result["artifact_sha256"] = path, assembly.Bytes, strings.Repeat("0", 64)
			result["payload_validation"] = "encoding_and_length_verified"
			if _, err := encodeMediaResult(result, options.MaxBytes-256); err != nil {
				return err
			}
			record, err := beginBrowserEvidence("media.export", map[string]any{"record_id": request.ID, "saved": saved}, "", -1)
			if err != nil {
				return err
			}
			defer record.finishOnReturn(&returnErr)
			record.dispatch()
			size, digest, err := saveMedia(path, assembly.Chunks)
			if err != nil {
				return err
			}
			result["artifact_bytes"], result["artifact_sha256"] = size, digest
			coverage, reason := request.Stream.Capture.State, request.Stream.Capture.Reason
			if !assembly.Complete {
				coverage, reason = "partial", strings.Join(assembly.Reasons, ", ")
			}
			if err := record.artifact(path, "webrtc_media", coverage, reason,
				map[string]any{"record_id": request.ID, "saved": saved, "source": "browser_media_recorder", "payload_semantics": "reencoded_media", "scope": "recorded_media_interval", "mime_type": assembly.Metadata.MIMEType}); err != nil {
				return err
			}
			record.reference(evidence.Reference{Kind: "capture_record", ID: request.ID, Metadata: map[string]any{"saved": saved}})
			if err := record.finish(result, "completed", "unverified", "", nil); err != nil {
				return err
			}
			attachOperationEvidence(result, record)
			return writeMediaResult(cmd, result, options.MaxBytes)
		},
	}
	command.Flags().StringVar(&options.Saved, "saved", "", "Read one immutable archive (full hash, unique prefix, or latest)")
	command.Flags().BoolVar(&options.Info, "info", false, "Inspect recording metadata and chunk coverage")
	command.Flags().StringVar(&options.Save, "save", "", "Assemble exact ordered container chunks into a new private file")
	command.Flags().BoolVar(&options.RequireComplete, "require-complete", false, "Require a closed recording interval with all ordered chunks and valid payloads")
	command.Flags().IntVar(&options.MaxBytes, "max-bytes", 8192, "Maximum JSON output bytes (2048–65536)")
	return command
}

// Resolve aliases once, while reading the selected archive. A later capture
// must not change the identity stored with an export made using --saved latest.
func findMediaRequest(id, saved string) (*store.Request, string, error) {
	if saved == "" {
		request, err := findBodyWebRequest(id, "")
		return request, "", err
	}
	session, handled, err := store.LoadIndexedSession(saved)
	if !handled {
		var persistent *store.Store
		persistent, err = store.Load()
		if err == nil {
			if saved == "latest" || saved == "last" {
				session = persistent.GetLatestSession()
			} else {
				session, err = selectSummaryArchive(persistent.Sessions, saved)
			}
		}
	}
	if err != nil {
		return nil, "", err
	}
	if session == nil {
		return nil, "", fmt.Errorf("saved session not found in this task")
	}
	return store.BuildIndex(session.Requests).GetByAny(id), session.HashID, nil
}

func inspectMedia(request *store.Request, requireComplete bool) (mediaAssembly, error) {
	result := mediaAssembly{Reasons: []string{}}
	stream := request.Stream
	if request.RecordKind != "webrtc_media" || stream == nil || stream.Protocol != "webrtc_media" || stream.Version != 1 ||
		stream.Source != "browser_media_recorder" || stream.PayloadSemantics != "reencoded_media" || stream.Capture.Scope != "recorded_media_interval" {
		return result, fmt.Errorf("record is not a supported WebRTC media recording; inspect rep stream --info for its source and scope")
	}
	if err := validateStreamCoverage(stream); err != nil {
		return result, err
	}
	if err := json.Unmarshal(stream.Metadata, &result.Metadata); err != nil {
		return result, fmt.Errorf("invalid media metadata: %w", err)
	}
	m := result.Metadata
	if (m.MediaKind != "audio" && m.MediaKind != "video") || (m.Direction != "sent" && m.Direction != "received") || len(m.MIMEType) > 256 || len(m.TrackID) > 256 || len(m.ParentPeerID) > 256 {
		return result, fmt.Errorf("invalid media kind, direction, or metadata bounds")
	}
	addReason := func(reason string) {
		for _, existing := range result.Reasons {
			if existing == reason {
				return
			}
		}
		result.Reasons = append(result.Reasons, reason)
	}
	if stream.Capture.State != "complete" {
		addReason("capture_incomplete")
	}
	if stream.State != "closed" || len(stream.Events) == 0 || stream.Events[len(stream.Events)-1].Kind != "closed" {
		addReason("recording_not_finalized")
	}
	previous := int64(0)
	closed := false
	var final struct {
		Finalized bool   `json:"recorder_finalized"`
		Chunks    *int64 `json:"chunk_count"`
		Bytes     *int64 `json:"observed_media_bytes"`
	}
	for _, event := range stream.Events {
		if closed {
			addReason("events_after_recording_closed")
		}
		if event.Kind == "closed" {
			closed = true
			if len(event.Metadata) == 0 || json.Unmarshal(event.Metadata, &final) != nil {
				addReason("recording_finalization_unverified")
			}
		}
		if event.Kind == "gap" || event.Kind == "error" || event.Truncated || event.Error != "" {
			addReason("missing_or_incomplete_chunks")
		}
		if event.Kind != "chunk" {
			if event.Bytes != 0 || event.Payload != "" {
				return result, fmt.Errorf("media lifecycle event %d contains unexpected payload", event.Sequence)
			}
			continue
		}
		var chunk struct {
			Index    int64  `json:"chunk_index"`
			MIMEType string `json:"mime_type"`
		}
		if err := json.Unmarshal(event.Metadata, &chunk); err != nil || chunk.Index <= previous {
			return result, fmt.Errorf("media chunk %d has invalid or unordered chunk_index", event.Sequence)
		}
		if chunk.Index != previous+1 {
			addReason("missing_or_incomplete_chunks")
		}
		previous = chunk.Index
		if event.PayloadEncoding != "base64" {
			return result, fmt.Errorf("media chunk %d must contain binary base64 bytes", event.Sequence)
		}
		if m.MIMEType == "" {
			m.MIMEType = chunk.MIMEType
		}
		if len(chunk.MIMEType) > 256 || (chunk.MIMEType != "" && !strings.EqualFold(chunk.MIMEType, m.MIMEType)) {
			return result, fmt.Errorf("media chunk %d MIME type disagrees with the recording", event.Sequence)
		}
		result.Chunks = append(result.Chunks, event)
		result.Bytes += event.Bytes
	}
	if !closed || !final.Finalized || final.Chunks == nil || final.Bytes == nil {
		addReason("recording_finalization_unverified")
	} else {
		if *final.Chunks != previous || *final.Chunks != int64(len(result.Chunks)) {
			addReason("missing_or_incomplete_chunks")
		}
		if *final.Bytes != result.Bytes || *final.Bytes != stream.Capture.ObservedBytes {
			addReason("recording_byte_count_mismatch")
		}
	}
	result.Metadata = m
	if len(result.Chunks) == 0 || result.Bytes == 0 {
		addReason("no_media_bytes")
	}
	if !strings.HasPrefix(strings.ToLower(m.MIMEType), m.MediaKind+"/") {
		addReason("unknown_media_container")
	}
	result.Complete = len(result.Reasons) == 0
	if requireComplete && !result.Complete {
		return result, fmt.Errorf("media recording is incomplete: %s (capture reason: %s)", strings.Join(result.Reasons, ", "), stream.Capture.Reason)
	}
	return result, nil
}

func mediaResult(request *store.Request, assembly mediaAssembly, saved string) map[string]any {
	result := map[string]any{
		"id": request.ID, "protocol": "webrtc_media", "source": "browser_media_recorder",
		"payload_semantics": "reencoded_media", "state": request.Stream.State,
		"capture": request.Stream.Capture, "metadata": assembly.Metadata,
		"chunks": len(assembly.Chunks), "recorded_bytes": assembly.Bytes,
		"assembly_complete": assembly.Complete, "assembly_reasons": assembly.Reasons,
		"payload_validation": "not_checked", "container_playability": "not_verified",
	}
	if saved != "" {
		result["saved"] = saved
	}
	return result
}

// Concatenate MediaRecorder container segments. Individual timeslices are not
// standalone files. Decode one segment at a time into a reusable native buffer;
// never build a second full recording in memory or invoke a media decoder.
func copyMedia(destination io.Writer, chunks []store.StreamEvent) (int64, string, error) {
	digest := sha256.New()
	w := io.MultiWriter(destination, digest)
	buffer := make([]byte, 128<<10)
	var total int64
	for _, chunk := range chunks {
		reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(chunk.Payload))
		n, err := io.CopyBuffer(w, reader, buffer)
		if err != nil {
			return total, "", fmt.Errorf("media chunk %d: %w", chunk.Sequence, err)
		}
		if n != chunk.Bytes {
			return total, "", fmt.Errorf("media chunk %d byte count mismatch", chunk.Sequence)
		}
		total += n
	}
	return total, hex.EncodeToString(digest.Sum(nil)), nil
}

func saveMedia(path string, chunks []store.StreamEvent) (int64, string, error) {
	if len(chunks) == 0 {
		return 0, "", fmt.Errorf("no media chunks were captured")
	}
	if _, err := os.Lstat(path); err == nil {
		return 0, "", fmt.Errorf("output already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return 0, "", err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".rep-media-*")
	if err != nil {
		return 0, "", err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	buffer := bufio.NewWriterSize(file, 256<<10)
	size, digest, err := copyMedia(buffer, chunks)
	if err != nil {
		return 0, "", err
	}
	if err := buffer.Flush(); err != nil {
		return 0, "", err
	}
	if err := file.Sync(); err != nil {
		return 0, "", err
	}
	if err := file.Close(); err != nil {
		return 0, "", err
	}
	// Same-directory link publishes the sealed file atomically and cannot
	// overwrite an existing file or symlink, including one created after Lstat.
	if err := os.Link(file.Name(), path); err != nil {
		return 0, "", err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err == nil {
		err = dir.Sync()
		dir.Close()
	}
	if err != nil {
		return size, digest, fmt.Errorf("media saved at %s but directory sync failed: %w", path, err)
	}
	return size, digest, nil
}

func encodeMediaResult(result map[string]any, maxBytes int) ([]byte, error) {
	var payload any = result
	if forceEnvelope && !rawJSON {
		payload = output.WrapData("media", "capture_archive", result)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(data)+1 > maxBytes {
		return nil, fmt.Errorf("media metadata exceeds --max-bytes; increase the budget")
	}
	return append(data, '\n'), nil
}

func writeMediaResult(cmd *cobra.Command, result map[string]any, maxBytes int) error {
	data, err := encodeMediaResult(result, maxBytes)
	if err != nil {
		return err
	}
	_, err = cmd.OutOrStdout().Write(data)
	return err
}

func init() { rootCmd.AddCommand(newMediaCommand()) }
