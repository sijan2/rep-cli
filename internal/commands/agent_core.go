package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/headless"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

type coreError struct {
	Code       string   `json:"code"`
	Message    string   `json:"message"`
	Next       string   `json:"next"`
	Retry      string   `json:"retry"`
	Candidates []string `json:"candidates,omitempty"`
}
type coreResult struct {
	Task           string     `json:"task"`
	TaskSource     string     `json:"task_source"`
	Browser        string     `json:"browser,omitempty"`
	Profile        string     `json:"profile_id,omitempty"`
	Tab            string     `json:"tab,omitempty"`
	Viewport       string     `json:"viewport,omitempty"`
	Status         string     `json:"status,omitempty"`
	Complete       *bool      `json:"complete,omitempty"`
	Next           string     `json:"next,omitempty"`
	PageState      any        `json:"page_state,omitempty"`
	ContentChanged bool       `json:"content_changed,omitempty"`
	Error          *coreError `json:"error,omitempty"`
	Meta           any        `json:"meta,omitempty"`
	Untrusted      any        `json:"untrusted,omitempty"`
}

func coreNext(verb string, handles ...string) string {
	args := append([]string{"rep", verb}, handles...)
	if selected, ok := scope.Configured(); ok && verb != "" && !strings.HasPrefix(verb, "-") && (selected.TaskSource == "flag" || selected.TaskSource == "handle" && !coreHasHandle(handles)) && !selected.ExplicitGlobal {
		args = append(args, "--task", selected.Identity())
	}
	return quoteCommand(args...)
}

