package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/repplus/rep-cli/internal/evidence"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/packetcapture"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

const packetResultBudget = 128 << 10

type packetCommandBackend struct {
	interfaces func() ([]packetcapture.Interface, error)
	capture    func(context.Context, packetcapture.Options) (packetcapture.Result, error)
	inspect    func(string, packetcapture.InspectOptions) (packetcapture.Inspection, error)
}

func newPacketsCommand() *cobra.Command {
	return newPacketsCommandWith(packetCommandBackend{packetcapture.Interfaces, packetcapture.Capture, packetcapture.Inspect}, openScopedEvidence)
}

func newPacketsCommandWith(backend packetCommandBackend, open evidenceOpener) *cobra.Command {
	command := &cobra.Command{Use: "packets", Short: "Collect filtered native packets or inspect bounded pcap headers", Long: "Passive native diagnostics with explicit interface, filter and resource bounds. Live capture requires macOS, cgo/libpcap and access to a BPF capture device. Output is bounded JSON. Read rep describe packets for coverage and encryption limits."}
	command.AddCommand(&cobra.Command{Use: "interfaces", Short: "List native capture interfaces without opening a capture", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		interfaces, err := backend.interfaces()
		if err != nil {
			return packetCommandError(cmd, err)
		}
		if interfaces == nil {
			interfaces = []packetcapture.Interface{}
		}
		return writePacketJSON(cmd, map[string]any{"interfaces": interfaces, "capture_permission": "not_checked"}, packetResultBudget)
	}})
	options := packetcapture.DefaultOptions()
	capture := &cobra.Command{Use: "capture", Short: "Write a bounded filtered capture and metadata to new private files", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if err := requirePacketScope(); err != nil {
			return err
		}
		if err := packetcapture.Validate(options); err != nil {
			return err
		}
		journal, err := beginPacketEvidence(open, options)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		result, captureErr := backend.capture(ctx, options)
		if captureErr == nil && result.Status == "cancelled" {
			captureErr = context.Canceled
		}
		if captureErr == nil && result.Status == "failed" {
			captureErr = errors.New("native packet capture failed: " + result.Error)
		}
		response := packetCaptureOutput{Result: result}
		if journal != nil {
			response.Evidence = journal.reference()
			if err := journal.finish(result, captureErr); err != nil {
				response.EvidenceError = err.Error()
				captureErr = errors.Join(captureErr, err)
			}
			response.Evidence = journal.reference()
		}
		if result.Version == 0 {
			return packetCommandError(cmd, captureErr)
		}
		if captureErr != nil {
			code := result.ErrorCode
			if code == "" {
				code = "packet_capture_failed"
			}
			if errors.Is(captureErr, context.Canceled) {
				code = "capture_cancelled"
			} else if response.EvidenceError != "" && result.ErrorCode == "" {
				code = "packet_evidence_failed"
			}
			message := captureErr.Error()
			if len(message) > 4096 {
				message = message[:4096]
			}
			failure := output.NewAgentError(code, cmd.CommandPath(), message)
			response.CommandError = &failure
		}
		if err := writePacketJSON(cmd, response, packetResultBudget); err != nil {
			return errors.Join(captureErr, err)
		}
		if captureErr != nil {
			return output.MarkReported(*response.CommandError)
		}
		return nil
	}}
	capture.Flags().StringVar(&options.Interface, "interface", "", "Explicit native interface name (required)")
	capture.Flags().StringVar(&options.Filter, "filter", "", "Explicit nonempty BPF filter (required)")
	capture.Flags().StringVar(&options.Output, "output", "", "New .pcap file; matching .metadata.json is also created (required)")
	capture.Flags().DurationVar(&options.Duration, "duration", options.Duration, "Observation duration (1ms to 1h)")
	capture.Flags().Uint64Var(&options.MaxPackets, "max-packets", options.MaxPackets, "Maximum packets written (1 to 10000000)")
	capture.Flags().Uint64Var(&options.MaxBytes, "max-bytes", options.MaxBytes, "Maximum pcap bytes including file and record headers (24 to 1073741824)")
	capture.Flags().IntVar(&options.Snaplen, "snaplen", options.Snaplen, "Maximum captured bytes per packet (1 to 262144)")
	capture.Flags().IntVar(&options.BufferBytes, "buffer-bytes", options.BufferBytes, "Requested kernel capture buffer bytes (65536 to 67108864)")
	command.AddCommand(capture)
	inspectOptions := packetcapture.InspectOptions{Offset: 24, Limit: 20, MaxScanBytes: 1 << 20}
	maxBytes := 8192
	inspect := &cobra.Command{Use: "inspect FILE.pcap", Short: "Page through offline packet headers and unverified protocol candidates", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := requirePacketScope(); err != nil {
			return err
		}
		if maxBytes < 1024 || maxBytes > 256<<10 {
			return fmt.Errorf("--max-bytes must be between 1024 and 262144 for inspection output")
		}
		if inspectOptions.Offset < 24 || inspectOptions.Limit < 1 || inspectOptions.Limit > packetcapture.MaxInspectPackets || inspectOptions.MaxScanBytes < 16 || inspectOptions.MaxScanBytes > packetcapture.MaxInspectScanBytes {
			return fmt.Errorf("--offset must be a record boundary >=24, --limit must be 1..%d, and --max-scan-bytes must be 16..%d", packetcapture.MaxInspectPackets, packetcapture.MaxInspectScanBytes)
		}
		result, err := backend.inspect(args[0], inspectOptions)
		if err != nil {
			return err
		}
		data, err := encodePacketInspection(result, maxBytes)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(data)
		return err
	}}
	inspect.Flags().Int64Var(&inspectOptions.Offset, "offset", inspectOptions.Offset, "Pcap record byte boundary from a previous next_offset (default 24)")
	inspect.Flags().IntVar(&inspectOptions.Limit, "limit", inspectOptions.Limit, "Maximum packet descriptors (1 to 1000)")
	inspect.Flags().Int64Var(&inspectOptions.MaxScanBytes, "max-scan-bytes", inspectOptions.MaxScanBytes, "Maximum record bytes traversed per page (16 to 67108864)")
	inspect.Flags().IntVar(&maxBytes, "max-bytes", maxBytes, "Maximum JSON output bytes (1024 to 262144)")
	command.AddCommand(inspect)
	return command
}

