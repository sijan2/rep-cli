package browserflow

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"time"

	"github.com/repplus/rep-cli/internal/browserrpc"
)

//go:embed runtime.js
var renderer string

// Selection is provider-neutral; model probabilities/configuration stay outside the executor.
// Providers must apply their confidence/coverage policy before returning selected.
type Selection struct {
	Status           string
	NeedsReview      bool
	Name, Role       string
	BackendDOMNodeID int64
	TextTruncated    bool
}
type SelectFunc func(context.Context, string, string) (Selection, error)
type Engine struct {
	Browser browserrpc.Caller
	Select  SelectFunc
	calls   int
}
type StepResult struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Code        string `json:"code,omitempty"`
	Evidence    string `json:"evidence,omitempty"`
	Attempted   bool   `json:"attempted"`
	Changed     bool   `json:"changed"`
	DurationMS  int64  `json:"duration_ms"`
	Control     string `json:"control,omitempty"`
	WouldChange bool   `json:"would_change,omitempty"`
}
type Report struct {
	Version            int          `json:"version"`
	Status             string       `json:"status"`
	Applied            bool         `json:"applied"`
	Steps              []StepResult `json:"steps"`
	DurationMS         int64        `json:"duration_ms"`
	BridgeCalls        int          `json:"bridge_calls"`
	SemanticSelections int          `json:"semantic_selections"`
}
type runtimeSpec struct {
	URL       string `json:"url"`
	Operation string `json:"operation"`
	Step      Step   `json:"step"`
	TimeoutMS int    `json:"timeout_ms"`
}

func (engine *Engine) Call(ctx context.Context, method string, params any, out any) error {
	return engine.call(ctx, method, params, out)
}
func (engine *Engine) call(ctx context.Context, method string, params any, out any) error {
	engine.calls++
	return engine.Browser.Call(ctx, method, params, out)
}
func (engine *Engine) cdp(ctx context.Context, tab int, method string, params any, out any) error {
	return browserrpc.CDP(ctx, engine, tab, method, params, out)
}

