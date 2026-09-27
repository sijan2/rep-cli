package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/packetcapture"
)

func runPacketCommand(t *testing.T, backend packetCommandBackend, args ...string) (string, error) {
	t.Helper()
	command := newPacketsCommandWith(backend, openScopedEvidence)
	command.SilenceUsage, command.SilenceErrors = true, true
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}

func packetTestScope(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	configureCaptureTestScope(t, "packets")
	previous := evidenceRunID
	evidenceRunID = ""
	t.Cleanup(func() { evidenceRunID = previous })
}

func packetFixtureFile(t *testing.T, path string, count int) []byte {
	t.Helper()
	var data bytes.Buffer
	for _, value := range []any{uint32(0xa1b2c3d4), uint16(2), uint16(4), uint32(0), uint32(0), uint32(65535), uint32(1)} {
		if err := binary.Write(&data, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < count; i++ {
		frame := make([]byte, 14+20+8+24)
		binary.BigEndian.PutUint16(frame[12:14], 0x0800)
		frame[14], frame[23] = 0x45, 17
		binary.BigEndian.PutUint16(frame[16:18], uint16(len(frame)-14))
		copy(frame[26:30], []byte{127, 0, 0, 1})
		copy(frame[30:34], []byte{127, 0, 0, 1})
		binary.BigEndian.PutUint16(frame[34:36], 40001)
		binary.BigEndian.PutUint16(frame[36:38], 40002)
		binary.BigEndian.PutUint16(frame[38:40], uint16(len(frame)-34))
		copy(frame[42:], "PRIVATE_PACKET_PAYLOAD")
		for _, value := range []uint32{uint32(1700000000 + i), 123456, uint32(len(frame)), uint32(len(frame))} {
			if err := binary.Write(&data, binary.LittleEndian, value); err != nil {
				t.Fatal(err)
			}
		}
		data.Write(frame)
	}
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestPacketsInterfacesDisclosesPermissionNotChecked(t *testing.T) {
	backend := packetCommandBackend{interfaces: func() ([]packetcapture.Interface, error) {
		return []packetcapture.Interface{{Name: "lo0", Loopback: true, Up: true}}, nil
	}}
	text, err := runPacketCommand(t, backend, "interfaces")
	if err != nil || !strings.Contains(text, `"capture_permission":"not_checked"`) || !strings.Contains(text, `"name":"lo0"`) {
		t.Fatalf("interface enumeration misreported capability: %s %v", text, err)
	}
	backend.interfaces = func() ([]packetcapture.Interface, error) { return nil, packetcapture.ErrUnsupported }
	text, err = runPacketCommand(t, backend, "interfaces")
	var agentError output.AgentError
	if !errors.As(err, &agentError) || agentError.Code != "packet_capture_unsupported" || text != "" {
		t.Fatalf("unsupported backend lacks stable error: %s %+v", text, err)
	}
}

func TestPacketsCaptureRejectsInvalidBoundsBeforeDispatch(t *testing.T) {
	packetTestScope(t)
	called := false
	backend := packetCommandBackend{capture: func(context.Context, packetcapture.Options) (packetcapture.Result, error) {
		called = true
		return packetcapture.Result{}, nil
	}}
	base := []string{"capture", "--interface", "lo0", "--filter", "udp and port 8443", "--output", filepath.Join(t.TempDir(), "capture.pcap")}
	for _, flags := range [][]string{{"--interface", ""}, {"--filter", ""}, {"--duration", "0s"}, {"--max-packets", "0"}, {"--max-bytes", "23"},
		{"--max-bytes", "1073741825"}, {"--snaplen", "0"}, {"--buffer-bytes", "1"}, {"--output", "capture.pcapng"}} {
		args := append(append([]string{}, base...), flags...)
		if _, err := runPacketCommand(t, backend, args...); err == nil || called {
			t.Fatalf("invalid flags dispatched capture: %v, called=%v, err=%v", flags, called, err)
		}
	}
}

func packetCaptureFixture(t *testing.T, options packetcapture.Options, outcome string) packetcapture.Result {
	t.Helper()
	var data []byte
	if outcome == "permission" {
		if err := os.WriteFile(options.Output, nil, 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		data = packetFixtureFile(t, options.Output, 2)
	}
	digest := sha256.Sum256(data)
	result := packetcapture.Result{Version: 1, Collector: "fixture-native", BackendVersion: "fixture-pcap", Interface: options.Interface, Filter: options.Filter,
		Status: "completed", StopReason: "duration", Packets: 2,
		Coverage: packetcapture.Coverage{State: "unknown", Scope: "filtered_interface_observation", Reasons: []string{"wire_completeness_not_established"}},
		Artifact: packetcapture.Artifact{Path: options.Output, MetadataPath: strings.TrimSuffix(options.Output, ".pcap") + ".metadata.json", Bytes: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), PCAPValid: true}}
	if outcome == "permission" {
		result.Status, result.StopReason, result.Packets = "failed", "setup_error", 0
		result.Error, result.ErrorCode = "fixture BPF access denied", "capture_permission_denied"
		result.Coverage.State, result.Coverage.Reasons = "none", []string{"observation_not_started"}
		result.Artifact.PCAPValid = false
	} else if outcome == "cancelled" {
		result.Status, result.StopReason = "cancelled", "cancelled"
		result.Coverage.State, result.Coverage.Reasons = "partial", []string{"cancelled"}
	}
	metadata, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result.Artifact.MetadataPath, metadata, 0600); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPacketsCaptureRecordsArtifactsAndTermination(t *testing.T) {
	for _, outcome := range []string{"completed", "permission", "cancelled", "finalization"} {
		t.Run(outcome, func(t *testing.T) {
			store, run := operationEvidenceFixture(t)
			backend := packetCommandBackend{capture: func(ctx context.Context, options packetcapture.Options) (packetcapture.Result, error) {
				if ctx.Done() == nil || options.Duration != 25*time.Millisecond || options.MaxPackets != 19 || options.MaxBytes != 8192 || options.Snaplen != 1500 || options.BufferBytes != 65536 {
					t.Fatalf("capture flags/cancellation lost at native boundary: %+v", options)
				}
				result := packetCaptureFixture(t, options, outcome)
				switch outcome {
				case "permission":
					return result, fmt.Errorf("fixture access denied: %w", packetcapture.ErrPermission)
				case "finalization":
					return result, errors.New("fixture metadata finalization failed")
				default:
					return result, nil
				}
			}}
			text, err := runPacketCommand(t, backend, "capture", "--interface", "lo0", "--filter", "udp and port 8443", "--output", filepath.Join(t.TempDir(), "capture.pcap"),
				"--duration", "25ms", "--max-packets", "19", "--max-bytes", "8192", "--snaplen", "1500", "--buffer-bytes", "65536")
			failed := outcome != "completed"
			if (err != nil) != failed || (failed && !output.Reported(err)) {
				t.Fatalf("capture result/exit diverged: %s %v", text, err)
			}
			var result packetCaptureOutput
			if err := json.Unmarshal([]byte(text), &result); err != nil || result.Evidence == nil || result.Evidence.RunID != run.ID || (result.CommandError != nil) != failed {
				t.Fatalf("capture did not emit one linked, honest result: %s %v", text, err)
			}
			page, err := store.ListOperations(run.ID, "", 20)
			if err != nil || len(page.Operations) != 2 || page.Operations[0].Status != "pending" {
				t.Fatalf("pending operation lost: %+v %v", page, err)
			}
			finished := page.Operations[1]
			wantStatus := "completed"
			if failed {
				wantStatus = "failed"
			}
			if finished.Kind != "packets.capture" || finished.Status != wantStatus || finished.Verification != "unverified" || len(finished.ArtifactIDs) != 2 {
				t.Fatalf("capture outcome or artifacts lost: %+v", finished)
			}
			for _, id := range finished.ArtifactIDs {
				artifact, err := store.LoadArtifact(run.ID, id)
				if err != nil || artifact.Collector != "fixture-native" || artifact.Coverage.State != result.Coverage.State || artifact.Metadata["scope"] != "filtered_interface_observation" {
					t.Fatalf("artifact provenance lost: %+v %v", artifact, err)
				}
			}
			if outcome == "cancelled" && (result.Status != "cancelled" || result.StopReason != "cancelled" || result.CommandError.Code != "capture_cancelled" || result.Coverage.State != "partial" || !bytes.Contains(finished.Result, []byte(`"status":"cancelled"`))) {
				t.Fatalf("cancellation became a setup failure: %+v", result)
			}
			if outcome == "permission" && (result.Artifact.Bytes != 0 || result.Artifact.PCAPValid || result.Coverage.State != "none" || result.Stats.Available) {
				t.Fatalf("failed setup became a packet observation: %+v", result)
			}
		})
	}
}

func TestPacketsInspectBudgetPreservesCursorAndPayloadBoundary(t *testing.T) {
	packetTestScope(t)
	path := filepath.Join(t.TempDir(), "fixture.pcap")
	data := packetFixtureFile(t, path, 27)
	backend := packetCommandBackend{inspect: packetcapture.Inspect}
	offset, seen, pages := int64(24), 0, 0
	for {
		text, err := runPacketCommand(t, backend, "inspect", path, "--offset", fmt.Sprint(offset), "--limit", "20", "--max-bytes", "2048")
		if err != nil {
			t.Fatal(err)
		}
		if len(text) > 2048 || strings.Contains(text, "PRIVATE_PACKET_PAYLOAD") {
			t.Fatalf("inspection exceeded output/payload boundary: %s", text)
		}
		var result struct {
			packetcapture.Inspection
			OutputOmittedPackets int `json:"output_omitted_packets"`
		}
		if err := json.Unmarshal([]byte(text), &result); err != nil {
			t.Fatal(err)
		}
		if !result.PayloadOmitted || result.NextOffset <= offset || len(result.Packets) == 0 || result.Packets[0].Offset != offset || pages > 27 {
			t.Fatalf("inspection cursor failed: %+v", result)
		}
		for _, packet := range result.Packets {
			if packet.Offset != offset || packet.UDP == nil || packet.UDP.DestinationPort != 40002 {
				t.Fatalf("ordered UDP evidence lost: %+v", packet)
			}
			offset = packet.NextOffset
			seen++
		}
		if result.NextOffset != offset || (result.OutputOmittedPackets > 0 && !result.HasMore) {
			t.Fatalf("output budgeting skipped descriptors: %+v", result)
		}
		pages++
		if !result.HasMore {
			break
		}
	}
	if seen != 27 || offset != int64(len(data)) || pages < 2 {
		t.Fatalf("inspection skipped/repeated packets: seen=%d offset=%d pages=%d", seen, offset, pages)
	}
}

func TestPacketsInspectionRejectsNoProgressBudgetAndInvalidFlags(t *testing.T) {
	packetTestScope(t)
	backend := packetCommandBackend{inspect: func(string, packetcapture.InspectOptions) (packetcapture.Inspection, error) {
		return packetcapture.Inspection{Offset: 24, NextOffset: 80, Packets: []packetcapture.Packet{{Offset: 24, NextOffset: 80, Notes: []string{strings.Repeat("x", 4096)}}}}, nil
	}}
	if text, err := runPacketCommand(t, backend, "inspect", "fixture.pcap", "--max-bytes", "2048"); err == nil || text != "" {
		t.Fatalf("overlarge descriptor advanced an empty page: %s %v", text, err)
	}
	called := false
	backend.inspect = func(string, packetcapture.InspectOptions) (packetcapture.Inspection, error) {
		called = true
		return packetcapture.Inspection{}, nil
	}
	for _, flags := range [][]string{{"--offset", "0"}, {"--limit", "0"}, {"--limit", "1001"}, {"--max-scan-bytes", "0"}, {"--max-scan-bytes", "67108865"}, {"--max-bytes", "1023"}} {
		if _, err := runPacketCommand(t, backend, append([]string{"inspect", "fixture.pcap"}, flags...)...); err == nil || called {
			t.Fatalf("invalid inspection flags reached backend: %v %v", flags, err)
		}
	}
}

func TestPacketsInvalidEvidenceRunPreventsCapture(t *testing.T) {
	packetTestScope(t)
	evidenceRunID = "missing-run"
	called := false
	backend := packetCommandBackend{capture: func(context.Context, packetcapture.Options) (packetcapture.Result, error) {
		called = true
		return packetcapture.Result{}, nil
	}}
	text, err := runPacketCommand(t, backend, "capture", "--interface", "lo0", "--filter", "udp and port 8443", "--output", filepath.Join(t.TempDir(), "capture.pcap"))
	if err == nil || called || text != "" {
		t.Fatalf("invalid run dispatched observation: called=%v %s %v", called, text, err)
	}
}
