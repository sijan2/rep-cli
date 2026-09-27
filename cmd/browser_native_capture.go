package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/repplus/rep-cli/internal/browserdiagnostics"
	"github.com/repplus/rep-cli/internal/evidence"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/spf13/cobra"
)

type nativeBrowserCaptureOutput struct {
	browserdiagnostics.Result
	Evidence     *operationEvidenceRef `json:"evidence,omitempty"`
	CommandError *output.AgentError    `json:"command_error,omitempty"`
}

func newNativeBrowserCaptureCommandWith(run func(context.Context, browserdiagnostics.Options) (browserdiagnostics.Result, error)) *cobra.Command {
	options := browserdiagnostics.DefaultOptions()
	command := &cobra.Command{
		Use: "native-capture URL", Short: "Collect native Chromium key logs and original RTP in a private session",
		Long: "Launch an isolated, extension-free Chromium session with explicit native diagnostics.\nTLS/QUIC key logging and original RTP/RTCP logging are independent opt-ins.\nTLS key capture requires an explicit interface and BPF filter for wire packets.\nThe new private output directory contains sensitive capture artifacts.\nRead rep describe native-capture for provenance, bounds and browser support.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (returnErr error) {
			if _, err := headlessTaskDir(); err != nil {
				return err
			}
			options.URL = args[0]
			if err := browserdiagnostics.Validate(options); err != nil {
				return err
			}
			record, err := beginBrowserEvidence("browser.native_capture", options, "private_chromium", 0)
			if err != nil {
				return err
			}
			defer record.finishOnReturn(&returnErr)
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			record.dispatch()
			result, captureErr := run(ctx, options)
			if captureErr == nil && result.Status == "failed" {
				captureErr = errors.New("native browser capture failed: " + result.Error)
			}
			if captureErr == nil && result.Status == "cancelled" {
				captureErr = context.Canceled
			}
			if record != nil {
				for _, artifact := range result.Artifacts {
					record.reference(evidence.Reference{Kind: "native_browser_artifact", ID: artifact.Path, SHA256: artifact.SHA256,
						Metadata: map[string]any{"kind": artifact.Kind, "bytes": artifact.Bytes, "storage": "private_capture_bundle", "imported": false}})
				}
				// The manifest contains paths and hashes. Key material and media stay
				// in the explicitly requested private bundle, without extra copies.
				manifest := filepath.Join(result.Output, "manifest.json")
				if result.Output != "" {
					if _, err := os.Stat(manifest); err == nil {
						captureErr = errors.Join(captureErr, record.artifact(manifest, "native_browser_manifest", "unknown", "native diagnostic observation; payload completeness is not established", nil))
					} else {
						captureErr = errors.Join(captureErr, fmt.Errorf("capture manifest unavailable for evidence: %w", err))
					}
				}
			}
			status := "completed"
			if captureErr != nil {
				status = "failed"
			}
			captureErr = errors.Join(captureErr, record.finish(result, status, "unverified", "", captureErr))
			response := nativeBrowserCaptureOutput{Result: result, Evidence: record.ref()}
			if captureErr != nil {
				message := captureErr.Error()
				if len(message) > 4096 {
					message = message[:4096]
				}
				failure := output.NewAgentError("native_browser_capture_failed", cmd.CommandPath(), message)
				if errors.Is(captureErr, context.Canceled) {
					failure.Code = "capture_cancelled"
				}
				response.CommandError = &failure
			}
			if err := writePacketJSON(cmd, response, packetResultBudget); err != nil {
				return errors.Join(captureErr, err)
			}
			if captureErr != nil {
				return output.MarkReported(*response.CommandError)
			}
			return nil
		},
	}
	command.Flags().StringVar(&options.Output, "output", "", "New private capture directory (required; existing paths are rejected)")
	command.Flags().StringVar(&options.Binary, "binary", "", "Chromium executable (or REP_HEADLESS_BINARY; otherwise discover installed Chrome)")
	command.Flags().BoolVar(&options.TLSKeys, "tls-keys", false, "Record this new session's native TLS/QUIC keys alongside filtered wire packets")
	command.Flags().BoolVar(&options.WebRTCRTP, "webrtc-rtp", false, "Record original native plaintext RTP/RTCP datagrams at the SRTP boundary")
	command.Flags().BoolVar(&options.Headless, "headless", false, "Use headless Chromium; the default opens a private visible window")
	command.Flags().StringVar(&options.Interface, "interface", "", "Explicit native packet interface (required with --tls-keys)")
	command.Flags().StringVar(&options.Filter, "filter", "", "Explicit BPF packet filter (required with --interface)")
	command.Flags().DurationVar(&options.Duration, "duration", options.Duration, "Capture duration; maximum 10 minutes")
	command.Flags().Int64Var(&options.MaxLogBytes, "max-log-bytes", options.MaxLogBytes, "Maximum retained Chromium diagnostic log bytes")
	command.Flags().Int64Var(&options.MaxKeyLogBytes, "max-key-log-bytes", options.MaxKeyLogBytes, "TLS key-log stop threshold; native writes may overshoot before shutdown")
	command.Flags().Int64Var(&options.MaxPacketBytes, "max-packet-bytes", options.MaxPacketBytes, "Maximum retained wire pcap bytes")
	return command
}

func init() { browserCmd.AddCommand(newNativeBrowserCaptureCommandWith(browserdiagnostics.Run)) }
