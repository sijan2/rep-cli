package cmd

import (
	"fmt"
	"io"
	"sort"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/retention"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

func gcNewCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "gc", Short: "Plan and apply task-local body retention",
		SilenceErrors: true, SilenceUsage: true,
		Long: "Evict aged or over-budget body bytes, keeping request metadata and explicit eviction markers. Evidence references are permanently protected. The default action is a dry-run plan.",
		Args: cobra.NoArgs,
	}
	gcConfigureCommand(command, "plan")
	for _, action := range []string{"plan", "apply", "status"} {
		child := &cobra.Command{Use: action, Short: map[string]string{
			"plan": "Show body evictions without writing", "apply": "Show the plan; apply only with --yes", "status": "Show retained usage by class for the selected task",
		}[action], Args: cobra.NoArgs}
		gcConfigureCommand(child, action)
		command.AddCommand(child)
	}
	return command
}

func gcConfigureCommand(command *cobra.Command, action string) {
	options := retention.RetentionOptions{MaxBytes: retention.RetentionDefaultMaxBytes, KeepDays: retention.RetentionDefaultKeepDays, Status: action == "status"}
	var yes bool
	if action != "status" {
		command.Flags().Int64Var(&options.MaxBytes, "max-bytes", options.MaxBytes, "Task byte budget; pressure evicts ambient bodies before primary bodies")
		command.Flags().IntVar(&options.KeepDays, "keep-days", options.KeepDays, "Primary age limit in days; ambient age limit is at most one day")
	}
	if action == "apply" {
		command.Flags().BoolVar(&yes, "yes", false, "Apply the displayed plan")
	}
	command.RunE = func(cmd *cobra.Command, args []string) error { return gcRun(cmd, action, options, yes) }
}

func gcRun(command *cobra.Command, action string, options retention.RetentionOptions, yes bool) error {
	jsonMode := getOutputMode() == "json"
	fail := func(code string, err error) error {
		return output.EmitAgentError(command.OutOrStdout(), output.NewAgentError(code, "gc "+action, err.Error(), "rep --workspace <project> --task <task> gc plan"), jsonMode)
	}
	if options.MaxBytes < 0 || options.KeepDays < 0 || options.KeepDays > 365000 {
		return fail(output.ErrCodeInvalidArgument, fmt.Errorf("max-bytes must be nonnegative and keep-days must be between 0 and 365000"))
	}
	selected, err := scope.Current()
	if err != nil {
		return fail(output.ErrCodeInvalidArgument, err)
	}
	if !selected.Scoped || selected.TaskSource == "default" {
		return fail("scope_required", fmt.Errorf("gc requires an explicit workspace and task; shared global data cannot be collected"))
	}
	dir, err := store.GetStorePath()
	if err != nil {
		return fail(output.ErrCodeStoreRead, err)
	}
	plan, err := retention.RetentionBuildPlan(dir, options)
	if err != nil {
		return fail(output.ErrCodeStoreRead, err)
	}
	plan.Report.Workspace, plan.Report.Task = selected.Workspace, selected.Task
	if action != "apply" {
		phase := "plan"
		if action == "status" {
			phase = "status"
		}
		return gcEmitReport(command.OutOrStdout(), action, phase, dir, plan.Report, nil, jsonMode)
	}
	if !yes {
		ae := output.NewAgentError("confirmation_required", "gc apply", "plan only: apply requires --yes", "rep gc apply --yes with the same scope and retention flags")
		if err := gcEmitReport(command.OutOrStdout(), action, "plan", dir, plan.Report, &ae, jsonMode); err != nil {
			return err
		}
		return output.MarkReported(ae)
	}
	// Emit and flush the exact snapshot before performing its first mutation.
	if err := gcEmitReport(command.OutOrStdout(), action, "plan", dir, plan.Report, nil, jsonMode); err != nil {
		return err
	}
	if writer, ok := command.OutOrStdout().(interface{ Flush() error }); ok {
		if err := writer.Flush(); err != nil {
			return err
		}
	}
	report, err := retention.RetentionApply(plan)
	var ae *output.AgentError
	if err != nil {
		value := output.NewAgentError(output.ErrCodeStoreWrite, "gc apply", err.Error(), "rep gc plan with the same scope")
		ae = &value
	}
	if report != nil {
		if emitErr := gcEmitReport(command.OutOrStdout(), action, "result", dir, report, ae, jsonMode); emitErr != nil {
			return emitErr
		}
	}
	if ae != nil {
		return output.MarkReported(*ae)
	}
	return nil
}

