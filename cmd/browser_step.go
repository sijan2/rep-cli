package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/browserflow"
	"github.com/repplus/rep-cli/internal/browserrpc"
	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/repplus/rep-cli/internal/jevrpc"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

// browser step is jev-ultrafast's decision loop inside Rep's verified runtime:
// one observation, one Jev request choosing an operation and its target, and
// optionally one guarded action. Typed values never leave this process.

type stepOutput struct {
	Version     int                     `json:"version"`
	Evidence    *operationEvidenceRef   `json:"evidence,omitempty"`
	Decision    jevdom.StepDecision     `json:"decision"`
	Reused      bool                    `json:"decision_reused,omitempty"`
	Applied     bool                    `json:"applied"`
	Execution   *browserflow.StepResult `json:"execution,omitempty"`
	PageChanged *bool                   `json:"page_changed,omitempty"`
	Fingerprint string                  `json:"fingerprint,omitempty"`
	Next        string                  `json:"next,omitempty"`
	// Error is set when --apply did not perform the step. It is part of this
	// single document; stdout never carries a second JSON object.
	Error *output.AgentError `json:"error,omitempty"`
}

type stepValues struct {
	keys   []string
	values map[string]string
}

func parseStepValues(pairs []string, text string, file string) (stepValues, error) {
	result := stepValues{values: map[string]string{}}
	add := func(key, value string) error {
		key = strings.TrimSpace(key)
		if _, exists := result.values[key]; exists {
			return fmt.Errorf("value %q is supplied more than once", key)
		}
		if len(value) > 32768 {
			return fmt.Errorf("value %q exceeds 32 KiB", key)
		}
		result.values[key] = value
		result.keys = append(result.keys, key)
		return nil
	}
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return result, errors.New("cannot read --values-file")
		}
		var values map[string]string
		decoder := json.NewDecoder(bytes.NewReader(data))
		if decoder.Decode(&values) != nil || decoder.Decode(new(any)) != io.EOF {
			return result, errors.New("--values-file must contain one JSON object of string values")
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sortStrings(keys)
		for _, key := range keys {
			if err := add(key, values[key]); err != nil {
				return result, err
			}
		}
	}
	for _, pair := range pairs {
		key, value, found := strings.Cut(pair, "=")
		if !found {
			return result, errors.New("--value must be NAME=TEXT")
		}
		if err := add(key, value); err != nil {
			return result, err
		}
	}
	if text != "" {
		if err := add("text", text); err != nil {
			return result, err
		}
	}
	return result, nil
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// stepHistoryPath keeps a small per-task, per-tab log of applied operations.
func stepHistoryPath(tab int) (string, error) {
	selected, err := scope.Current()
	if err != nil {
		return "", err
	}
	base := filepath.Join(os.TempDir(), "rep-artifacts")
	if selected.Scoped {
		base = selected.DataDir
	}
	return filepath.Join(base, "steps", "tab-"+strconv.Itoa(tab)+".json"), nil
}

func loadStepHistory(tab int) []jevdom.StepHistory {
	path, err := stepHistoryPath(tab)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 64*1024 {
		return nil
	}
	var history []jevdom.StepHistory
	if json.Unmarshal(data, &history) != nil {
		return nil
	}
	if len(history) > jevdom.MaxStepHistory {
		history = history[len(history)-jevdom.MaxStepHistory:]
	}
	return history
}

func saveStepHistory(tab int, history []jevdom.StepHistory) error {
	path, err := stepHistoryPath(tab)
	if err != nil {
		return err
	}
	if len(history) > jevdom.MaxStepHistory {
		history = history[len(history)-jevdom.MaxStepHistory:]
	}
	if err := privateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	data, _ := json.Marshal(history)
	return writePrivateFile(path, data)
}

// pendingStep is a previewed target decision that --apply may reuse once,
// after a synchronous revalidation, instead of repeating inference.
type pendingStep struct {
	Goal     string              `json:"goal"`
	Values   []string            `json:"value_keys"`
	History  int                 `json:"history"`
	SavedAt  int64               `json:"saved_at"`
	Decision jevdom.StepDecision `json:"decision"`
}

const pendingStepTTL = 2 * time.Minute

func pendingStepPath(tab int) (string, error) {
	path, err := stepHistoryPath(tab)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(path, ".json") + ".pending.json", nil
}

func savePendingStep(tab int, pending pendingStep) {
	path, err := pendingStepPath(tab)
	if err != nil || privateDirectory(filepath.Dir(path)) != nil {
		return
	}
	data, _ := json.Marshal(pending)
	_ = writePrivateFile(path, data)
}

