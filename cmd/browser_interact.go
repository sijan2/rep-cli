package cmd

import (
	"context"
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/repplus/rep-cli/internal/browserflow"
	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/repplus/rep-cli/internal/jevrpc"
	"github.com/spf13/cobra"
)

func newBrowserInteractCommand(deps jevDOMDependencies) *cobra.Command {
	var tab int
	var path, browserName string
	var apply, noCache bool
	command := &cobra.Command{Use: "interact [plan.json] --tab ID", Short: "Run explicit, verified UI interactions with optional Jev target selection", Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) (returnErr error) {
			if len(args) == 1 {
				if path != "" {
					return reportJevError(command, errors.New("provide a positional plan or --plan, not both"))
				}
				path = args[0]
			}
			if tab < 0 || path == "" {
				return reportJevError(command, errors.New("tab and plan are required"))
			}
			if browserName != "arc" && browserName != "chrome" && browserName != "headless" && browserName != "any" {
				return reportJevError(command, errors.New("unsupported browser"))
			}
			file, err := os.Open(path)
			if err != nil {
				return reportJevError(command, errors.New("cannot open interaction plan"))
			}
			plan, err := browserflow.Decode(file)
			_ = file.Close()
			if err != nil {
				return reportJevError(command, err)
			}
			// Credentials are unnecessary for exact local targets. Nothing in the plan's
			// input values or postconditions is forwarded to the semantic selector.
			var config jev.Config
			semantic := false
			for _, step := range plan.Steps {
				semantic = semantic || (step.Target != nil && step.Target.Goal != "")
			}
			if semantic {
				config, err = deps.loadConfig()
				if err != nil {
					return reportJevError(command, err)
				}
			}
			record, err := beginBrowserEvidence("browser.interact", map[string]any{"apply": apply, "steps": len(plan.Steps), "semantic_selection": semantic}, browserName, tab)
			if err != nil {
				return reportJevError(command, err)
			}
			defer record.finishOnReturn(&returnErr)
			ctx, cancel := context.WithTimeout(command.Context(), 10*time.Minute)
			defer cancel()
			browser, err := deps.browser(ctx, browserName)
			if err != nil {
				return reportJevError(command, err)
			}
			owner, err := decisionOwner()
			if err != nil {
				return reportJevError(command, err)
			}
			engine := browserflow.Engine{Browser: browser, Owner: owner}
			var decisionRequests int
			var decisionUsage jev.Usage
			if semantic {
				engine.SelectBatchScoped = func(ctx context.Context, targets []browserflow.Target, _ string) ([]browserflow.Selection, error) {
					options := make([]jevdom.Options, len(targets))
					lease := engine.Lease()
					groups := map[string][]int{}
					var order []string
					for index, target := range targets {
						options[index] = jevdom.DefaultOptions()
						options[index].TabID, options[index].Goal = tab, target.Goal
						options[index].Model, options[index].NoCache = config.Model, noCache
						options[index].Owner, options[index].LeaseID = lease.Owner, lease.LeaseID
						options[index].FrameID, options[index].FrameURL = target.FrameID, target.FrameURL
						options[index].ScopeBackendDOMNodeID = target.ScopeBackendDOMNodeID
						key := target.FrameID + "\x00" + target.FrameURL + "\x00" + strconv.FormatInt(target.ScopeBackendDOMNodeID, 10)
						if _, exists := groups[key]; !exists {
							order = append(order, key)
						}
						groups[key] = append(groups[key], index)
					}
					resolved := make([]browserflow.Selection, len(targets))
					for _, key := range order {
						remaining := groups[key]
						for len(remaining) > 0 {
							positions := remaining[:min(len(remaining), jevrpc.MaxBatch)]
							remaining = remaining[len(positions):]
							batch := make([]jevdom.Options, len(positions))
							for index, position := range positions {
								batch[index] = options[position]
							}
							decisions, err := runSelections(ctx, deps, &engine, config, batch)
							if err != nil {
								return nil, err
							}
							if len(decisions) != len(positions) {
								return nil, errors.New("incomplete semantic batch")
							}
							for index, decision := range decisions {
								record.observation("before", "controls", decision.SnapshotFingerprint, decision.Binding)
								position := positions[index]
								resolved[position] = interactionSelection(decision)
								resolved[position].ScopeFrameID, resolved[position].ScopeFrameURL = batch[index].FrameID, batch[index].FrameURL
								resolved[position].ScopeBackendDOMNodeID = batch[index].ScopeBackendDOMNodeID
								decisionRequests += decision.Timing.Requests
								decisionUsage.InputTokens += decision.Usage.InputTokens
								decisionUsage.OutputTokens += decision.Usage.OutputTokens
							}
						}
					}
					return resolved, nil
				}
			}
			if apply {
				record.dispatch()
			}
			report, runErr := engine.Run(ctx, tab, plan, apply)
			result := struct {
				browserflow.Report
				Evidence         *operationEvidenceRef `json:"evidence,omitempty"`
				DecisionRequests int                   `json:"decision_requests"`
				DecisionUsage    jev.Usage             `json:"decision_usage"`
			}{Report: report, Evidence: record.ref(), DecisionRequests: decisionRequests, DecisionUsage: decisionUsage}
			status, verification := "completed", "unverified"
			if runErr != nil || report.Status == "failed" {
				status, verification = "failed", "unknown"
			}
			if report.Status == "unconfirmed" {
				status, verification = "unknown", "unknown"
			}
			if report.Status == "verified" {
				verification = "satisfied"
			}
			if err := record.finish(map[string]any{"report": report, "decision_requests": decisionRequests, "decision_usage": decisionUsage,
				"verification_scope": "declared_plan_checks"}, status, verification, "", runErr); err != nil {
				return reportJevError(command, errors.Join(runErr, err))
			}
			if err = emitJev(command, command.CommandPath(), result); err != nil {
				return err
			}
			if runErr != nil {
				return reportJevError(command, runErr)
			}
			return nil
		}}
	command.Flags().IntVar(&tab, "tab", -1, "Existing task-owned browser tab")
	command.Flags().StringVar(&path, "plan", "", "Versioned JSON interaction plan (values remain local)")
	command.Flags().StringVar(&browserName, "browser", "arc", "arc, chrome, any, or task-owned headless")
	command.Flags().BoolVar(&apply, "apply", false, "Execute the explicit plan; default validates and previews first target")
	command.Flags().BoolVar(&noCache, "no-cache", false, "Bypass the Jev decision cache for semantic targets")
	_ = command.MarkFlagRequired("tab")
	advancedFlags(command, "plan", "no-cache")
	return command
}

