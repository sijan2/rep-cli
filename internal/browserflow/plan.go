// Package browserflow executes bounded, explicitly supplied UI steps through Rep's bridge.
// Jev may choose an observed target; it never supplies code, values, or success criteria.
package browserflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

const MaxPlanBytes = 256 * 1024

type Scope struct {
	Selector     string `json:"selector"`
	Text         string `json:"text,omitempty"`
	TextSelector string `json:"text_selector,omitempty"`
}
type Target struct {
	Selector string `json:"selector,omitempty"`
	Name     string `json:"name,omitempty"`
	Role     string `json:"role,omitempty"`
	Within   *Scope `json:"within,omitempty"`
	Goal     string `json:"goal,omitempty"`
}
type Condition struct {
	URL     string  `json:"url,omitempty"`
	Target  *Target `json:"target,omitempty"`
	Text    *string `json:"text,omitempty"`
	Value   *string `json:"value,omitempty"`
	Checked *bool   `json:"checked,omitempty"`
	Visible *bool   `json:"visible,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
	Absent  bool    `json:"absent,omitempty"`
}
type Step struct {
	ID        string      `json:"id"`
	Action    string      `json:"action"`
	Target    *Target     `json:"target,omitempty"`
	Value     *string     `json:"value,omitempty"`
	Values    []string    `json:"values,omitempty"`
	Checked   *bool       `json:"checked,omitempty"`
	Old       string      `json:"old,omitempty"`
	Replace   bool        `json:"replace,omitempty"`
	Keys      []string    `json:"keys,omitempty"`
	After     []Condition `json:"after,omitempty"`
	SkipIf    []Condition `json:"skip_if,omitempty"`
	TimeoutMS int         `json:"timeout_ms,omitempty"`
}
type Plan struct {
	Version int    `json:"version"`
	URL     string `json:"url"`
	Steps   []Step `json:"steps"`
}

func Decode(reader io.Reader) (Plan, error) {
	var plan Plan
	data, err := io.ReadAll(io.LimitReader(reader, MaxPlanBytes+1))
	if err != nil {
		return plan, err
	}
	if len(data) > MaxPlanBytes {
		return plan, errors.New("interaction plan exceeds 256 KiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&plan); err != nil {
		return plan, errors.New("invalid interaction plan JSON or unknown field")
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return plan, errors.New("interaction plan must contain one JSON object")
	}
	return plan, plan.Validate()
}
func validURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && len(raw) <= 8192
}
func (target Target) validate(semantic bool) error {
	if target.Goal != "" {
		if !semantic || len(target.Goal) > 2000 || strings.TrimSpace(target.Goal) == "" || target.Selector != "" || target.Within != nil {
			return errors.New("goal targets cannot combine selectors/scopes or be used in conditions")
		}
	} else if target.Selector == "" && target.Name == "" {
		return errors.New("target requires an exact name, selector, or goal")
	}
	if len(target.Selector) > 2000 || len(target.Name) > 10000 || len(target.Role) > 100 {
		return errors.New("target exceeds its bounds")
	}
	if target.Within != nil && (target.Within.Selector == "" || len(target.Within.Selector) > 2000 || len(target.Within.Text) > 10000 || len(target.Within.TextSelector) > 2000) {
		return errors.New("within requires a bounded selector and optional exact text")
	}
	return nil
}
func conditionsValid(conditions []Condition) error {
	if len(conditions) > 16 {
		return errors.New("at most 16 conditions are supported")
	}
	requiredURL := ""
	for _, condition := range conditions {
		if condition.URL != "" {
			if requiredURL != "" && requiredURL != condition.URL {
				return errors.New("conditions require conflicting URLs")
			}
			requiredURL = condition.URL
		}
		if condition.URL != "" && !validURL(condition.URL) {
			return errors.New("condition URL must be absolute HTTP(S)")
		}
		state := condition.Text != nil || condition.Value != nil || condition.Checked != nil || condition.Visible != nil || condition.Enabled != nil || condition.Absent
		if condition.Target == nil {
			if condition.URL == "" || state {
				return errors.New("condition needs a target or URL")
			}
		} else {
			if err := condition.Target.validate(false); err != nil {
				return err
			}
			if !state {
				return errors.New("target condition requires an explicit state")
			}
		}
		if condition.Absent && (condition.Text != nil || condition.Value != nil || condition.Checked != nil || condition.Visible != nil || condition.Enabled != nil) {
			return errors.New("absent cannot combine with element states")
		}
		for _, text := range []*string{condition.Text, condition.Value} {
			if text != nil && len(*text) > 32768 {
				return errors.New("condition text exceeds 32 KiB")
			}
		}
	}
	return nil
}
func (plan Plan) Validate() error {
	if plan.Version != 1 || !validURL(plan.URL) {
		return errors.New("version 1 and an exact HTTP(S) starting URL are required")
	}
	if len(plan.Steps) < 1 || len(plan.Steps) > 100 {
		return errors.New("provide 1 to 100 interaction steps")
	}
	ids := map[string]bool{}
	for _, step := range plan.Steps {
		if strings.TrimSpace(step.ID) == "" || len(step.ID) > 100 || ids[step.ID] {
			return errors.New("each step needs a unique, bounded id")
		}
		ids[step.ID] = true
		fail := func(message string) error { return fmt.Errorf("step %s: %s", step.ID, message) }
		switch step.Action {
		case "fill", "replace", "choose", "check", "click", "press", "wait":
		default:
			return fail("unsupported action")
		}
		if step.TimeoutMS < 0 || step.TimeoutMS > 30000 {
			return fail("timeout_ms must be 0 to 30000")
		}
		if step.Action == "wait" {
			if step.Target != nil {
				return fail("wait uses after conditions, not a target")
			}
		} else {
			if step.Target == nil {
				return fail("action target required")
			}
			if err := step.Target.validate(true); err != nil {
				return fail(err.Error())
			}
		}
		if err := conditionsValid(step.After); err != nil {
			return fail(err.Error())
		}
		if err := conditionsValid(step.SkipIf); err != nil {
			return fail(err.Error())
		}
		if (step.Action == "click" || step.Action == "press" || step.Action == "wait") && len(step.After) == 0 {
			return fail("click, press, and wait require explicit after conditions")
		}
		if (step.Action == "fill" || step.Action == "replace") && (step.Value == nil || len(*step.Value) > 32768) {
			return fail("fill/replace requires value up to 32 KiB")
		}
		if step.Action == "replace" && (step.Old == "" || len(step.Old) > 32768) {
			return fail("replace requires a unique nonempty old substring")
		}
		if step.Action == "choose" {
			if len(step.Values) < 1 || len(step.Values) > 100 {
				return fail("choose requires option labels")
			}
			seen := map[string]bool{}
			for _, value := range step.Values {
				if value == "" || len(value) > 10000 || seen[value] {
					return fail("invalid or duplicate option label")
				}
				seen[value] = true
			}
		}
		if step.Action == "check" && step.Checked == nil {
			return fail("check requires checked: true or false")
		}
		if step.Action == "press" {
			if len(step.Keys) < 1 || len(step.Keys) > 32 {
				return fail("press requires 1 to 32 supported keys")
			}
			for _, key := range step.Keys {
				if _, ok := KeyEvent(key); !ok {
					return fail("unsupported key")
				}
			}
		}
		if step.Action != "fill" && step.Action != "replace" && (step.Value != nil || step.Replace) {
			return fail("value/replace only apply to text edits")
		}
		if step.Action != "replace" && step.Old != "" {
			return fail("old only applies to replace")
		}
		if step.Action != "choose" && len(step.Values) > 0 {
			return fail("values only applies to choose")
		}
		if step.Action != "check" && step.Checked != nil {
			return fail("checked only applies to check")
		}
		if step.Action != "press" && len(step.Keys) > 0 {
			return fail("keys only applies to press")
		}
	}
	return nil
}
func KeyEvent(key string) (map[string]any, bool) {
	codes := map[string]int{"Enter": 13, "Tab": 9, "Escape": 27, "Space": 32, "ArrowLeft": 37, "ArrowUp": 38, "ArrowRight": 39, "ArrowDown": 40, "Backspace": 8, "Delete": 46, "Home": 36, "End": 35}
	code, ok := codes[key]
	if !ok {
		return nil, false
	}
	name := key
	if key == "Space" {
		name = " "
	}
	return map[string]any{"key": name, "code": key, "windowsVirtualKeyCode": code}, true
}
