package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/engine"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(engineNewCommand())
}

func engineNewCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "engine", Short: "Bounded typed model proposals and independent adjudication",
		Long: "Run typed triage or adjudicate tasks with scrubbed facts. Every decision is model_assessed, never observed evidence. Models cannot execute proposals through this command.",
	}
	registry := engine.EngineNewRegistry()
	for _, name := range []string{"list", "check"} {
		command.AddCommand(&cobra.Command{
			Use: name, Short: "Inspect local engines, models, capabilities and availability without a model call", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return engineEmit(cmd, "engine "+cmd.Name(), map[string]any{"engines": registry.List(), "model_call": false}, nil)
			},
		})
	}
	var options engine.EngineOptions
	var task, input string
	run := &cobra.Command{
		Use: "run --job triage|adjudicate --input @file", Short: "Assess typed facts with one bounded model call", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options.Task = engine.EngineTask(task)
			if err := engine.EngineValidateLimits(options.Limits); err != nil {
				return engineReportError(cmd, err)
			}
			data, err := engineReadInput(input, options.Limits.MaxInputBytes)
			if err != nil {
				return engineReportError(cmd, err)
			}
			result, err := registry.Run(cmd.Context(), options, data)
			if err != nil {
				return engineReportError(cmd, err)
			}
			filters := map[string]any{"job": options.Task, "engine": result.Engine, "model": result.Model, "timeout": options.Limits.Timeout.String(), "max_input_bytes": options.Limits.MaxInputBytes, "max_output_bytes": options.Limits.MaxOutputBytes}
			return engineEmit(cmd, "engine run", result, filters)
		},
	}
	run.Flags().StringVar(&task, "job", "", "Typed job: triage or adjudicate")
	run.Flags().StringVar(&input, "input", "", "Typed JSON facts in @file (no raw bodies or credentials)")
	run.Flags().StringVar(&options.Engine, "engine", "", "Engine name (default: codex for triage, jev for adjudicate)")
	run.Flags().StringVar(&options.Model, "model", "", "Model supported by the selected engine")
	run.Flags().StringVar(&options.Effort, "effort", "medium", "Codex reasoning effort: low, medium, high, xhigh, max")
	run.Flags().DurationVar(&options.Limits.Timeout, "timeout", engine.EngineDefaultTimeout, "Overall model deadline (maximum 2m)")
	run.Flags().IntVar(&options.Limits.MaxInputBytes, "max-input-bytes", engine.EngineMaxInputBytes, "Maximum input and provider request bytes (maximum 65536)")
	run.Flags().IntVar(&options.Limits.MaxOutputBytes, "max-output-bytes", engine.EngineDefaultOutput, "Maximum model answer bytes (maximum 65536)")
	advancedFlags(run, "max-input-bytes", "max-output-bytes", "effort")
	_ = run.MarkFlagRequired("job")
	_ = run.MarkFlagRequired("input")
	command.AddCommand(run)
	return command
}

func engineReadInput(argument string, limit int) ([]byte, error) {
	invalid := func(code, message string) error { return &engine.EngineError{Code: code, Message: message} }
	if !strings.HasPrefix(argument, "@") || len(argument) <= 1 {
		return nil, invalid("engine_invalid_input", "--input requires @file containing typed JSON facts")
	}
	path := strings.TrimPrefix(argument, "@")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, invalid("engine_invalid_input", "input must be a readable regular file")
	}
	if info.Size() > int64(limit) {
		return nil, invalid("engine_input_limit", "input exceeds max-input-bytes")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, invalid("engine_invalid_input", "cannot open input file")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, invalid("engine_invalid_input", "cannot read input file")
	}
	if len(data) > limit {
		return nil, invalid("engine_input_limit", "input exceeds max-input-bytes")
	}
	return data, nil
}

func engineEmit(cmd *cobra.Command, command string, data any, filters map[string]any) error {
	if useEnvelope() {
		envelope := output.WrapData(command, "engine", data)
		envelope.Filters = filters
		data = envelope
	}
	encoded, err := sonic.MarshalIndent(data, "", "  ")
	if err != nil {
		return engineReportError(cmd, &engine.EngineError{Code: "engine_encoding_failed", Message: "cannot encode engine result"})
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
	return err
}

func engineReportError(cmd *cobra.Command, err error) error {
	code, message := "engine_failed", "engine command failed"
	var classified *engine.EngineError
	if errors.As(err, &classified) {
		code, message = classified.Code, classified.Message
	}
	ae := output.NewAgentError(code, cmd.CommandPath(), message, "rep engine list -j")
	writer := cmd.ErrOrStderr()
	if getOutputMode() == "json" {
		writer = cmd.OutOrStdout()
	}
	return output.EmitAgentError(writer, ae, getOutputMode() == "json")
}
