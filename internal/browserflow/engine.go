package browserflow

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/browserrpc"
)

//go:embed runtime.js
var renderer string

// Selection is provider-neutral; model probabilities/configuration stay outside the executor.
// Providers must apply their confidence/coverage policy before returning selected.
type Selection struct {
	Status                                           string
	NeedsReview                                      bool
	Name, Role                                       string
	BackendDOMNodeID                                 int64
	TextTruncated                                    bool
	FrameID, FrameURL, SessionID, DocumentGeneration string
	Generation, SnapshotFingerprint, Kind, Origin    string
	ObservationMode                                  string
	ScopeFrameID, ScopeFrameURL                      string
	ScopeBackendDOMNodeID                            int64
}
type SelectFunc func(context.Context, string, string) (Selection, error)
type SelectBatchFunc func(context.Context, []string, string) ([]Selection, error)
type SelectScopedFunc func(context.Context, Target, string) (Selection, error)
type SelectBatchScopedFunc func(context.Context, []Target, string) ([]Selection, error)
type Engine struct {
	Browser           browserrpc.Caller
	Select            SelectFunc
	SelectBatch       SelectBatchFunc
	SelectScoped      SelectScopedFunc
	SelectBatchScoped SelectBatchScopedFunc
	Owner             string
	lease             browserrpc.Lease
	objectGroup       string
	routes            map[string]browserrpc.Route
	calls             int
}
type StepResult struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	Code        string            `json:"code,omitempty"`
	Evidence    string            `json:"evidence,omitempty"`
	Attempted   bool              `json:"attempted"`
	Changed     bool              `json:"changed"`
	DurationMS  int64             `json:"duration_ms"`
	Control     string            `json:"control,omitempty"`
	WouldChange bool              `json:"would_change,omitempty"`
	Point       *browserrpc.Point `json:"point,omitempty"`
}
type Report struct {
	Version            int          `json:"version"`
	Status             string       `json:"status"`
	Applied            bool         `json:"applied"`
	Steps              []StepResult `json:"steps"`
	DurationMS         int64        `json:"duration_ms"`
	BridgeCalls        int          `json:"bridge_calls"`
	SemanticSelections int          `json:"semantic_selections"`
	SemanticBatches    int          `json:"semantic_batches"`
	BindingReuses      int          `json:"binding_reuses"`
}
type runtimeSpec struct {
	URL           string            `json:"url"`
	Operation     string            `json:"operation"`
	Step          Step              `json:"step"`
	TimeoutMS     int               `json:"timeout_ms"`
	FrameID       string            `json:"frame_id,omitempty"`
	FrameURL      string            `json:"frame_url,omitempty"`
	OutcomeID     string            `json:"outcome_id,omitempty"`
	ExpectedPoint *browserrpc.Point `json:"expected_point,omitempty"`
}

func (engine *Engine) Lease() browserrpc.Lease { return engine.lease }

func (engine *Engine) Call(ctx context.Context, method string, params any, out any) error {
	return engine.call(ctx, method, params, out)
}
func (engine *Engine) call(ctx context.Context, method string, params any, out any) error {
	engine.calls++
	return engine.Browser.Call(ctx, method, params, out)
}
func (engine *Engine) cdp(ctx context.Context, tab int, method string, params any, out any) error {
	return engine.cdpRoute(ctx, tab, engine.route("", nil), method, params, out)
}
func (engine *Engine) cdpRoute(ctx context.Context, tab int, route browserrpc.Route, method string, params any, out any) error {
	return browserrpc.CDPRoute(ctx, engine, tab, route, method, params, out)
}
func (engine *Engine) route(page string, target *Target) browserrpc.Route {
	route := browserrpc.Route{Owner: engine.lease.Owner, LeaseID: engine.lease.LeaseID, Generation: engine.lease.Generation, ExpectedRootURL: page}
	if target != nil {
		route.FrameID, route.ExpectedFrameURL = target.FrameID, target.FrameURL
	}
	return route
}

