package commands

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

func coreSource(command *cobra.Command, value string) (string, error) {
	if !strings.HasPrefix(value, "@") {
		if len(value) > 256*1024 {
			return "", coreArgument("source exceeds 256 KiB")
		}
		return value, nil
	}
	var reader io.Reader = command.InOrStdin()
	if value != "@-" {
		file, err := os.Open(strings.TrimPrefix(value, "@"))
		if err != nil {
			return "", coreArgument("source file is unreadable")
		}
		defer file.Close()
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, 256*1024+1))
	if err != nil {
		return "", err
	}
	if len(data) > 256*1024 {
		return "", coreArgument("source exceeds 256 KiB")
	}
	return string(data), nil
}
func newCoreEvalCommand() *cobra.Command {
	var capture bool
	var timeout time.Duration
	command := &cobra.Command{Use: "eval JS|@FILE|@- [TAB]", Short: "Evaluate JavaScript", Args: cobra.RangeArgs(1, 2)}
	command.Long = "Awaits promises and supports scripts from files or stdin. Use an explicit tab in shared agent sessions. --out saves the raw JSON result, including credential fields, to a new file; stdout returns its path and digest. For HttpOnly cookies use rep cookies."
	command.Example = "  rep eval 'document.title' t3\n  rep eval @script.js t3 --capture\n  rep eval 'Object.fromEntries(Object.entries(localStorage))' t3 --out storage.json"
	command.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Execution deadline (1s–5m)")
	command.Flags().BoolVar(&capture, "capture", false, "Record traffic triggered by the script")
	command.Flags().String("out", "", "Export the raw result as JSON to a new file")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := coreEvaluate(cmd, args, timeout, false, capture); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}
func newCoreCDPCommand() *cobra.Command {
	command := &cobra.Command{Use: "cdp METHOD [JSON|@FILE] [TAB]", Short: "Run a guarded protocol call", Args: cobra.RangeArgs(1, 3)}
	command.Long = "Address an owned tab explicitly. Page, DOM, Runtime, Input, Emulation and related page methods are supported. Use rep cookies for HttpOnly session cookies and rep eval for localStorage; browser-global target management is outside this command. --out saves raw result JSON."
	command.Example = "  rep cdp Page.getLayoutMetrics t3\n  rep cdp Page.getLayoutMetrics t3 --out layout.json"
	command.Flags().String("out", "", "Export the raw result as JSON to a new file")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := coreEvaluate(cmd, args, 30*time.Second, true, false); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}

var coreMethodPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*\.[A-Za-z][A-Za-z0-9_]*$`)

func cdpAllowed(method string) bool {
	if !coreMethodPattern.MatchString(method) {
		return false
	}
	// The escape hatch can change page state, but cannot read credential stores,
	// other targets, or browser-global data. Network responses use the redacted
	// capture/body path instead.
	domain, _, ok := strings.Cut(method, ".")
	if !ok {
		return false
	}
	switch domain {
	case "Storage", "DOMStorage", "IndexedDB", "CacheStorage", "Network", "Fetch", "Target", "Browser", "SystemInfo", "IO", "ServiceWorker", "Security":
		return false
	}
	lower := strings.ToLower(method)
	if strings.Contains(lower, "cookie") || strings.Contains(lower, "storage") || strings.Contains(lower, "credential") {
		return false
	}
	switch domain {
	case "Runtime", "DOM", "Page", "Input", "Emulation", "CSS", "Accessibility", "Animation", "Performance", "Overlay":
		return true
	}
	return false
}
func coreEvaluate(command *cobra.Command, args []string, timeout time.Duration, protocol, capture bool) error {
	if command.Flags().Changed("out") {
		path, _ := command.Flags().GetString("out")
		if strings.TrimSpace(path) == "" {
			return coreArgument("--out requires a new file path")
		}
		// Catch destination collisions before running a possibly mutating script.
		// Exclusive creation in coreArtifact also protects against later races.
		if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
			return coreErrorf("artifact_exists_or_unwritable", "export destination already exists or is unavailable; no script was run")
		}
	}
	if timeout < time.Second || timeout > 5*time.Minute {
		return coreArgument("timeout must be between 1s and 5m")
	}
	selected, _ := scope.Current()
	store, err := coreStore()
	if err != nil {
		return err
	}
	target := ""
	if len(args) > 1 && agentstate.TabPattern.MatchString(args[len(args)-1]) {
		target = args[len(args)-1]
		args = args[:len(args)-1]
	}
	// Scripts can mutate the page. Same-session siblings share an identity, so
	// even a single current tab is not a safe implicit execution target.
	if !protocol && target == "" && selected.TaskSource != "flag" && strings.HasPrefix(selected.SessionKey, "agent-") {
		return agentstate.Failure("ambiguous_target", "address a tab before evaluating in a shared agent task", coreNext("tabs"))
	}
	operation := "eval"
	if protocol {
		operation = "cdp"
		if coreMethodPattern.MatchString(args[0]) {
			operation += ":" + args[0]
		}
	}
	if err = store.Journal(selected.Identity(), target, operation, "requested"); err != nil {
		return err
	}
	outcome := "failed"
	defer func() { _ = store.Journal(selected.Identity(), target, operation, outcome) }()
	method := "Runtime.evaluate"
	params := map[string]any{}
	if protocol {
		method = args[0]
		if !cdpAllowed(method) {
			outcome = "refused"
			return agentstate.Failure("cdp_forbidden", "this protocol method is outside the task-owned page capability", coreNext("tabs"))
		}
		if len(args) > 1 {
			source, e := coreSource(command, args[1])
			if e != nil {
				return e
			}
			if e = json.Unmarshal([]byte(source), &params); e != nil || params == nil {
				return coreArgument("CDP parameters must be a JSON object")
			}
		}
	} else {
		if len(args) != 1 {
			return coreArgument("eval requires one source and an optional tab")
		}
		source, e := coreSource(command, args[0])
		if e != nil {
			return e
		}
		params = map[string]any{"expression": source, "awaitPromise": true, "returnByValue": true, "userGesture": true, "replMode": true, "serializationOptions": map[string]any{"serialization": "idOnly"}, "timeout": timeout.Milliseconds()}
	}
	var addressed *agentstate.Tab
	if target != "" {
		addressed, err = store.Tab(target)
		if err != nil {
			return err
		}
	}
	ctx, cancel := coreTimeout(command, timeout+time.Second)
	defer cancel()
	client, browser, err := resolveCoreBrowser(ctx, store, addressed)
	if err != nil {
		return err
	}
	if protocol && target == "" {
		return coreArgument("cdp requires an explicit owned tab")
	}
	tab, lease, err := store.Reserve(browser, selected.Identity(), selected.SessionKey, target, selected.TaskSource == "flag", false, timeout+2*time.Second)
	if err != nil {
		return err
	}
	defer store.Release(tab.Handle, lease)
	target = tab.Handle
	if tab.ChromeID < 0 {
		return agentstate.Failure("tab_required", "open a page before evaluation", coreNext("tabs"))
	}
	session := &browserfast.Session{Browser: client, Tab: tab.ChromeID, Owner: tab.Owner}
	defer session.Close()
	if err = session.Acquire(ctx); err != nil {
		return err
	}
	if err = applyCoreViewport(ctx, store, tab, lease, session, ""); err != nil {
		return err
	}
	if capture {
		recording, e := beginCoreCapture(ctx, command, client, session, tab, tab.URL)
		if e != nil {
			return e
		}
		defer recording.finish()
	}
	var wire json.RawMessage
	result := coreTabResult(tab)
	result.Meta = map[string]any{"operation": operation}
	result.Untrusted = map[string]any{"url": tab.URL}
	if err = session.Inspect(ctx, "", method, params, &wire); err != nil {
		outcome = "outcome_unknown"
		if bridge.PossibleDispatch(err) || bridge.BeforeDispatch(err) {
			if bridge.BeforeDispatch(err) {
				outcome = "not_dispatched"
			}
			return err
		}
		result.Status = "performed"
		result.Next = coreNext("page", tab.Handle)
		result.Meta = map[string]any{"operation": operation, "observation_error": "outcome_unknown; verify before continuing"}
		return emitCore(command, result)
	}

	if !protocol {
		var initial struct {
			Result struct {
				ID      string `json:"objectId"`
				Subtype string `json:"subtype"`
			} `json:"result"`
		}
		_ = json.Unmarshal(wire, &initial)
		if initial.Result.ID != "" {
			objectID := initial.Result.ID
			defer func() {
				cleanup, done := context.WithTimeout(context.Background(), time.Second)
				defer done()
				var ignored any
				_ = session.Inspect(cleanup, "", "Runtime.releaseObject", map[string]any{"objectId": objectID}, &ignored)
			}()
			method := "Runtime.callFunctionOn"
			params := map[string]any{"objectId": objectID, "functionDeclaration": "function(){return this}", "returnByValue": true, "awaitPromise": true, "userGesture": true}
			if initial.Result.Subtype == "promise" {
				method = "Runtime.awaitPromise"
				params = map[string]any{"promiseObjectId": objectID, "returnByValue": true}
			}
			if err = session.Inspect(ctx, "", method, params, &wire); err != nil {
				outcome = "outcome_unknown"
				result.Status = "performed"
				result.Next = coreNext("page", tab.Handle)
				result.Meta = map[string]any{"operation": operation, "observation_error": "evaluation completion unavailable; verify before continuing"}
				return emitCore(command, result)
			}
		}
	}
	var value any
	if protocol {
		value = decodeObject(wire)
	} else {
		var response struct {
			Result struct {
				Value          json.RawMessage `json:"value"`
				Unserializable string          `json:"unserializableValue"`
				Type           string          `json:"type"`
			} `json:"result"`
			Exception json.RawMessage `json:"exceptionDetails"`
		}
		if err = json.Unmarshal(wire, &response); err != nil {
			return err
		}
		if len(response.Exception) > 0 {
			outcome = "exception"
			result.Error = &coreError{Code: "evaluation_exception", Message: "page evaluation threw an exception", Next: coreNext("page", tab.Handle), Retry: "do-not-retry"}
			result.Untrusted = map[string]any{"url": tab.URL, "exception": decodeObject(response.Exception)}
			if err = emitCore(command, result); err != nil {
				return err
			}
			return output.MarkReported(output.NewAgentError("evaluation_exception", command.CommandPath(), "page evaluation threw an exception"))
		}
		value = decodeObject(response.Result.Value)
		if response.Result.Unserializable != "" {
			value = response.Result.Unserializable
		}
	}
	if command.Flags().Changed("out") {
		path, _ := command.Flags().GetString("out")
		encoded, e := output.Marshal(value)
		if e != nil {
			return e
		}
		artifact, e := coreArtifact(path, ".json", encoded)
		if e != nil {
			// The evaluation already completed. A filesystem failure must not
			// invite repeating a script that may have submitted a form or written data.
			outcome = "performed_export_failed"
			result.Status, result.Complete = "performed", boolPointer(false)
			result.Next = coreNext("page", tab.Handle)
			result.Meta = map[string]any{"operation": operation, "observation_error": "evaluation completed but result export failed; do not repeat the script"}
			result.Error = &coreError{Code: "result_export_failed", Message: "evaluation completed; export destination is unavailable", Next: result.Next, Retry: "do-not-retry"}
			if err := emitCore(command, result); err != nil {
				return err
			}
			return output.MarkReported(output.NewAgentError("result_export_failed", command.CommandPath(), result.Error.Message))
		}
		result.Meta = map[string]any{"operation": operation, "artifact": artifact}
		result.Status, result.Complete = "complete", boolPointer(true)
		outcome = "complete"
		return emitCore(command, result)
	}
	value = output.RedactValue(value)
	encoded, err := output.Marshal(value)
	if err != nil {
		return err
	}
	if len(encoded) > 8000 {
		artifact, e := coreArtifact("", ".json", encoded)
		if e != nil {
			return e
		}
		result.Meta = map[string]any{"operation": operation, "artifact": artifact}
		result.Untrusted = map[string]any{"url": tab.URL}
	} else {
		result.Untrusted = map[string]any{"url": tab.URL, "value": value}
	}
	result.Status = "complete"
	result.Complete = boolPointer(true)
	outcome = "complete"
	return emitCore(command, result)
}
func init() { rootCmd.AddCommand(newCoreEvalCommand(), newCoreCDPCommand()) }