func interactionSelection(decision jevdom.Result) browserflow.Selection {
	resolved := browserflow.Selection{Status: decision.Status, NeedsReview: decision.NeedsReview, Kind: "controls", SnapshotFingerprint: decision.SnapshotFingerprint}
	if candidate := decision.Selected; candidate != nil {
		resolved.Name, resolved.Role = candidate.Name, candidate.Role
		resolved.BackendDOMNodeID, resolved.TextTruncated = candidate.BackendDOMNodeID, candidate.TextTruncated
		resolved.FrameID, resolved.FrameURL = candidate.FrameID, candidate.FrameURL
		resolved.SessionID, resolved.DocumentGeneration = candidate.SessionID, candidate.DocumentGeneration
	}
	if binding := decision.Binding; binding != nil {
		resolved.ObservationMode = binding.ObservationMode
		resolved.Kind, resolved.Origin = binding.Kind, binding.Origin
		resolved.ScopeFrameID, resolved.ScopeFrameURL = binding.ScopeFrameID, binding.ScopeFrameURL
		resolved.ScopeBackendDOMNodeID = binding.ScopeBackendDOMNodeID
		resolved.Generation, resolved.SnapshotFingerprint = binding.Generation, binding.SnapshotFingerprint
		resolved.FrameID, resolved.FrameURL = binding.FrameID, binding.FrameURL
		resolved.SessionID, resolved.DocumentGeneration = binding.SessionID, binding.DocumentGeneration
	}
	return resolved
}
func init() {
	browserCmd.AddCommand(newBrowserInteractCommand(defaultJevDOMDependencies()))
	legacy := newBrowserInteractCommand(defaultJevDOMDependencies())
	legacy.Use = "act [plan.json] --tab ID"
	legacy.Hidden = true
	jevCmd.AddCommand(legacy)
}