// takePendingStep returns a matching, unexpired preview and always consumes it.
func takePendingStep(tab int, input jevdom.StepOptions, now time.Time) (jevdom.StepDecision, bool) {
	path, err := pendingStepPath(tab)
	if err != nil {
		return jevdom.StepDecision{}, false
	}
	data, err := os.ReadFile(path)
	_ = os.Remove(path)
	var pending pendingStep
	if err != nil || len(data) > 256*1024 || json.Unmarshal(data, &pending) != nil {
		return jevdom.StepDecision{}, false
	}
	age := now.Sub(time.Unix(0, pending.SavedAt))
	if pending.Goal != input.Goal || strings.Join(pending.Values, "\x00") != strings.Join(input.ValueKeys, "\x00") || pending.History != len(input.History) || age < 0 || age > pendingStepTTL {
		return jevdom.StepDecision{}, false
	}
	decision := pending.Decision
	if decision.Status != "selected" || decision.Target == nil || decision.Binding == nil {
		return jevdom.StepDecision{}, false
	}
	return decision, true
}

func stepSelection(decision jevdom.StepDecision) browserflow.Selection {
	selection := browserflow.Selection{Status: "selected", Kind: "controls", SnapshotFingerprint: decision.SnapshotFingerprint}
	if candidate := decision.Target; candidate != nil {
		selection.Name, selection.Role = candidate.Name, candidate.Role
		selection.BackendDOMNodeID, selection.TextTruncated = candidate.BackendDOMNodeID, candidate.TextTruncated
		selection.FrameID, selection.FrameURL = candidate.FrameID, candidate.FrameURL
		selection.SessionID, selection.DocumentGeneration = candidate.SessionID, candidate.DocumentGeneration
	}
	if binding := decision.Binding; binding != nil {
		selection.ObservationMode, selection.Kind, selection.Origin = binding.ObservationMode, binding.Kind, binding.Origin
		selection.ScopeFrameID, selection.ScopeFrameURL, selection.ScopeBackendDOMNodeID = binding.ScopeFrameID, binding.ScopeFrameURL, binding.ScopeBackendDOMNodeID
		selection.Generation, selection.SnapshotFingerprint = binding.Generation, binding.SnapshotFingerprint
		selection.FrameID, selection.FrameURL = binding.FrameID, binding.FrameURL
		selection.SessionID, selection.DocumentGeneration = binding.SessionID, binding.DocumentGeneration
	}
	return selection
}

func hostStep(ctx context.Context, browser jevdom.Browser, config jev.Config, options jevdom.StepOptions) (jevdom.StepDecision, error) {
	var capabilities jevrpc.Capabilities
	if err := browser.Call(ctx, "jev.capabilities", nil, &capabilities); err != nil || capabilities.Version != jevrpc.Version || !capabilities.StepDecision {
		return jevdom.StepDecision{}, output.NewAgentError("host_outdated", "", "the connected rep-host predates step decisions; install matching binaries and reconnect the browser",
			"scripts/build_install.sh --host  # in the rep-cli checkout", "rep browser reload-extension --browser arc", "rep browser headless restart")
	}
	request := jevrpc.StepRequest{Version: jevrpc.Version, Options: options}
	if config.APIKey != "" {
		request.Credentials = &jevrpc.Credentials{APIKey: config.APIKey}
	}
	var decision jevdom.StepDecision
	err := browser.Call(ctx, "jev.step", request, &decision)
	return decision, err
}

// observeAfter reports whether the controls or visible text changed after an
// action. A navigation can make reads fail briefly; those are retried until
// the deadline. The returned fingerprint is the controls fingerprint.
func observeAfter(ctx context.Context, browser jevdom.Browser, options jevdom.Options, before, textBefore string, wait time.Duration) (bool, string, bool) {
	deadline := time.Now().Add(wait)
	delay := 50 * time.Millisecond
	params := func(kind string) map[string]any {
		return map[string]any{"tab_id": options.TabID, "owner": options.Owner, "kind": kind, "origin": options.Origin, "frame_id": options.FrameID, "frame_url": options.FrameURL, "root_backend_dom_node_id": options.ScopeBackendDOMNodeID}
	}
	for {
		select {
		case <-ctx.Done():
			return false, "", false
		case <-time.After(delay):
		}
		var snapshot, text struct {
			Fingerprint string `json:"fingerprint"`
		}
		err := browser.Call(ctx, "browser.observe", params("controls"), &snapshot)
		controlsKnown := err == nil && before != "" && snapshot.Fingerprint != ""
		if controlsKnown && snapshot.Fingerprint != before {
			return true, snapshot.Fingerprint, true
		}
		textKnown := textBefore == ""
		if textBefore != "" {
			textKnown = browser.Call(ctx, "browser.observe", params("text"), &text) == nil && text.Fingerprint != ""
			if textKnown && text.Fingerprint != textBefore {
				return true, snapshot.Fingerprint, true
			}
		}
		if time.Now().After(deadline) {
			return false, snapshot.Fingerprint, controlsKnown && textKnown
		}
		if delay *= 2; delay > 250*time.Millisecond {
			delay = 250 * time.Millisecond
		}
	}
}