func requirePacketScope() error {
	selected, err := scope.Current()
	if err != nil {
		return err
	}
	if !selected.Scoped || selected.Task == "" || selected.TaskSource == "default" {
		return fmt.Errorf("packets capture/inspect requires an explicit workspace and task; use --workspace PROJECT --task TASK")
	}
	return nil
}

func packetCommandError(cmd *cobra.Command, err error) error {
	if err == nil {
		return errors.New("packet capture did not return a result")
	}
	if errors.Is(err, packetcapture.ErrUnsupported) {
		return output.NewAgentError("packet_capture_unsupported", cmd.CommandPath(), err.Error(), "offline packets inspect remains available on this build")
	}
	if errors.Is(err, packetcapture.ErrPermission) {
		return output.NewAgentError("capture_permission_denied", cmd.CommandPath(), err.Error())
	}
	return err
}

func writePacketJSON(cmd *cobra.Command, result any, budget int) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if len(data)+1 > budget {
		return fmt.Errorf("packet result exceeds bounded output budget %d", budget)
	}
	_, err = cmd.OutOrStdout().Write(append(data, '\n'))
	return err
}

// If the JSON budget shortens a descriptor page, rewind the cursor to the last
// descriptor actually returned. No omitted record is silently consumed.
func encodePacketInspection(result packetcapture.Inspection, budget int) ([]byte, error) {
	encode := func(count int) ([]byte, error) {
		page := result
		page.Packets = result.Packets[:count]
		page.NextOffset = page.Offset
		if count > 0 {
			page.NextOffset = page.Packets[count-1].NextOffset
		}
		page.ScannedBytes = page.NextOffset - page.Offset
		page.HasMore = page.NextOffset < page.FileBytes
		data, err := json.Marshal(struct {
			packetcapture.Inspection
			OutputOmittedPackets int `json:"output_omitted_packets"`
		}{page, len(result.Packets) - count})
		return append(data, '\n'), err
	}
	data, err := encode(len(result.Packets))
	if err != nil {
		return nil, err
	}
	if len(data) <= budget {
		return data, nil
	}
	low, high := 0, len(result.Packets)
	for low < high {
		middle := (low + high + 1) / 2
		data, err = encode(middle)
		if err != nil {
			return nil, err
		}
		if len(data) <= budget {
			low = middle
		} else {
			high = middle - 1
		}
	}
	if low == 0 {
		return nil, fmt.Errorf("one packet descriptor plus metadata exceeds --max-bytes; increase the budget")
	}
	return encode(low)
}