func (engine *Engine) invoke(ctx context.Context, tab int, spec runtimeSpec, objectID string) (StepResult, error) {
	var wire struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	var err error
	if objectID != "" {
		err = engine.cdp(ctx, tab, "Runtime.callFunctionOn", map[string]any{"objectId": objectID, "functionDeclaration": "function(spec){return (" + renderer + ").call(this,spec)}", "arguments": []any{map[string]any{"value": spec}}, "awaitPromise": true, "returnByValue": true, "userGesture": spec.Operation == "perform"}, &wire)
	} else {
		encoded, _ := json.Marshal(spec)
		err = engine.cdp(ctx, tab, "Runtime.evaluate", map[string]any{"expression": "(" + renderer + ")(" + string(encoded) + ")", "awaitPromise": true, "returnByValue": true, "userGesture": spec.Operation == "perform"}, &wire)
	}
	if err != nil {
		return StepResult{}, err
	}
	if len(wire.Exception) > 0 || len(wire.Result.Value) == 0 {
		return StepResult{}, errors.New("renderer context unavailable")
	}
	var result StepResult
	if err = json.Unmarshal(wire.Result.Value, &result); err != nil {
		return result, err
	}
	switch result.Status {
	case "ready", "planned", "skipped", "verified", "prepared", "performed", "failed", "unconfirmed":
	default:
		return result, errors.New("invalid runtime status")
	}
	return result, nil
}
func (engine *Engine) bind(ctx context.Context, tab int, target *Target, page string) (string, error) {
	if target == nil || target.Goal == "" {
		return "", nil
	}
	if engine.Select == nil {
		return "", errors.New("semantic selection unavailable")
	}
	decision, err := engine.Select(ctx, target.Goal, page)
	if err != nil {
		return "", errors.New("semantic_selection_failed")
	}
	if decision.Status != "selected" || decision.NeedsReview || decision.BackendDOMNodeID <= 0 || decision.TextTruncated {
		switch decision.Status {
		case "needs_review", "no_match", "stale":
			return "", errors.New("semantic_" + decision.Status)
		}
		return "", errors.New("semantic_target_not_verified")
	}
	if target.Name == "" {
		target.Name = decision.Name
	}
	if target.Role == "" {
		target.Role = decision.Role
	}
	var resolved struct {
		Object struct {
			ID string `json:"objectId"`
		} `json:"object"`
	}
	err = engine.cdp(ctx, tab, "DOM.resolveNode", map[string]any{"backendNodeId": decision.BackendDOMNodeID, "objectGroup": "rep-interaction"}, &resolved)
	if err != nil || resolved.Object.ID == "" {
		return "", errors.New("semantic_target_stale")
	}
	return resolved.Object.ID, nil
}
func (engine *Engine) wait(ctx context.Context, tab int, page string, step Step) StepResult {
	timeout := step.TimeoutMS
	if timeout == 0 {
		timeout = 10000
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()
	// Re-enter only the read-only observer if a full document navigation destroys it.
	// No mutation is retried, including after an RPC timeout or a lost response.
	conditions := append([]Condition{{URL: page}}, step.After...)
	step.After = conditions
	for {
		remaining := time.Until(deadline(waitCtx)).Milliseconds()
		if remaining <= 0 {
			return StepResult{Status: "failed", Code: "postcondition_timeout"}
		}
		result, err := engine.invoke(waitCtx, tab, runtimeSpec{URL: page, Operation: "wait", Step: step, TimeoutMS: int(remaining)}, "")
		if err == nil {
			return result
		}
		select {
		case <-waitCtx.Done():
			return StepResult{Status: "failed", Code: "postcondition_timeout"}
		case <-time.After(50 * time.Millisecond):
		}
	}
}
func deadline(ctx context.Context) time.Time { value, _ := ctx.Deadline(); return value }

func (engine *Engine) Run(ctx context.Context, tab int, plan Plan, apply bool) (report Report, err error) {
	started := time.Now()
	engine.calls = 0
	report = Report{Version: 1, Status: "failed", Applied: apply, Steps: []StepResult{}}
	defer func() { report.DurationMS = time.Since(started).Milliseconds(); report.BridgeCalls = engine.calls }()
	if err = plan.Validate(); err != nil {
		return report, err
	}
	if tab < 0 || engine.Browser == nil {
		return report, errors.New("browser and tab are required")
	}
	var attached struct {
		Already bool `json:"already_attached"`
	}
	if err = engine.call(ctx, "browser.attach", map[string]any{"tab_id": tab}, &attached); err != nil {
		return report, errors.New("cannot attach interaction tab")
	}
	if attached.Already {
		return report, errors.New("tab already attached; existing owner preserved")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var ignored any
		_ = engine.cdp(cleanup, tab, "Runtime.releaseObjectGroup", map[string]any{"objectGroup": "rep-interaction"}, &ignored)
		if cleanupErr := engine.call(cleanup, "browser.detach", map[string]any{"tab_id": tab}, &ignored); cleanupErr != nil {
			report.Status = "cleanup_failed"
			if err == nil {
				err = errors.New("interaction finished but debugger cleanup failed")
			}
		}
	}()
	page := plan.URL
	for index, original := range plan.Steps {
		step := original
		if step.Target != nil {
			copy := *step.Target
			step.Target = &copy
		}
		if !apply && index > 0 {
			report.Steps = append(report.Steps, StepResult{ID: step.ID, Status: "planned", Evidence: "deferred_until_prior_steps"})
			continue
		}
		stepStart := time.Now()
		if step.Target != nil && step.Target.Goal != "" {
			if len(step.SkipIf) > 0 {
				skipped, skipErr := engine.invoke(ctx, tab, runtimeSpec{URL: page, Operation: "check_skip", Step: step}, "")
				if skipErr != nil || skipped.Status == "failed" {
					skipped.ID = step.ID
					skipped.Status = "failed"
					if skipped.Code == "" {
						skipped.Code = "skip_check_failed"
					}
					report.Steps = append(report.Steps, skipped)
					return report, errors.New("skip condition could not be checked")
				}
				if skipped.Status == "skipped" {
					skipped.ID = step.ID
					report.Steps = append(report.Steps, skipped)
					continue
				}
			}
			report.SemanticSelections++
		}
		objectID, bindErr := engine.bind(ctx, tab, step.Target, page)
		if bindErr != nil {
			report.Steps = append(report.Steps, StepResult{ID: step.ID, Status: "failed", Code: bindErr.Error(), DurationMS: time.Since(stepStart).Milliseconds()})
			return report, errors.New("semantic target was not verified; no action performed")
		}
		operation := "preview"
		if apply {
			operation = "perform"
		}
		nextPage := page
		for _, condition := range step.After {
			if condition.URL != "" {
				nextPage = condition.URL
			}
		}
		var result StepResult
		var runErr error
		if apply && step.Action == "wait" {
			result = engine.wait(ctx, tab, nextPage, step)
		} else {
			result, runErr = engine.invoke(ctx, tab, runtimeSpec{URL: page, Operation: operation, Step: step, TimeoutMS: step.TimeoutMS}, objectID)
		}
		if runErr != nil {
			result = StepResult{Status: "failed", Code: "renderer_unavailable"}
			if apply && step.Action != "wait" {
				result.Status = "unconfirmed"
				result.Attempted = true
				result.Code = "action_outcome_unknown"
			}
		}
		if apply && result.Status == "prepared" && step.Action == "press" {
			for _, key := range step.Keys {
				guard, guardErr := engine.invoke(ctx, tab, runtimeSpec{URL: page, Operation: "focus_guard", Step: step}, objectID)
				if guardErr != nil || guard.Status != "verified" {
					result.Status = "unconfirmed"
					result.Code = "focus_changed"
					break
				}
				event, _ := KeyEvent(key)
				for _, kind := range []string{"keyDown", "keyUp"} {
					event["type"] = kind
					result.Attempted = true
					var ignored any
					if keyErr := engine.cdp(ctx, tab, "Input.dispatchKeyEvent", event, &ignored); keyErr != nil {
						result.Status = "unconfirmed"
						result.Code = "key_outcome_unknown"
						// Still attempt the paired release when keyDown's reply is lost.
						// A release is not a retry of the keyDown action.
					}
				}
				if result.Status == "unconfirmed" {
					break
				}
			}
			if result.Status == "prepared" {
				result.Status = "performed"
				result.Changed = true
			}
		}
		if apply && result.Status == "performed" {
			checked := engine.wait(ctx, tab, nextPage, step)
			result.Evidence = checked.Evidence
			result.Code = checked.Code
			result.Status = checked.Status
			if result.Status != "verified" {
				result.Status = "unconfirmed"
			}
		}
		result.ID = step.ID
		result.DurationMS = time.Since(stepStart).Milliseconds()
		report.Steps = append(report.Steps, result)
		if result.Status == "failed" || result.Status == "unconfirmed" {
			report.Status = result.Status
			return report, errors.New("interaction stopped; inspect the step result before resuming")
		}
		if result.Status != "skipped" {
			page = nextPage
		}
	}
	report.Status = "verified"
	if !apply {
		report.Status = "preview"
	}
	return report, nil
}