func newBrowserStepCommand(deps jevDOMDependencies) *cobra.Command {
	options := jevdom.DefaultOptions()
	browserName := "arc"
	var apply, noText, resetHistory bool
	var valuePairs []string
	var text, valuesFile string
	command := &cobra.Command{
		Use:   "step [goal] --tab ID",
		Short: "Choose the next operation and target with one Jev request; --apply performs it",
		Long: `Decide the single next operation toward a goal from the current page: CLICK,
TYPE_TEXT, SELECT (native dropdowns), SCROLL_DOWN, SCROLL_UP, WAIT, DONE, or
BLOCKED. One Jev request
carries an operation question plus operation-specific target questions
(jev-ultrafast's speculative fan-out); only the chosen operation's target is
used. TYPE_TEXT chooses a field and the name of a value you supplied with
--value NAME=TEXT or --text; only names reach Jev, never values.

Without --apply nothing is executed. With --apply a selected decision runs
through the verified executor: text entry and dropdown choices are checked by
exact read-back, and a click passes lease, freshness, geometry, and occlusion guards and is reported
as performed with observed page_changed evidence, not as verified success.
DONE is the model's claim; verify the outcome yourself. Applied steps are
remembered per task and tab (without values) so later steps avoid repeats.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) (returnErr error) {
			if len(args) == 1 {
				options.Goal = args[0]
			}
			if !validDecisionBrowser(browserName) {
				return reportJevError(command, errors.New("browser must be arc, chrome, any, or headless"))
			}
			values, err := parseStepValues(valuePairs, text, valuesFile)
			if err != nil {
				return reportJevError(command, err)
			}
			if resetHistory {
				if path, pathErr := stepHistoryPath(options.TabID); pathErr == nil {
					_ = os.Remove(path)
				}
			}
			input := jevdom.StepOptions{Options: options, ValueKeys: values.keys, History: loadStepHistory(options.TabID), NoText: noText}
			if input, err = input.Validate(); err != nil {
				return reportJevError(command, err)
			}
			config, err := deps.loadConfig()
			if err != nil {
				return reportJevError(command, err)
			}
			input.Model = config.Model
			if input.Owner, err = decisionOwner(); err != nil {
				return reportJevError(command, err)
			}
			record, err := beginBrowserEvidence("browser.step", map[string]any{"intent": input.Goal, "apply": apply, "value_keys": input.ValueKeys}, browserName, options.TabID)
			if err != nil {
				return reportJevError(command, err)
			}
			defer record.finishOnReturn(&returnErr)
			ctx, cancel := context.WithTimeout(command.Context(), jevdom.Timeout+30*time.Second)
			defer cancel()
			browser, err := deps.browser(ctx, browserName)
			if err != nil {
				return reportJevError(command, err)
			}
			var decision jevdom.StepDecision
			reused := false
			if apply {
				if pending, ok := takePendingStep(options.TabID, input, time.Now()); ok {
					// Zero inference, but only while the previewed controls are unchanged.
					fresh, validateErr := jevdom.Selector{Browser: browser}.Validate(ctx, input.Options, *pending.Binding)
					if validateErr == nil && fresh {
						decision, reused = pending, true
					}
				}
			}
			if !reused {
				decision, err = hostStep(ctx, browser, config, input)
				if err != nil {
					return reportJevError(command, err)
				}
			}
			record.observation("before", "controls", decision.SnapshotFingerprint, decision.Binding)
			record.observation("before", "text", decision.TextFingerprint, nil)
			result := stepOutput{Version: 1, Decision: decision, Reused: reused, Evidence: record.ref()}
			if !apply {
				if decision.Status == "selected" && decision.Target != nil && decision.Binding != nil {
					savePendingStep(options.TabID, pendingStep{Goal: input.Goal, Values: input.ValueKeys, History: len(input.History), SavedAt: time.Now().UnixNano(), Decision: decision})
				}
				switch {
				case decision.Status != "selected":
					result.Next = "Review the decision; narrow the goal or scope, or act with explicit targets"
				case decision.Operation == jevdom.OpDone:
					result.Next = "Verify the goal's outcome independently; DONE is the model's claim"
				default:
					result.Next = "Re-run with --apply to perform this step"
				}
				if err := finishStepEvidence(record, result, nil); err != nil {
					return reportJevError(command, err)
				}
				return emitJev(command, command.CommandPath(), result)
			}
			if decision.Status == "selected" && decision.Operation != jevdom.OpDone && decision.Operation != jevdom.OpBlocked && decision.Operation != jevdom.OpWait {
				record.dispatch()
			}
			execution, changed, fingerprint, applyErr := applyStep(ctx, browser, options, input, decision, values)
			result.Applied, result.Execution, result.PageChanged, result.Fingerprint = applyErr == nil && execution != nil, execution, changed, fingerprint
			record.observation("after", "controls", fingerprint, nil)
			if applyErr == nil && execution != nil {
				entry := jevdom.StepHistory{Operation: decision.Operation, ValueKey: decision.ValueKey, Status: execution.Status, PageChanged: changed}
				if decision.Target != nil {
					entry.Target = decision.Target.Role + " " + decision.Target.Name
				}
				_ = saveStepHistory(options.TabID, append(input.History, entry))
			}
			if evidenceErr := finishStepEvidence(record, result, applyErr); evidenceErr != nil {
				applyErr = errors.Join(applyErr, evidenceErr)
			}
			if applyErr == nil {
				return emitJev(command, command.CommandPath(), result)
			}
			agentError := jevAgentError(command.CommandPath(), applyErr)
			result.Error = &agentError
			if err := emitJev(command, command.CommandPath(), result); err != nil {
				return err
			}
			if getOutputMode() != "json" {
				_ = output.EmitAgentError(command.ErrOrStderr(), agentError, false)
			}
			return output.MarkReported(agentError)
		},
	}
	command.Flags().IntVar(&options.TabID, "tab", -1, "Existing task-owned browser tab")
	command.Flags().StringVar(&browserName, "browser", "arc", "arc, chrome, any, or task-owned headless")
	command.Flags().BoolVar(&apply, "apply", false, "Perform a selected decision through the verified executor")
	command.Flags().StringArrayVar(&valuePairs, "value", nil, "Named value NAME=TEXT available to TYPE_TEXT (repeatable; names only reach Jev)")
	command.Flags().StringVar(&text, "text", "", "Shorthand for --value text=TEXT")
	command.Flags().StringVar(&valuesFile, "values-file", "", "JSON object of named values (keeps secrets out of process arguments)")
	command.Flags().Float64Var(&options.Confidence, "confidence", 0.8, "Minimum operation and target confidence (0 to 1)")
	command.Flags().IntVar(&options.Limit, "limit", 240, "Maximum candidate controls (1 to 480)")
	command.Flags().StringVar(&options.Origin, "origin", "", "Require and restrict to this top-frame origin")
	command.Flags().StringVar(&options.FrameID, "frame", "", "Restrict the observation to this frame id")
	command.Flags().StringVar(&options.FrameURL, "frame-url", "", "Restrict to the unique frame at this exact URL")
	command.Flags().Int64Var(&options.ScopeBackendDOMNodeID, "root-node", 0, "Restrict candidates to this backend node subtree; requires a frame")
	command.Flags().BoolVar(&noText, "no-text", false, "Omit visible page text evidence from the decision")
	command.Flags().BoolVar(&resetHistory, "reset-history", false, "Forget this tab's earlier applied steps first")
	_ = command.MarkFlagRequired("tab")
	advancedFlags(command, "limit", "origin", "frame", "frame-url", "root-node", "no-text", "values-file")
	return command
}

func finishStepEvidence(record *browserOperationEvidence, result stepOutput, applyErr error) error {
	if record == nil {
		return nil
	}
	status, verification, claim := "completed", "unverified", ""
	if result.Decision.Operation == jevdom.OpDone || result.Decision.Operation == jevdom.OpBlocked {
		claim = result.Decision.Operation
	}
	if applyErr != nil {
		status, verification = "failed", "unknown"
	}
	if result.Execution != nil {
		switch result.Execution.Status {
		case "verified":
			verification = "satisfied"
		case "unconfirmed":
			status, verification = "unknown", "unknown"
		case "failed":
			status, verification = "failed", "unknown"
		}
	}
	return record.finish(map[string]any{"decision_status": result.Decision.Status, "operation": result.Decision.Operation,
		"confidence": result.Decision.Confidence, "model": result.Decision.Model, "applied": result.Applied, "execution": result.Execution,
		"page_changed": result.PageChanged, "verification_scope": "declared_step_check"}, status, verification, claim, applyErr)
}

// applyStep performs a selected decision. It returns an error when nothing
// was attempted, or when the executor could not confirm the operation.
func applyStep(ctx context.Context, browser jevdom.Browser, options jevdom.Options, input jevdom.StepOptions, decision jevdom.StepDecision, values stepValues) (*browserflow.StepResult, *bool, string, error) {
	if decision.Status != "selected" {
		return nil, nil, "", output.NewAgentError("step_not_selected", "browser step", "the decision is "+decision.Status+"; nothing was performed: "+decision.Reason, "rep browser observe --tab "+strconv.Itoa(options.TabID))
	}
	observeOptions := input.Options
	result := &browserflow.StepResult{ID: "step", Status: "performed"}
	// Post-action observation windows follow jev-ultrafast: short by default,
	// longer where suggestions or async UI commonly follow. A change ends the
	// window early; the next step observes again anyway.
	wait := 400 * time.Millisecond
	if decision.Operation == jevdom.OpTypeText && decision.Target != nil {
		wait = 100 * time.Millisecond
		if decision.Target.Role == "combobox" || decision.Target.Role == "searchbox" {
			wait = 300 * time.Millisecond
		}
	}
	switch decision.Operation {
	case jevdom.OpDone, jevdom.OpBlocked:
		return nil, nil, "", nil
	case jevdom.OpClick, jevdom.OpTypeText, jevdom.OpSelect:
		if decision.Target == nil {
			return nil, nil, "", errors.New("the decision has no target")
		}
		step := browserflow.Step{ID: "step", Action: "click", Target: &browserflow.Target{Goal: "step decision", Role: decision.Target.Role}}
		if decision.Operation == jevdom.OpTypeText {
			value, ok := values.values[decision.ValueKey]
			if !ok {
				return nil, nil, "", output.NewAgentError("step_value_required", "browser step", "TYPE_TEXT needs a value; supply --text or --value NAME=TEXT", "rep browser step --help")
			}
			step.Action, step.Value, step.Replace = "fill", &value, true
		}
		if decision.Operation == jevdom.OpSelect {
			if decision.Option == "" {
				return nil, nil, "", errors.New("the SELECT decision has no option")
			}
			// Verified by the selected-option read-back of the choose adapter.
			step.Action, step.Values = "choose", []string{decision.Option}
		}
		engine := browserflow.Engine{Browser: browser, Owner: input.Owner}
		executed, err := engine.RunDecision(ctx, options.TabID, decision.PageURL, step, stepSelection(decision))
		result = &executed
		if err != nil {
			return result, nil, "", err
		}
		if executed.Status != "performed" && executed.Status != "verified" {
			return result, nil, "", output.NewAgentError("step_not_performed", "browser step", "the executor reported "+executed.Status+" ("+executed.Code+"); unconfirmed actions are never retried", "rep browser observe --tab "+strconv.Itoa(options.TabID))
		}
	case jevdom.OpScrollDown, jevdom.OpScrollUp:
		direction := "1"
		if decision.Operation == jevdom.OpScrollUp {
			direction = "-1"
		}
		var ignored any
		expression := "window.scrollBy({top: " + direction + " * Math.round(innerHeight * 0.8), behavior: 'instant'}), scrollY"
		if err := browserrpc.CDP(ctx, browser, options.TabID, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true}, &ignored); err != nil {
			return nil, nil, "", err
		}
		wait = 150 * time.Millisecond
	case jevdom.OpWait:
		wait = time.Second
	default:
		return nil, nil, "", errors.New("unsupported step operation")
	}
	changed, fingerprint, known := observeAfter(ctx, browser, observeOptions, decision.SnapshotFingerprint, decision.TextFingerprint, wait)
	if !known {
		return result, nil, fingerprint, nil
	}
	return result, &changed, fingerprint, nil
}

func init() {
	browserCmd.AddCommand(newBrowserStepCommand(defaultJevDOMDependencies()))
}
