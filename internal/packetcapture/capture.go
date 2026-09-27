package packetcapture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type nativeResult struct {
	Link                                                    LinkType
	Packets, CapturedBytes, OriginalBytes, TruncatedPackets uint64
	Stats                                                   Statistics
	StopReason                                              string
	PCAPValid                                               bool
	ObservationStartedAt                                    *time.Time
	Warnings                                                []string
}

func Validate(options Options) error {
	if strings.TrimSpace(options.Interface) == "" || len(options.Interface) > 256 || strings.ContainsRune(options.Interface, 0) {
		return errors.New("--interface must name one explicit capture interface (up to 256 bytes)")
	}
	if strings.TrimSpace(options.Filter) == "" || len(options.Filter) > 4096 || strings.ContainsRune(options.Filter, 0) {
		return errors.New("--filter must contain an explicit nonempty BPF expression (up to 4096 bytes)")
	}
	if options.Output == "" || !strings.HasSuffix(options.Output, ".pcap") || len(options.Output) > 4096 {
		return errors.New("--output must name a new .pcap file")
	}
	if options.Duration < time.Millisecond || options.Duration > MaxDuration {
		return errors.New("--duration must be between 1ms and 1h")
	}
	if options.MaxPackets == 0 || options.MaxPackets > MaxPackets || options.MaxBytes < 24 || options.MaxBytes > MaxBytes {
		return fmt.Errorf("--max-packets must be 1..%d and --max-bytes must be 24..%d (including pcap headers)", MaxPackets, MaxBytes)
	}
	if options.Snaplen < 1 || options.Snaplen > MaxSnaplen || options.BufferBytes < 65536 || options.BufferBytes > MaxBufferBytes {
		return fmt.Errorf("--snaplen must be 1..%d and --buffer-bytes must be 65536..%d", MaxSnaplen, MaxBufferBytes)
	}
	return nil
}

// Capture writes original captured packet bytes directly from libpcap in C. A
// failed native setup retains a private empty .pcap and a failure sidecar; it is
// explicitly marked pcap_valid=false. Existing files are never overwritten.
func Capture(ctx context.Context, options Options) (Result, error) {
	if err := Validate(options); err != nil {
		return Result{}, err
	}
	if !Supported() {
		return Result{}, ErrUnsupported
	}
	return captureWith(ctx, options, captureNative)
}

func captureWith(ctx context.Context, options Options, run func(context.Context, Options, *os.File) (nativeResult, error)) (Result, error) {
	if err := Validate(options); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	path, err := filepath.Abs(options.Output)
	if err != nil {
		return Result{}, err
	}
	metadataPath := strings.TrimSuffix(path, ".pcap") + ".metadata.json"
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return Result{}, fmt.Errorf("create private pcap without overwriting: %w", err)
	}
	defer file.Close()
	metadata, err := os.OpenFile(metadataPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		removeOwnedEmpty(file, path)
		return Result{}, fmt.Errorf("create private metadata without overwriting: %w", err)
	}
	defer metadata.Close()
	started := time.Now()
	result := Result{Version: 1, Collector: "rep-libpcap", BackendVersion: backendVersion(), Interface: options.Interface, Filter: options.Filter,
		StartedAt: started.UTC(), Status: "pending", StopReason: "pending", Datalink: LinkType{ID: -1, Name: "unknown"},
		Limits:   Limits{DurationMS: options.Duration.Milliseconds(), MaxPackets: options.MaxPackets, MaxBytes: options.MaxBytes, Snaplen: options.Snaplen, BufferBytes: options.BufferBytes},
		Artifact: Artifact{Path: path, MetadataPath: metadataPath}}
	result.Coverage = Coverage{State: "unknown", Scope: "filtered_interface_observation", Reasons: []string{"pending_capture_outcome"}, Limitations: []string{"interrupted_process_may_leave_unfinalized_artifacts"}}
	if err := json.NewEncoder(metadata).Encode(result); err != nil {
		return result, fmt.Errorf("write pending capture metadata: %w", err)
	}
	if err := metadata.Sync(); err != nil {
		return result, fmt.Errorf("sync pending capture metadata: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return result, err
	}
	observation, captureErr := run(ctx, options, file)
	result.Datalink, result.Packets = observation.Link, observation.Packets
	result.CapturedBytes, result.OriginalBytes, result.TruncatedPackets = observation.CapturedBytes, observation.OriginalBytes, observation.TruncatedPackets
	result.Stats, result.StopReason, result.Artifact.PCAPValid = observation.Stats, observation.StopReason, observation.PCAPValid
	result.ObservationStartedAt = observation.ObservationStartedAt
	result.Warnings = observation.Warnings
	result.StoppedAt, result.DurationMS = time.Now().UTC(), time.Since(started).Milliseconds()
	result.Stats.Interpretation = "libpcap ps_recv may include unfiltered or unread packets depending on platform; zero ps_drop may mean unsupported; zero ps_ifdrop does not prove no interface drops. Counters are not transmitted totals."
	if result.StopReason == "" {
		result.StopReason = "setup_error"
	}
	if captureErr == nil {
		result.Status = "completed"
	}
	if result.StopReason == "cancelled" {
		result.Status = "cancelled"
	}
	if syncErr := file.Sync(); syncErr != nil {
		captureErr = errors.Join(captureErr, fmt.Errorf("sync pcap: %w", syncErr))
		result.StopReason = "write_error"
	}
	if _, err = file.Seek(0, io.SeekStart); err == nil {
		hash := sha256.New()
		result.Artifact.Bytes, err = io.Copy(hash, file)
		if err == nil {
			result.Artifact.SHA256 = hex.EncodeToString(hash.Sum(nil))
		}
	}
	if err != nil {
		captureErr = errors.Join(captureErr, fmt.Errorf("hash pcap artifact: %w", err))
	}
	result.Coverage = captureCoverage(result, captureErr)
	if captureErr != nil {
		result.Status, result.ErrorCode = "failed", "capture_failed"
		if errors.Is(captureErr, ErrPermission) {
			result.ErrorCode = "capture_permission_denied"
		}
		result.Error = boundedError(captureErr.Error())
	}
	if err := finalizeMetadata(metadata, metadataPath, result); err != nil {
		return result, errors.Join(captureErr, err)
	}
	// Persist both newly created directory entries where the platform supports it.
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return result, errors.Join(captureErr, err)
	}
	return result, captureErr
}

