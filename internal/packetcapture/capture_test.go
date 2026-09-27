package packetcapture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func captureOptions(t testing.TB) Options {
	t.Helper()
	options := DefaultOptions()
	options.Interface = "lo0"
	options.Filter = "udp and src port 32123 and dst port 32124"
	options.Output = filepath.Join(t.TempDir(), "owned.pcap")
	return options
}

func TestCapturePrivateFinalArtifactsAndPendingMetadata(t *testing.T) {
	options := captureOptions(t)
	packet := ipv4UDP(rtpShape())
	payload := fixtureBytes(binary.LittleEndian, false, 101, []fixturePacket{{packet, uint32(len(packet)), 1, 0}})
	result, err := captureWith(context.Background(), options, func(_ context.Context, _ Options, file *os.File) (nativeResult, error) {
		pending, err := os.ReadFile(strings.TrimSuffix(options.Output, ".pcap") + ".metadata.json")
		if err != nil {
			t.Fatal(err)
		}
		var state Result
		if err := json.Unmarshal(pending, &state); err != nil {
			t.Fatal(err)
		}
		if state.Status != "pending" || state.Coverage.State != "unknown" {
			t.Fatalf("missing pending trial: %+v", state)
		}
		_, err = file.Write(payload)
		now := time.Now().UTC()
		return nativeResult{Link: linkType(101), Packets: 1, CapturedBytes: uint64(len(packet)), OriginalBytes: uint64(len(packet)), PCAPValid: true, StopReason: "duration_limit", ObservationStartedAt: &now, Stats: Statistics{Available: true}}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.Coverage.State != "unknown" || result.Artifact.Bytes != int64(len(payload)) || result.Packets != 1 || !result.Artifact.PCAPValid {
		t.Fatalf("bad capture result: %+v", result)
	}
	sum := sha256.Sum256(payload)
	if result.Artifact.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("artifact hash mismatch")
	}
	stored, err := os.ReadFile(result.Artifact.Path)
	if err != nil || !bytes.Equal(stored, payload) {
		t.Fatalf("artifact bytes changed: %v", err)
	}
	for _, path := range []string{result.Artifact.Path, result.Artifact.MetadataPath} {
		stat, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if stat.Mode().Perm() != 0600 {
			t.Fatalf("artifact is not private: %s %o", path, stat.Mode().Perm())
		}
	}
	metadata, err := os.ReadFile(result.Artifact.MetadataPath)
	if err != nil {
		t.Fatal(err)
	}
	var final Result
	if err := json.Unmarshal(metadata, &final); err != nil {
		t.Fatal(err)
	}
	if final.Artifact.SHA256 != result.Artifact.SHA256 || final.Status != "completed" {
		t.Fatalf("sidecar not finalized: %+v", final)
	}
}

func TestCapturePermissionFailureRetainsNoObservationEvidence(t *testing.T) {
	options := captureOptions(t)
	result, err := captureWith(context.Background(), options, func(context.Context, Options, *os.File) (nativeResult, error) {
		return nativeResult{Link: linkType(-1)}, ErrPermission
	})
	if !errors.Is(err, ErrPermission) || result.ErrorCode != "capture_permission_denied" || result.Status != "failed" || result.Coverage.State != "none" || result.Artifact.PCAPValid || result.ObservationStartedAt != nil || result.Artifact.Bytes != 0 || result.Stats.Available {
		t.Fatalf("setup failure appears observed: %+v %v", result, err)
	}
	if result.Artifact.SHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatal("empty failed artifact hash missing")
	}
	metadata, err := os.ReadFile(result.Artifact.MetadataPath)
	if err != nil || !json.Valid(metadata) {
		t.Fatalf("failed trial missing: %s %v", metadata, err)
	}
}

func TestCaptureFinalMetadataPreservesPendingDocumentForOpenReaders(t *testing.T) {
	options := captureOptions(t)
	var pending *os.File
	result, err := captureWith(context.Background(), options, func(context.Context, Options, *os.File) (nativeResult, error) {
		var err error
		pending, err = os.Open(strings.TrimSuffix(options.Output, ".pcap") + ".metadata.json")
		if err != nil {
			t.Fatal(err)
		}
		return nativeResult{StopReason: "setup_error"}, ErrPermission
	})
	if !errors.Is(err, ErrPermission) {
		t.Fatal(err)
	}
	defer pending.Close()
	data, err := io.ReadAll(pending)
	if err != nil {
		t.Fatal(err)
	}
	var original Result
	if err := json.Unmarshal(data, &original); err != nil || original.Status != "pending" {
		t.Fatalf("finalization modified the pending document in place: %s, %v", data, err)
	}
	data, err = os.ReadFile(result.Artifact.MetadataPath)
	if err != nil {
		t.Fatal(err)
	}
	var final Result
	if err := json.Unmarshal(data, &final); err != nil || final.Status != "failed" {
		t.Fatalf("final document was not published: %s, %v", data, err)
	}
}

