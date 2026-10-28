package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/repplus/rep-cli/internal/output"
	"github.com/spf13/cobra"
)

// Coverage is an offline aggregate: it never exercises the captured application.
func newCoverageCommand() *cobra.Command {
	var saved string
	command := &cobra.Command{
		Use: "coverage", Short: "Report capture gaps, body evidence, and collector visibility",
		Long: `Read one capture and report its retained evidence and known gaps.
Output is aggregate JSON with no response bodies, headers, or endpoint lists.
Unvisited application features remain unknown. A report about every retained
record does not prove that the collector captured every request or payload.

Use --saved HASH to inspect the same archive as a body or stream result.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			capture, err := loadObservedCapture(saved)
			if err != nil {
				return output.EmitAgentError(cmd.OutOrStdout(), output.NewAgentError(
					"coverage_unavailable", "coverage", err.Error(), "rep scope", "rep describe coverage",
				), true)
			}
			var result any = struct {
				Provenance observedCaptureProvenance `json:"provenance"`
				Coverage   CaptureCoverageReport     `json:"coverage"`
			}{capture.Provenance, capture.coverage()}
			if forceEnvelope && !rawJSON {
				result = output.WrapData("coverage", capture.Provenance.Source, result)
			}
			data, err := json.Marshal(result)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return err
		},
	}
	command.Flags().StringVar(&saved, "saved", "", "Read one archive (exact ID, unique prefix, or latest)")
	return command
}

func init() { rootCmd.AddCommand(newCoverageCommand()) }