// Preserve the complete pending document until the final document is durable.
// A process interruption leaves either valid pending metadata or valid final
// metadata, rather than an in-place truncated/partly written JSON document.
func finalizeMetadata(pending *os.File, path string, result Result) error {
	final, err := os.CreateTemp(filepath.Dir(path), ".rep-packets-metadata-*.json")
	if err != nil {
		return fmt.Errorf("create final capture metadata: %w", err)
	}
	defer os.Remove(final.Name())
	defer final.Close()
	if err := json.NewEncoder(final).Encode(result); err != nil {
		return fmt.Errorf("write final capture metadata: %w", err)
	}
	if err := final.Sync(); err != nil {
		return fmt.Errorf("sync final capture metadata: %w", err)
	}
	if err := final.Close(); err != nil {
		return fmt.Errorf("close final capture metadata: %w", err)
	}
	own, err := pending.Stat()
	if err != nil {
		return fmt.Errorf("check pending capture metadata: %w", err)
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(own, current) {
		return errors.New("pending capture metadata was moved or replaced; refusing to replace the current path")
	}
	if err := os.Rename(final.Name(), path); err != nil {
		return fmt.Errorf("publish final capture metadata: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	parent, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open artifact directory for sync: %w", err)
	}
	defer parent.Close()
	if err := parent.Sync(); err != nil {
		return fmt.Errorf("sync artifact directory: %w", err)
	}
	return nil
}

func captureCoverage(result Result, captureErr error) Coverage {
	coverage := Coverage{State: "unknown", Scope: "filtered_interface_observation", Reasons: []string{}, Limitations: []string{
		"filter_and_interface_define_observation_scope", "os_capture_point_can_reflect_offloads_and_link_header_transformations",
		"zero_reported_drops_do_not_establish_wire_completeness", "encrypted_bytes_remain_encrypted_no_decryption", "packet_timestamps_are_host_capture_timestamps",
	}}
	if captureErr != nil {
		coverage.Reasons = append(coverage.Reasons, "capture_failed")
	}
	if result.StopReason == "packet_limit" || result.StopReason == "byte_limit" || result.StopReason == "cancelled" {
		coverage.Reasons = append(coverage.Reasons, result.StopReason)
	}
	if result.TruncatedPackets > 0 {
		coverage.Reasons = append(coverage.Reasons, "snaplen_truncation")
	}
	if result.Stats.Dropped > 0 || result.Stats.InterfaceDropped > 0 {
		coverage.Reasons = append(coverage.Reasons, "reported_packet_drops")
	}
	if len(coverage.Reasons) > 0 {
		coverage.State = "partial"
	} else {
		coverage.Reasons = append(coverage.Reasons, "wire_completeness_not_established")
	}
	if !result.Stats.Available {
		coverage.Reasons = append(coverage.Reasons, "capture_statistics_unavailable")
	}
	if !result.Artifact.PCAPValid && result.ObservationStartedAt == nil {
		coverage.State = "none"
		coverage.Reasons = append(coverage.Reasons, "observation_not_started")
	}
	return coverage
}

func boundedError(value string) string {
	if len(value) > 2048 {
		return value[:2048]
	}
	return value
}

func removeOwnedEmpty(file *os.File, path string) {
	own, err := file.Stat()
	if err != nil || own.Size() != 0 {
		return
	}
	current, err := os.Lstat(path)
	if err == nil && os.SameFile(own, current) {
		_ = os.Remove(path)
	}
}