func gcEmitReport(writer io.Writer, action, phase, dir string, report *retention.RetentionReport, ae *output.AgentError, jsonMode bool) error {
	if jsonMode {
		data := struct {
			Phase  string                     `json:"phase"`
			Report *retention.RetentionReport `json:"report"`
			Error  *output.AgentError         `json:"error,omitempty"`
		}{phase, report, ae}
		envelope := output.WrapData("gc "+action, dir, data)
		envelope.Filters = map[string]any{"workspace": report.Workspace, "task": report.Task, "max_bytes": report.Policy.MaxBytes, "keep_days": report.Policy.PrimaryKeepDays}
		envelope.Suggest = []string{"rep gc status with the same scope", "rep gc plan with the same scope"}
		if report.EvictionsOmitted > 0 || report.SkippedOmitted > 0 {
			envelope.Truncation = &output.TruncationInfo{Reason: "detail samples capped; aggregate counts and bytes are complete", Returned: len(report.Evictions) + len(report.Skipped), Total: len(report.Evictions) + len(report.Skipped) + report.EvictionsOmitted + report.SkippedOmitted}
		}
		raw, err := sonic.Marshal(envelope)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(writer, string(raw))
		return err
	}
	if _, err := fmt.Fprintf(writer, "gc %s (%s) workspace=%s task=%s: %d bytes\n", action, phase, report.Workspace, report.Task, report.TotalBytes); err != nil {
		return err
	}
	for _, class := range []string{retention.RetentionEvidence, retention.RetentionPrimary, retention.RetentionAmbient} {
		usage := report.Classes[class]
		if _, err := fmt.Fprintf(writer, "%s: %d records, %d body bytes; planned reclaimed %d bytes, reclaimed %d bytes; protected %d records (%d body bytes)\n", class, usage.Records, usage.BodyBytes, usage.PlannedReclaimed, usage.ReclaimedBytes, usage.ProtectedRecords, usage.ProtectedBytes); err != nil {
			return err
		}
	}
	var reasons []string
	for reason := range report.Protected {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		protection := report.Protected[reason]
		if _, err := fmt.Fprintf(writer, "protected %s: %d records, %d body bytes, %d files (%d storage bytes)\n", reason, protection.Records, protection.Bytes, protection.Files, protection.StorageBytes); err != nil {
			return err
		}
	}
	for _, eviction := range report.Evictions {
		if _, err := fmt.Fprintf(writer, "planned %s %s/%s: %s, %d body bytes, %d bytes reclaimed\n", eviction.Class, eviction.File, eviction.RequestID, eviction.Reason, eviction.BodyBytes, eviction.Reclaimed); err != nil {
			return err
		}
	}
	if report.EvictionsOmitted > 0 || report.SkippedOmitted > 0 {
		if _, err := fmt.Fprintf(writer, "detail samples omitted: %d evictions, %d skipped files\n", report.EvictionsOmitted, report.SkippedOmitted); err != nil {
			return err
		}
	}
	for _, skipped := range report.Skipped {
		if _, err := fmt.Fprintf(writer, "skipped %s: %s (%d bytes)\n", skipped.Path, skipped.Reason, skipped.Bytes); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(writer, "metadata/unmanaged: %d bytes; remaining: %d bytes; budget unmet: %d bytes\n", report.MetadataBytes, report.RemainingBytes, report.BudgetUnmetBytes); err != nil {
		return err
	}
	if ae != nil {
		_, err := fmt.Fprint(writer, output.FormatErrorText(*ae))
		return err
	}
	return nil
}

func init() { rootCmd.AddCommand(gcNewCommand()) }