type binding struct {
	decision Selection
	objectID string
	route    browserrpc.Route
}

func (engine *Engine) invoke(ctx context.Context, tab int, spec runtimeSpec, bound binding) (StepResult, error) {
	spec.FrameID, spec.FrameURL = bound.route.FrameID, bound.route.ExpectedFrameURL
	spec.OutcomeID = engine.objectGroup + ":" + spec.Step.ID
	spec.ExpectedPoint = bound.route.FramePoint
	var wire struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	var err error
	if bound.objectID != "" {
		err = engine.cdpRoute(ctx, tab, bound.route, "Runtime.callFunctionOn", map[string]any{"objectId": bound.objectID, "functionDeclaration": "function(spec){return (" + renderer + ").call(this,spec)}", "arguments": []any{map[string]any{"value": spec}}, "awaitPromise": true, "returnByValue": true, "userGesture": spec.Operation == "perform"}, &wire)
	} else {
		encoded, _ := json.Marshal(spec)
		err = engine.cdpRoute(ctx, tab, bound.route, "Runtime.evaluate", map[string]any{"expression": "(" + renderer + ")(" + string(encoded) + ")", "awaitPromise": true, "returnByValue": true, "userGesture": spec.Operation == "perform"}, &wire)
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
func (engine *Engine) bind(ctx context.Context, tab int, target *Target, page string, decision *Selection) (binding, error) {
	bound := binding{route: engine.route(page, target)}
	if target == nil || target.Goal == "" {
		return bound, nil
	}
	if decision == nil {
		return bound, errors.New("semantic selection unavailable")
	}
	if decision.Status != "selected" || decision.NeedsReview || decision.BackendDOMNodeID <= 0 || decision.TextTruncated {
		switch decision.Status {
		case "needs_review", "no_match", "stale":
			return bound, errors.New("semantic_" + decision.Status)
		}
		return bound, errors.New("semantic_target_not_verified")
	}
	if decision.FrameID == "" || decision.DocumentGeneration == "" || decision.Generation == "" || decision.SnapshotFingerprint == "" {
		return bound, errors.New("semantic_binding_incomplete")
	}
	if target.FrameID != "" && target.FrameID != decision.FrameID {
		return bound, errors.New("semantic_frame_mismatch")
	}
	if target.FrameURL != "" && target.FrameURL != decision.FrameURL {
		return bound, errors.New("semantic_frame_mismatch")
	}
	if decision.ObservationMode == "legacy" {
		return bound, errors.New("semantic_legacy_binding_requires_compact")
	}
	bound.decision = *decision
	bound.route.FrameID, bound.route.SessionID, bound.route.DocumentGeneration = decision.FrameID, decision.SessionID, decision.DocumentGeneration
	bound.route.Generation, bound.route.ExpectedFrameURL = decision.Generation, decision.FrameURL
	// AX names and roles are authoritative for a semantic binding. The renderer
	// implements only an approximation for exact DOM targets, so do not turn its
	// differing accessible-name calculation into a rejection of an AX match.
	if target.Name != "" && strings.Join(strings.Fields(target.Name), " ") != strings.Join(strings.Fields(decision.Name), " ") {
		return bound, errors.New("semantic_name_mismatch")
	}
	if target.Role != "" && target.Role != decision.Role {
		return bound, errors.New("semantic_role_mismatch")
	}
	var resolved struct {
		Object struct {
			ID string `json:"objectId"`
		} `json:"object"`
	}
	err := engine.cdpRoute(ctx, tab, bound.route, "DOM.resolveNode", map[string]any{"backendNodeId": decision.BackendDOMNodeID, "objectGroup": engine.objectGroup}, &resolved)
	if err != nil || resolved.Object.ID == "" {
		return bound, errors.New("semantic_target_stale")
	}
	bound.objectID = resolved.Object.ID
	engine.routes[bound.route.FrameID+"\x00"+bound.route.SessionID] = bound.route
	return bound, nil
}

// conditionGroups keeps root URL predicates in the root context and element
// predicates in their declared frame, defaulting to the action's frame.
func (engine *Engine) conditionGroups(page string, conditions []Condition, base browserrpc.Route) []struct {
	route      browserrpc.Route
	conditions []Condition
} {
	type group = struct {
		route      browserrpc.Route
		conditions []Condition
	}
	root := engine.route(page, nil)
	groups := []group{{route: root, conditions: []Condition{{URL: page}}}}
	indices := map[string]int{"": 0}
	for _, condition := range conditions {
		if condition.URL != "" {
			groups[0].conditions = append(groups[0].conditions, Condition{URL: condition.URL})
		}
		if condition.Target == nil {
			continue
		}
		condition.URL = ""
		route := engine.route(page, nil)
		route.FrameID = base.FrameID
		if condition.Target.FrameID != "" {
			route.FrameID = condition.Target.FrameID
		}
		route.ExpectedFrameURL = condition.Target.FrameURL
		key := route.FrameID + "\x00" + route.ExpectedFrameURL
		if route.FrameID == "" && route.ExpectedFrameURL == "" {
			key = ""
		}
		index, ok := indices[key]
		if !ok {
			index = len(groups)
			indices[key] = index
			groups = append(groups, group{route: route})
		}
		groups[index].conditions = append(groups[index].conditions, condition)
	}
	return groups
}

func (engine *Engine) conditions(ctx context.Context, tab int, page string, conditions []Condition, base browserrpc.Route, id string) (bool, error) {
	for _, group := range engine.conditionGroups(page, conditions, base) {
		step := Step{ID: id, Action: "wait", After: group.conditions}
		result, err := engine.invoke(ctx, tab, runtimeSpec{URL: page, Operation: "check_conditions", Step: step}, binding{route: group.route})
		if err != nil {
			return false, err
		}
		if result.Status == "failed" {
			return false, errors.New(result.Code)
		}
		if result.Status != "verified" {
			return false, nil
		}
	}
	return true, nil
}

// A semantic goal without a frame id can resolve to any observed frame. Its
// default-frame skip predicates must wait for that resolution, including when
// the caller supplied a unique frame URL instead of an opaque frame id.
func skipNeedsBinding(step Step) bool {
	if step.Target == nil || step.Target.Goal == "" || step.Target.FrameID != "" {
		return false
	}
	for _, condition := range step.SkipIf {
		if condition.Target != nil && condition.Target.FrameID == "" {
			return true
		}
	}
	return false
}

func (engine *Engine) skip(ctx context.Context, tab int, page string, step Step, base browserrpc.Route) (bool, error) {
	conditions := append([]Condition(nil), step.SkipIf...)
	for index := range conditions {
		target := conditions[index].Target
		if target == nil || target.FrameID != "" && target.FrameID != base.FrameID {
			continue
		}
		copy := *target
		copy.FrameID = base.FrameID
		if copy.FrameURL == "" {
			copy.FrameURL = base.ExpectedFrameURL
		}
		conditions[index].Target = &copy
	}
	return engine.conditions(ctx, tab, page, conditions, base, step.ID)
}

// The renderer rejects already-satisfied outcomes in its own document. A
// declared outcome in another context needs the equivalent routed check before
// observers are armed; otherwise a pre-existing result could be latched as proof
// that this action worked.
func routedOutcome(step Step, route browserrpc.Route) bool {
	for _, condition := range step.After {
		if condition.URL != "" && route.FrameID != "" {
			return true
		}
		if condition.Target != nil && condition.Target.FrameID != "" && condition.Target.FrameID != route.FrameID {
			return true
		}
	}
	return false
}

func (engine *Engine) arm(ctx context.Context, tab int, page, nextPage string, step Step, base browserrpc.Route) {
	for _, group := range engine.conditionGroups(nextPage, step.After, base) {
		// The expected outcome may live in a future document. Arm any currently
		// available contexts before dispatch; the read-only waiter also handles
		// new documents and frames that did not exist at dispatch time.
		group.route.ExpectedRootURL, group.route.ExpectedFrameURL = page, ""
		observer := Step{ID: step.ID, Action: "wait", After: group.conditions}
		_, _ = engine.invoke(ctx, tab, runtimeSpec{URL: page, Operation: "arm_conditions", Step: observer, TimeoutMS: step.TimeoutMS}, binding{route: group.route})
	}
}

func (engine *Engine) wait(ctx context.Context, tab int, page string, step Step, base browserrpc.Route) StepResult {
	timeout := step.TimeoutMS
	if timeout == 0 {
		timeout = 10000
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()
	// Only observations are retried. Resolve each frame's current document after
	// expected navigation instead of reusing the pre-action execution context.
	for {
		matched, err := engine.conditions(waitCtx, tab, page, step.After, base, step.ID)
		if err == nil && matched {
			return StepResult{Status: "verified", Evidence: "postcondition"}
		}
		select {
		case <-waitCtx.Done():
			return StepResult{Status: "failed", Code: "postcondition_timeout"}
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (engine *Engine) fresh(ctx context.Context, tab int, selection Selection) (bool, error) {
	if selection.Status != "selected" {
		return true, nil
	}
	if selection.ObservationMode == "legacy" {
		return false, errors.New("semantic_legacy_binding_requires_compact")
	}
	if selection.SnapshotFingerprint == "" || selection.Generation == "" {
		return false, errors.New("semantic_binding_incomplete")
	}
	kind := selection.Kind
	if kind == "" {
		kind = "controls"
	}
	var response struct {
		Fresh bool `json:"fresh"`
	}
	err := engine.call(ctx, "browser.validate", map[string]any{
		"tab_id": tab, "owner": engine.lease.Owner, "lease_id": engine.lease.LeaseID,
		"kind": kind, "origin": selection.Origin, "fingerprint": selection.SnapshotFingerprint, "generation": selection.Generation,
		"frame_id": selection.ScopeFrameID, "frame_url": selection.ScopeFrameURL,
		"root_backend_dom_node_id": selection.ScopeBackendDOMNodeID,
	}, &response)
	return response.Fresh, err
}

func (engine *Engine) selectTargets(ctx context.Context, targets []Target, page string, report *Report) ([]Selection, error) {
	report.SemanticSelections += len(targets)
	goals := make([]string, len(targets))
	for i, target := range targets {
		goals[i] = target.Goal
	}
	if len(targets) > 1 && (engine.SelectBatch != nil || engine.SelectBatchScoped != nil) {
		report.SemanticBatches++
		var selections []Selection
		var err error
		if engine.SelectBatchScoped != nil {
			selections, err = engine.SelectBatchScoped(ctx, targets, page)
		} else {
			selections, err = engine.SelectBatch(ctx, goals, page)
		}
		if err == nil && len(selections) != len(targets) {
			err = errors.New("invalid semantic batch response")
		}
		return selections, err
	}
	selections := make([]Selection, 0, len(targets))
	for i, goal := range goals {
		var selection Selection
		var err error
		if engine.SelectScoped != nil {
			selection, err = engine.SelectScoped(ctx, targets[i], page)
		} else if engine.SelectBatchScoped != nil {
			var batch []Selection
			batch, err = engine.SelectBatchScoped(ctx, targets[i:i+1], page)
			if err == nil && len(batch) != 1 {
				err = errors.New("invalid semantic batch response")
			}
			if err == nil {
				selection = batch[0]
			}
		} else if engine.Select != nil {
			selection, err = engine.Select(ctx, goal, page)
		} else if engine.SelectBatch != nil {
			var batch []Selection
			batch, err = engine.SelectBatch(ctx, []string{goal}, page)
			if err == nil && len(batch) != 1 {
				err = errors.New("invalid semantic batch response")
			}
			if err == nil {
				selection = batch[0]
			}
		} else {
			err = errors.New("semantic selection unavailable")
		}
		if err != nil {
			return nil, err
		}
		selections = append(selections, selection)
	}
	return selections, nil
}

// acquire establishes this run's object group and exclusive execution lease.
func (engine *Engine) acquire(ctx context.Context, tab int) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return errors.New("cannot establish interaction identity")
	}
	engine.objectGroup = "rep-interaction-" + hex.EncodeToString(nonce[:])
	owner := engine.Owner
	if owner == "" {
		owner = engine.objectGroup
	}
	lease, err := browserrpc.Acquire(ctx, engine, tab, owner, "execute")
	if err != nil {
		return errors.New("cannot acquire interaction lease: " + err.Error())
	}
	engine.lease = lease
	return nil
}

// release frees bound objects and the lease on a fresh deadline.
func (engine *Engine) release(tab int) error {
	cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, route := range engine.routes {
		var ignored any
		// Cleanup is scoped by the lease and session; stale documents may have
		// already released the object group. Never detach another observer.
		route.ExpectedRootURL, route.ExpectedFrameURL = "", ""
		_ = engine.cdpRoute(cleanup, tab, route, "Runtime.releaseObjectGroup", map[string]any{"objectGroup": engine.objectGroup}, &ignored)
	}
	err := browserrpc.Release(cleanup, engine, tab, engine.lease)
	engine.lease = browserrpc.Lease{}
	if err != nil {
		return errors.New("interaction finished but lease cleanup failed")
	}
	return nil
}

// RunDecision performs one click or fill on a target already chosen by a
// semantic decision, such as a step decision. It applies the same lease,
// binding, freshness, frame geometry, and renderer guards as Run. Without a
// declared application outcome a click reports "performed"; the caller must
// verify its effect. Text entry is verified by exact read-back.
func (engine *Engine) RunDecision(ctx context.Context, tab int, page string, step Step, decision Selection) (result StepResult, err error) {
	started := time.Now()
	engine.calls = 0
	engine.routes = map[string]browserrpc.Route{}
	defer func() { result.ID, result.DurationMS = step.ID, time.Since(started).Milliseconds() }()
	if tab < 0 || engine.Browser == nil || step.Target == nil || step.Target.Goal == "" || (step.Action != "click" && step.Action != "fill" && step.Action != "choose") || len(step.After) > 0 {
		return StepResult{Status: "failed", Code: "invalid_decision_step"}, errors.New("a decision step needs a semantic click, fill, or choose target and no plan conditions")
	}
	if step.Action == "fill" && (step.Value == nil || len(*step.Value) > 32768) {
		return StepResult{Status: "failed", Code: "invalid_decision_step"}, errors.New("fill requires a value up to 32 KiB")
	}
	if step.Action == "choose" && (len(step.Values) != 1 || step.Values[0] == "" || len(step.Values[0]) > 10000) {
		return StepResult{Status: "failed", Code: "invalid_decision_step"}, errors.New("choose requires exactly one option label")
	}
	if err = engine.acquire(ctx, tab); err != nil {
		return StepResult{Status: "failed", Code: "lease_unavailable"}, err
	}
	defer func() {
		if cleanupErr := engine.release(tab); cleanupErr != nil && err == nil {
			err = cleanupErr
		}
	}()
	bound, bindErr := engine.bind(ctx, tab, step.Target, page, &decision)
	if bindErr != nil {
		return StepResult{Status: "failed", Code: bindErr.Error()}, errors.New("target binding was not verified; no action performed")
	}
	if bound.route.FrameID != "" {
		geometry, geometryErr := engine.invoke(ctx, tab, runtimeSpec{URL: page, Operation: "prepare_geometry", Step: step}, bound)
		if geometryErr != nil || geometry.Status != "ready" || geometry.Point == nil {
			code := "frame_geometry_unavailable"
			if geometry.Code != "" {
				code = geometry.Code
			}
			return StepResult{Status: "failed", Code: code}, nil
		}
		bound.route.FramePoint = geometry.Point
	}
	if fresh, freshErr := engine.fresh(ctx, tab, decision); freshErr != nil || !fresh {
		return StepResult{Status: "failed", Code: "semantic_stale_before_action"}, nil
	}
	result, runErr := engine.invoke(ctx, tab, runtimeSpec{URL: page, Operation: "perform", Step: step, TimeoutMS: step.TimeoutMS}, bound)
	if runErr != nil {
		result = StepResult{Status: "failed", Code: "renderer_unavailable"}
		if code, rejected := preDispatchRejection(runErr); rejected {
			result.Code = code
		} else {
			result.Status, result.Attempted, result.Code = "unconfirmed", true, "action_outcome_unknown"
		}
		return result, nil
	}
	if step.Action == "click" && result.Status == "verified" {
		// The renderer's default evidence is input read-back, which a click does
		// not have: it was dispatched, and its outcome is unverified.
		result.Status, result.Evidence = "performed", "dispatch"
	}
	return result, nil
}

func (engine *Engine) Run(ctx context.Context, tab int, plan Plan, apply bool) (report Report, err error) {
	started := time.Now()
	engine.calls = 0
	engine.routes = map[string]browserrpc.Route{}
	report = Report{Version: 1, Status: "failed", Applied: apply, Steps: []StepResult{}}
	defer func() { report.DurationMS = time.Since(started).Milliseconds(); report.BridgeCalls = engine.calls }()
	if err = plan.Validate(); err != nil {
		return report, err
	}
	if tab < 0 || engine.Browser == nil {
		return report, errors.New("browser and tab are required")
	}
	if err = engine.acquire(ctx, tab); err != nil {
		return report, err
	}
	defer func() {
		if cleanupErr := engine.release(tab); cleanupErr != nil {
			report.Status = "cleanup_failed"
			if err == nil {
				err = cleanupErr
			}
		}
	}()
	page := plan.URL
	pending := map[int]Selection{}
	cache := map[string]Selection{}
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
		base := engine.route(page, step.Target)
		deferredSkip := skipNeedsBinding(step)
		if len(step.SkipIf) > 0 && !deferredSkip {
			matched, skipErr := engine.skip(ctx, tab, page, step, base)
			if skipErr != nil {
				report.Steps = append(report.Steps, StepResult{ID: step.ID, Status: "failed", Code: "skip_check_failed"})
				return report, errors.New("skip condition could not be checked")
			}
			if matched {
				report.Steps = append(report.Steps, StepResult{ID: step.ID, Status: "skipped", Evidence: "skip_condition"})
				continue
			}
		}
		var decision *Selection
		var bindErr error
		if step.Target != nil && step.Target.Goal != "" {
			key := page + "\x00" + step.Target.Goal + "\x00" + step.Target.FrameID + "\x00" + step.Target.FrameURL + "\x00" + strconv.FormatInt(step.Target.ScopeBackendDOMNodeID, 10)
			selection, reuse := pending[index]
			if !reuse {
				selection, reuse = cache[key]
			}
			// A cached binding is only reused after a synchronous semantic-scope
			// refresh, including relevant competing targets and row relationships.
			if reuse {
				fresh, checkErr := engine.fresh(ctx, tab, selection)
				if checkErr != nil {
					bindErr = errors.New("semantic_dependency_check_failed")
				} else if !fresh {
					reuse = false
					pending = map[int]Selection{}
					cache = map[string]Selection{}
				} else {
					report.BindingReuses++
				}
			}
			if !reuse && bindErr == nil {
				targets := []Target{*step.Target}
				positions := [][]int{{index}}
				targetKey := func(target *Target) string {
					return target.Goal + "\x00" + target.FrameID + "\x00" + target.FrameURL + "\x00" + strconv.FormatInt(target.ScopeBackendDOMNodeID, 10)
				}
				byGoal := map[string]int{targetKey(step.Target): 0}
				if apply && step.Batch != "" && (engine.SelectBatch != nil || engine.SelectBatchScoped != nil) {
					for next := index + 1; next < len(plan.Steps) && plan.Steps[next].Batch == step.Batch; next++ {
						target := plan.Steps[next].Target
						if target == nil || target.Goal == "" {
							continue
						}
						position, exists := byGoal[targetKey(target)]
						if !exists {
							position = len(targets)
							byGoal[targetKey(target)] = position
							targets = append(targets, *target)
							positions = append(positions, nil)
						}
						positions[position] = append(positions[position], next)
					}
				}
				selections, selectionErr := engine.selectTargets(ctx, targets, page, &report)
				if selectionErr != nil {
					bindErr = errors.New("semantic_selection_failed")
				} else {
					for i, selected := range selections {
						for _, position := range positions[i] {
							pending[position] = selected
						}
					}
					selection = pending[index]
				}
			}
			decision = &selection
			if bindErr == nil && selection.Status == "selected" {
				cache[key] = selection
			}
		}
		var bound binding
		if bindErr == nil {
			bound, bindErr = engine.bind(ctx, tab, step.Target, page, decision)
		}
		if bindErr != nil {
			report.Steps = append(report.Steps, StepResult{ID: step.ID, Status: "failed", Code: bindErr.Error(), DurationMS: time.Since(stepStart).Milliseconds()})
			return report, errors.New("target binding was not verified; no action performed")
		}
		if deferredSkip {
			matched, skipErr := engine.skip(ctx, tab, page, step, bound.route)
			if skipErr != nil {
				report.Steps = append(report.Steps, StepResult{ID: step.ID, Status: "failed", Code: "skip_check_failed"})
				return report, errors.New("skip condition could not be checked")
			}
			if matched {
				report.Steps = append(report.Steps, StepResult{ID: step.ID, Status: "skipped", Evidence: "skip_condition"})
				continue
			}
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
			result = engine.wait(ctx, tab, nextPage, step, bound.route)
		} else {
			if apply && routedOutcome(step, bound.route) {
				matched, checkErr := engine.conditions(ctx, tab, page, step.After, bound.route, step.ID+":before")
				if checkErr != nil {
					// A future frame or its expected future URL cannot already satisfy
					// this outcome. Other failed reads leave the precondition unknown.
					code, rejected := preDispatchRejection(checkErr)
					if !rejected || (code != "page_changed" && code != "stale_frame") {
						result = StepResult{Status: "failed", Code: "postcondition_check_failed"}
					}
				} else if matched {
					wouldChange := step.Action == "click" || step.Action == "press"
					if !wouldChange {
						preview, previewErr := engine.invoke(ctx, tab, runtimeSpec{URL: page, Operation: "preview", Step: step}, bound)
						if previewErr != nil || preview.Status != "ready" {
							result = StepResult{Status: "failed", Code: "postcondition_check_failed"}
						} else {
							wouldChange = preview.WouldChange
							if !wouldChange {
								// The desired input and declared outcome were both
								// observed. Finish without dispatching a mutation whose
								// effect an already-true outcome could not establish.
								result = StepResult{Status: "verified", Evidence: "input_readback"}
							}
						}
					}
					if wouldChange {
						result = StepResult{Status: "failed", Code: "postcondition_already_satisfied"}
					}
				}
			}
			if result.Status == "" && apply && len(step.After) > 0 {
				engine.arm(ctx, tab, page, nextPage, step, bound.route)
			}
			if result.Status == "" && apply && bound.route.FrameID != "" {
				geometry, geometryErr := engine.invoke(ctx, tab, runtimeSpec{URL: page, Operation: "prepare_geometry", Step: step}, bound)
				if geometryErr == nil && geometry.Status == "skipped" {
					result = geometry
				} else if geometryErr != nil || geometry.Status != "ready" || geometry.Point == nil {
					result = StepResult{Status: "failed", Code: "frame_geometry_unavailable"}
					if geometry.Code != "" {
						result.Code = geometry.Code
					}
				} else {
					bound.route.FramePoint = geometry.Point
				}
			}
			if (result.Status == "" || result.Status == "verified") && decision != nil {
				fresh, checkErr := engine.fresh(ctx, tab, *decision)
				if checkErr != nil || !fresh {
					result = StepResult{Status: "failed", Code: "semantic_stale_before_action"}
				}
			}
			if result.Status == "" {
				result, runErr = engine.invoke(ctx, tab, runtimeSpec{URL: page, Operation: operation, Step: step, TimeoutMS: step.TimeoutMS}, bound)
			}
		}
		if runErr != nil {
			result = StepResult{Status: "failed", Code: "renderer_unavailable"}
			if code, rejected := preDispatchRejection(runErr); rejected {
				result.Code = code
			} else if apply && step.Action != "wait" {
				result.Status = "unconfirmed"
				result.Attempted = true
				result.Code = "action_outcome_unknown"
			}
		}
		if apply && result.Status == "prepared" && step.Action == "press" {
			// A typed pre-dispatch rejection is definite only while no earlier key
			// may have reached the page. Once any key was sent, never suggest replay.
			keyMayHaveDispatched := false
			for _, key := range step.Keys {
				guard, guardErr := engine.invoke(ctx, tab, runtimeSpec{URL: page, Operation: "focus_guard", Step: step}, bound)
				if guardErr != nil || guard.Status != "verified" {
					result.Status = "failed"
					if keyMayHaveDispatched {
						result.Status = "unconfirmed"
					}
					result.Code = "focus_changed"
					break
				}
				event, _ := KeyEvent(key)
				for _, kind := range []string{"keyDown", "keyUp"} {
					event["type"] = kind
					var ignored any
					if keyErr := engine.cdpRoute(ctx, tab, bound.route, "Input.dispatchKeyEvent", event, &ignored); keyErr != nil {
						if code, rejected := preDispatchRejection(keyErr); rejected && !keyMayHaveDispatched {
							result.Status, result.Code, result.Attempted = "failed", code, false
							break
						}
						keyMayHaveDispatched, result.Attempted = true, true
						result.Status = "unconfirmed"
						result.Code = "key_outcome_unknown"
						// Attempt paired release even if keyDown's reply is lost;
						// a release is never a retry of the mutation.
					} else {
						keyMayHaveDispatched, result.Attempted = true, true
					}
				}
				if result.Status == "unconfirmed" || result.Status == "failed" {
					break
				}
			}
			if result.Status == "prepared" {
				result.Status = "performed"
				result.Changed = true
			}
		}
		if apply && result.Status == "performed" {
			checked := engine.wait(ctx, tab, nextPage, step, bound.route)
			result.Evidence, result.Code, result.Status = checked.Evidence, checked.Code, checked.Status
			if result.Status != "verified" {
				result.Status = "unconfirmed"
			}
		}
		result.ID, result.DurationMS = step.ID, time.Since(stepStart).Milliseconds()
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

// Only a typed bridge response proving that the requested command was never
// dispatched can turn an RPC error into a definite rejection. Lost replies and
// renderer failures remain unknown; they must never invite a mutation retry.
func preDispatchRejection(err error) (string, bool) {
	var rpc *bridge.RPCError
	if !errors.As(err, &rpc) {
		return "", false
	}
	data, ok := rpc.Data.(map[string]any)
	if !ok || data["phase"] != "before_dispatch" {
		return "", false
	}
	if rpc.Code == "" {
		return "browser_guard_failed", true
	}
	return rpc.Code, true
}