func TestCaptureMetadataReplacementIsNotOverwritten(t *testing.T) {
	options := captureOptions(t)
	metadataPath := strings.TrimSuffix(options.Output, ".pcap") + ".metadata.json"
	_, err := captureWith(context.Background(), options, func(context.Context, Options, *os.File) (nativeResult, error) {
		if err := os.Rename(metadataPath, metadataPath+".moved"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(metadataPath, []byte("other owner"), 0600); err != nil {
			t.Fatal(err)
		}
		return nativeResult{StopReason: "setup_error"}, ErrPermission
	})
	if err == nil || !strings.Contains(err.Error(), "moved or replaced") {
		t.Fatalf("replacement did not stop finalization: %v", err)
	}
	data, err := os.ReadFile(metadataPath)
	if err != nil || string(data) != "other owner" {
		t.Fatalf("overwrote unrelated metadata: %s, %v", data, err)
	}
	temporary, err := filepath.Glob(filepath.Join(filepath.Dir(metadataPath), ".rep-packets-metadata-*.json"))
	if err != nil || len(temporary) != 0 {
		t.Fatalf("temporary metadata leaked: %v, %v", temporary, err)
	}
}

func TestCaptureNoClobberAndCancelledBeforeSetup(t *testing.T) {
	for _, conflict := range []string{"pcap", "metadata"} {
		t.Run(conflict, func(t *testing.T) {
			options := captureOptions(t)
			path := options.Output
			if conflict == "metadata" {
				path = strings.TrimSuffix(path, ".pcap") + ".metadata.json"
			}
			if err := os.WriteFile(path, []byte("preexisting"), 0600); err != nil {
				t.Fatal(err)
			}
			called := false
			_, err := captureWith(context.Background(), options, func(context.Context, Options, *os.File) (nativeResult, error) {
				called = true
				return nativeResult{}, nil
			})
			if err == nil || called {
				t.Fatal("capture ran despite output conflict")
			}
			stored, _ := os.ReadFile(path)
			if string(stored) != "preexisting" {
				t.Fatal("clobbered existing file")
			}
			if conflict == "metadata" {
				if _, err := os.Stat(options.Output); !os.IsNotExist(err) {
					t.Fatal("left orphan pcap after metadata collision")
				}
			}
		})
	}
	options := captureOptions(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := captureWith(ctx, options, func(context.Context, Options, *os.File) (nativeResult, error) {
		t.Fatal("ran after cancellation")
		return nativeResult{}, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(options.Output); !os.IsNotExist(err) {
		t.Fatal("cancelled setup created artifact")
	}
}

func TestCaptureLimitsAndCoverageNeverClaimWireCompleteness(t *testing.T) {
	valid := captureOptions(t)
	for _, change := range []func(*Options){func(o *Options) { o.Interface = " " }, func(o *Options) { o.Filter = "\t\n" }, func(o *Options) { o.Filter = "udp\x00bad" }, func(o *Options) { o.Output = "bad.json" }, func(o *Options) { o.Duration = 0 }, func(o *Options) { o.Duration = MaxDuration + 1 }, func(o *Options) { o.MaxPackets = 0 }, func(o *Options) { o.MaxPackets = MaxPackets + 1 }, func(o *Options) { o.MaxBytes = 23 }, func(o *Options) { o.MaxBytes = MaxBytes + 1 }, func(o *Options) { o.Snaplen = 0 }, func(o *Options) { o.Snaplen = MaxSnaplen + 1 }, func(o *Options) { o.BufferBytes = 65535 }, func(o *Options) { o.BufferBytes = MaxBufferBytes + 1 }} {
		bad := valid
		change(&bad)
		if err := Validate(bad); err == nil {
			t.Fatalf("accepted unbounded options: %+v", bad)
		}
	}
	now := time.Now()
	base := Result{Artifact: Artifact{PCAPValid: true}, ObservationStartedAt: &now, StopReason: "duration_limit", Stats: Statistics{Available: true}}
	if got := captureCoverage(base, nil); got.State != "unknown" {
		t.Fatalf("zero drops established completeness: %+v", got)
	}
	for _, reason := range []string{"packet_limit", "byte_limit", "cancelled"} {
		value := base
		value.StopReason = reason
		if got := captureCoverage(value, nil); got.State != "partial" || !strings.Contains(strings.Join(got.Reasons, ","), reason) {
			t.Fatalf("missing stop coverage: %+v", got)
		}
	}
	value := base
	value.TruncatedPackets = 1
	if got := captureCoverage(value, nil); got.State != "partial" {
		t.Fatalf("lost snaplen gap: %+v", got)
	}
	value = base
	value.Stats.Dropped = 1
	if got := captureCoverage(value, nil); got.State != "partial" {
		t.Fatalf("lost reported drops: %+v", got)
	}
}

func TestUnsupportedCaptureCreatesNoArtifacts(t *testing.T) {
	if Supported() {
		t.Skip("native backend available")
	}
	options := captureOptions(t)
	if _, err := Capture(context.Background(), options); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := Interfaces(); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := os.Stat(options.Output); !os.IsNotExist(err) {
		t.Fatal("unsupported capture created output")
	}
}