// Repair and inventory commands have no handle positional. Preserve a
// handle-derived task when their continuation cannot carry that capability.
func coreHasHandle(args []string) bool {
	for _, arg := range args {
		if agentstate.TabOf(arg) != "" || agentstate.CursorPattern.MatchString(arg) || strings.HasPrefix(arg, "h_") || strings.HasPrefix(arg, "s_") {
			return true
		}
	}
	return false
}
func emitCore(command *cobra.Command, result coreResult) error {
	attachCoreCapture(command, &result)
	selected, _ := scope.Current()
	result.Task, result.TaskSource = selected.Identity(), selected.TaskSource
	if result.Untrusted != nil {
		encoded, err := json.Marshal(result.Untrusted)
		if err != nil {
			return err
		}
		result.Untrusted = output.RedactValue(decodeObject(encoded))
	}
	return output.WriteJSON(command.OutOrStdout(), result)
}
func coreFailure(command *cobra.Command, err error) error {
	if output.Reported(err) {
		return err
	}
	problem := coreError{Code: "command_failed", Message: "operation failed", Next: coreNext("doctor"), Retry: "needs-agent"}
	var stateError *agentstate.Error
	var agentError output.AgentError
	var rpcError *bridge.RPCError
	var transportError *bridge.TransportError
	switch {
	case bridge.PossibleDispatch(err):
		problem.Code, problem.Message, problem.Retry = "outcome_unknown", "the browser may have executed the request; verify its state before acting again", "do-not-retry"
		problem.Next = coreVerificationNext(command)
	case errors.As(err, &transportError):
		problem.Code, problem.Message = "browser_unavailable", "the browser bridge is unavailable; nothing was dispatched"
	case errors.As(err, &stateError):
		problem = coreError{stateError.Code, stateError.Message, stateError.Next, stateError.Retry, stateError.Candidates}
	case errors.As(err, &agentError):
		problem.Code, problem.Message, problem.Next = agentError.Code, agentError.Message, agentError.Next
	case errors.As(err, &rpcError):
		problem.Code = rpcError.Code
		problem.Message = "browser rejected the operation"
		if rpcError.Code == "observation_capacity" || rpcError.Code == "tab_busy" || rpcError.Code == "capture_busy" || rpcError.Code == "capture_queue_timeout" {
			problem.Retry = "retryable"
		}
	default:
		problem.Message = "browser or local operation unavailable"
	}
	if problem.Next == "" {
		problem.Next = coreNext("tabs")
	}
	if problem.Retry == "" {
		problem.Retry = "needs-agent"
	}
	nextArgs := strings.Fields(problem.Next)
	if selected, ok := scope.Configured(); ok && len(nextArgs) > 1 && !strings.HasPrefix(nextArgs[1], "-") && (selected.TaskSource == "flag" || selected.TaskSource == "handle" && !coreHasHandle(nextArgs)) && !strings.Contains(problem.Next, "--task") {
		problem.Next += " --task " + quoteCommand(selected.Identity())
	}
	result := coreResult{Error: &problem}
	if state, e := coreStore(); e == nil {
		selected, _ := scope.Current()
		if browser, e := state.Remembered(selected.Identity()); e == nil && browser != nil {
			result.Browser, result.Profile = browser.Name, browser.Profile
		}
	}
	if writeErr := emitCore(command, result); writeErr != nil {
		return writeErr
	}
	return output.MarkReported(output.NewAgentError(problem.Code, command.CommandPath(), problem.Message))
}
func coreArgument(message string) error {
	return &agentstate.Error{Code: "invalid_argument", Message: message, Next: "rep --help", Retry: "needs-agent"}
}
func coreStore() (*agentstate.Store, error) { return agentstate.Default() }
func invocationHandle(command *cobra.Command, args []string) string {
	var candidate string
	switch rootCommandName(command) {
	case "do":
		if len(args) > 0 {
			candidate = args[0]
		}
		if strings.HasPrefix(candidate, "@") && len(args) > 1 {
			candidate = args[len(args)-1]
		}
	case "page", "shot", "reference", "body", "close", "replay", "download":
		if len(args) > 0 {
			candidate = args[0]
		}
	case "eval", "cdp":
		if len(args) > 1 {
			candidate = args[len(args)-1]
		}
	case "net":
		for _, arg := range args {
			if strings.HasPrefix(arg, "s_") || agentstate.CursorPattern.MatchString(arg) {
				candidate = arg
				break
			}
		}
	}
	if agentstate.TabOf(candidate) != "" || agentstate.CursorPattern.MatchString(candidate) || strings.HasPrefix(candidate, "h_") || strings.HasPrefix(candidate, "s_") {
		return candidate
	}
	return ""
}
func browserIdentity(client *bridge.Client, name, privateName string) agentstate.Browser {
	return agentstate.Browser{Name: name, Profile: client.Registry.ProfileID, Epoch: client.Registry.Epoch, PrivateName: privateName}
}
func resolveCoreBrowserOnce(ctx context.Context, store *agentstate.Store, tab *agentstate.Tab) (*bridge.Client, agentstate.Browser, error) {
	selected, _ := scope.Current()
	remembered, err := store.Remembered(selected.Identity())
	if err != nil {
		return nil, agentstate.Browser{}, err
	}
	choice := remembered
	if tab != nil {
		choice = &tab.Browser
	}
	name, privateName := "", ""
	if choice != nil {
		name, privateName = choice.Name, choice.PrivateName
	} else {
		name = strings.TrimSpace(os.Getenv("REP_BROWSER"))
	}
	var client *bridge.Client
	if name == "headless" {
		if privateName == "" {
			privateName = strings.TrimSpace(os.Getenv("REP_PROFILE"))
			if privateName == "" {
				privateName = "default"
			}
		}
		dir, dirErr := headless.NamedProfileDir(privateName)
		if dirErr != nil {
			return nil, agentstate.Browser{}, dirErr
		}
		client, err = headless.Connection(ctx, dir)
	} else {
		registries, discoverErr := bridge.Discover()
		if discoverErr != nil {
			return nil, agentstate.Browser{}, discoverErr
		}
		if name == "" {
			if len(registries) == 1 {
				name = registries[0].Browser
			} else {
				name = "dia"
			}
		}
		var matches []*bridge.Client
		for _, registry := range registries {
			if choice != nil && choice.Profile != "" && registry.ProfileID != choice.Profile {
				continue
			}
			if (choice == nil || choice.Profile == "") && name != "any" && registry.Browser != name && registry.ProfileID != name {
				continue
			}
			candidate := &bridge.Client{Registry: registry}
			probe, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
			var pong any
			probeErr := candidate.Call(probe, "bridge.ping", nil, &pong)
			cancel()
			if probeErr == nil {
				matches = append(matches, candidate)
			}
		}
		if len(matches) > 1 {
			return nil, agentstate.Browser{}, agentstate.Failure("ambiguous_browser", "multiple profiles match; select a profile through REP_BROWSER", "rep doctor")
		}
		if len(matches) == 1 {
			client = matches[0]
			name = client.Registry.Browser
		} else {
			err = errors.New("no matching live profile")
		}
	}
	if err != nil || client == nil {
		return nil, agentstate.Browser{}, agentstate.Failure("browser_unavailable", "the selected browser is unavailable; no other profile was selected", "rep doctor")
	}
	identity := browserIdentity(client, name, privateName)
	if identity.Profile == "" || identity.Epoch == "" {
		return nil, identity, agentstate.Failure("bridge_outdated", "the extension does not report a profile identity", "rep doctor")
	}
	if choice != nil && choice.Profile != "" && (choice.Profile != identity.Profile || tab != nil && choice.Epoch != identity.Epoch) {
		return nil, identity, agentstate.Failure("tab_gone", "the browser profile or session changed", "rep tabs")
	}
	if err := store.ObserveEpoch(identity.Profile, identity.Epoch); err != nil {
		return nil, identity, err
	}
	if err := store.Remember(selected.Identity(), selected.SessionKey, identity); err != nil {
		return nil, identity, err
	}
	return client, identity, nil
}

// decodeObject preserves arbitrary precision in page JSON until output encoding.
func decodeObject(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil
	}
	return value
}
func boolPointer(value bool) *bool { return &value }
func coreTabResult(tab *agentstate.Tab) coreResult {
	return coreResult{Browser: tab.Browser.Name, Profile: tab.Browser.Profile, Tab: tab.Handle, Viewport: tab.Viewport}
}
func coreTimeout(command *cobra.Command, duration time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(command.Context(), duration)
}
func coreHTTPURL(value string) bool {
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}
func coreErrorf(code, format string, args ...any) error {
	return agentstate.Failure(code, fmt.Sprintf(format, args...), coreNext("tabs"))
}