type packetEvidenceRef struct {
	RunID        string   `json:"run_id"`
	OperationID  string   `json:"operation_id"`
	CompletionID string   `json:"completion_id,omitempty"`
	ArtifactIDs  []string `json:"artifact_ids,omitempty"`
}
type packetCaptureOutput struct {
	packetcapture.Result
	Evidence      *packetEvidenceRef `json:"evidence,omitempty"`
	EvidenceError string             `json:"evidence_error,omitempty"`
	CommandError  *output.AgentError `json:"command_error,omitempty"`
}
type packetEvidence struct {
	store      *evidence.Store
	runID      string
	pending    evidence.Operation
	completion string
	artifacts  []string
}

func beginPacketEvidence(open evidenceOpener, options packetcapture.Options) (*packetEvidence, error) {
	if evidenceRunID == "" {
		return nil, nil
	}
	s, identity, err := open()
	if err != nil {
		return nil, err
	}
	metadata := map[string]any{"collector": "rep-libpcap", "cli_version": Version, "workspace": identity["workspace"], "task": identity["task"], "duration_ms": options.Duration.Milliseconds()}
	pending, err := s.BeginOperation(evidenceRunID, evidence.Operation{Kind: "packets.capture", Input: compactEvidenceJSON(options), Metadata: metadata})
	if err != nil {
		return nil, fmt.Errorf("record pending packet operation before capture: %w", err)
	}
	return &packetEvidence{store: s, runID: evidenceRunID, pending: pending}, nil
}

func (record *packetEvidence) reference() *packetEvidenceRef {
	return &packetEvidenceRef{RunID: record.runID, OperationID: record.pending.ID, CompletionID: record.completion, ArtifactIDs: record.artifacts}
}

func (record *packetEvidence) finish(result packetcapture.Result, captureErr error) error {
	var evidenceErr error
	if result.Artifact.Path != "" {
		for _, item := range []struct{ path, kind string }{{result.Artifact.Path, "pcap"}, {result.Artifact.MetadataPath, "pcap_metadata"}} {
			artifact, err := record.store.ImportArtifact(record.runID, evidence.ImportSpec{Path: item.path, Kind: item.kind, Collector: result.Collector, Build: Version, Trial: record.pending.ID,
				Coverage: evidence.Coverage{State: result.Coverage.State, Reason: strings.Join(result.Coverage.Reasons, ",")}, Metadata: map[string]any{"scope": result.Coverage.Scope, "interface": result.Interface, "pcap_valid": result.Artifact.PCAPValid, "capture_status": result.Status, "stop_reason": result.StopReason, "original_pcap_sha256": result.Artifact.SHA256}})
			if err != nil {
				evidenceErr = errors.Join(evidenceErr, fmt.Errorf("retain %s artifact: %w", item.kind, err))
				continue
			}
			record.artifacts = append(record.artifacts, artifact.ID)
			if item.kind == "pcap" && result.Artifact.SHA256 != "" && artifact.SHA256 != result.Artifact.SHA256 {
				evidenceErr = errors.Join(evidenceErr, errors.New("pcap bytes changed between collection and evidence import"))
			}
		}
	}
	status := evidence.StatusCompleted
	if captureErr != nil || evidenceErr != nil {
		status = evidence.StatusFailed
	} else if result.Status == "cancelled" {
		status = evidence.StatusUnknown
	}
	message := ""
	if err := errors.Join(captureErr, evidenceErr); err != nil {
		message = err.Error()
		if len(message) > 4096 {
			message = message[:4096]
		}
	}
	finished, err := record.store.FinishOperation(record.runID, record.pending.ID, evidence.Operation{Status: status, Result: compactEvidenceJSON(result), ArtifactIDs: record.artifacts, Error: message, Verification: "unverified"})
	if err != nil {
		return errors.Join(evidenceErr, fmt.Errorf("capture may have run; completion could not be recorded and pending operation remains: %w", err))
	}
	record.completion = finished.ID
	return evidenceErr
}

func init() { rootCmd.AddCommand(newPacketsCommand()) }
