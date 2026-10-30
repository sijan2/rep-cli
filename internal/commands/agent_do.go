package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/browserflow"
	"github.com/repplus/rep-cli/internal/browserrpc"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

func newCoreDoCommand() *cobra.Command {
	var capture bool
	var key string
	var files []string
	command := &cobra.Command{Use: "do REF [VALUE] | @PLAN [TAB]", Short: "Act and return verified changes and fresh controls", Args: cobra.RangeArgs(1, 33)}
	command.Flags().StringVar(&key, "press", "", "Key, chord, or space-separated sequence after filling")
	command.Flags().StringArrayVar(&files, "file", nil, "Upload a local file to the observed file input")
	command.Flags().BoolVar(&capture, "capture", false, "Record traffic triggered by this action")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := coreDo(cmd, args, key, files, capture); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}
func coreDo(command *cobra.Command, args []string, key string, files []string, capture bool) error {
	keys := []string{}
	if key != "" {
		keys = strings.FieldsFunc(key, func(r rune) bool { return r == ',' || r == ' ' })
		if len(keys) == 0 {
			return coreArgument("provide a key name; use Space for a space")
		}
		if len(keys) > 16 {
			return coreArgument("at most 16 key chords per action")
		}
		for _, k := range keys {
			if _, ok := browserflow.KeyEvent(k); !ok {
				return coreArgument("unsupported key or modifier chord")
			}
		}
	}
	if len(files) > 0 && len(args) > 1 {
		files = append(files, args[1:]...)
		args = args[:1]
	}
	if len(args) > 2 {
		return coreArgument("provide one value or tab")
	}
	for i, path := range files {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		info, err := os.Stat(absolute)
		if err != nil || !info.Mode().IsRegular() {
			return coreArgument("upload paths must be readable regular files")
		}
		files[i] = absolute
	}
	if len(files) > 32 || len(files) > 0 && (len(args) > 1 || key != "") {
		return coreArgument("file uploads cannot combine a value or key chord")
	}
	var plan *browserflow.Plan
	target := args[0]
	if strings.HasPrefix(args[0], "@") {
		if key != "" || len(files) > 0 {
			return coreArgument("plans contain their own actions")
		}
		source, err := coreSource(command, args[0])
		if err != nil {
			return err
		}
		decoded, err := browserflow.Decode(strings.NewReader(source))
		if err != nil {
			return agentstate.Failure("invalid_plan", "plan must be {version:1,url:HTTP_URL,steps:[{id,action,target:{name|selector},value?,keys?,after?}]}; actions: fill, choose, check, click, press, wait", coreNext("do", "--help"))
		}
		plan = &decoded
		target = ""
		if len(args) > 1 {
			target = args[1]
		}
	} else if !agentstate.RefPattern.MatchString(target) {
		return coreArgument("provide an observed control reference or @plan")
	}
	store, err := coreStore()
	if err != nil {
		return err
	}
	selected, _ := scope.Current()
	var reference agentstate.Ref
	var addressed *agentstate.Tab
	if target != "" {
		addressed, err = store.Tab(target)
		if err != nil {
			return err
		}
		if plan == nil {
			reference, err = store.ResolveRef(target)
			if err != nil {
				return err
			}
		}
	}
	duration := 15 * time.Second
	if plan != nil {
		duration = 5 * time.Minute
	}
	ctx, cancel := coreTimeout(command, duration)
	defer cancel()
	client, browser, err := resolveCoreBrowser(ctx, store, addressed)
	if err != nil {
		return err
	}
	if addressed != nil {
		target = addressed.Handle
	}
	tab, lease, err := store.Reserve(browser, selected.Identity(), selected.SessionKey, target, selected.TaskSource == "flag", true, duration+time.Second)
	if err != nil {
		return err
	}
	defer store.Release(tab.Handle, lease)
	if tab.ChromeID < 0 {
		return agentstate.Failure("tab_required", "open a page before acting", coreNext("tabs"))
	}
	if err = store.Journal(tab.Owner, tab.Handle, "do", "requested"); err != nil {
		return err
	}
	outcome := "failed"
	defer func() { _ = store.Journal(tab.Owner, tab.Handle, "do", outcome) }()
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
	before, err := session.Eval(ctx, browserfast.Spec{Operation: "page", Readable: true, MaxText: 8000, Limit: 40, WaitMS: 500, ReadMS: 1500})
	if err != nil {
		return err
	}
	operation := "click"
	verified := false
	observationError := ""
	if plan != nil {
		var engineBrowser browserrpc.Caller = client
		if capture {
			engineBrowser = session.CaptureCaller()
		} else {
			_ = session.Close()
			session = &browserfast.Session{Browser: client, Tab: tab.ChromeID, Owner: tab.Owner}
			defer session.Close()
		}
		engine := browserflow.Engine{Browser: engineBrowser, Owner: tab.Owner}
		report, runErr := engine.Run(ctx, tab.ChromeID, *plan, true)
		attempted := false
		for _, step := range report.Steps {
			attempted = attempted || step.Attempted
		}
		if runErr != nil && !attempted {
			return agentstate.Failure("plan_failed", "plan failed before input", coreNext("page", tab.Handle))
		}
		if runErr != nil {
			observationError = "plan stopped; verify the last attempted step before continuing"
		}
		verified = report.Status == "verified" || report.Status == "complete"
		operation = "plan"
		if !capture {
			if err = session.Acquire(ctx); err != nil {
				outcome = "performed"
				return coreDoUnobserved(command, tab, operation, "plan dispatched; observation unavailable")
			}
		}
	} else {
		if before.Generation != reference.Generation {
			return agentstate.Failure("reference_stale", "the reference belongs to an earlier document", coreNext("page", tab.Handle))
		}
		spec := browserfast.Spec{Operation: "do", Ref: reference.Raw, URL: tab.URL, Replace: true, Action: "click"}
		if len(args) > 1 {
			spec.Action, spec.Text = "fill", args[1]
		} else if len(files) > 0 {
			spec.Action = "file"
		} else if len(keys) > 0 {
			spec.Action = "press"
		}
		operation = spec.Action
		prepared, e := session.Eval(ctx, spec)
		if e != nil {
			outcome = "performed"
			return coreDoUnobserved(command, tab, operation, "outcome_unknown; verify before continuing")
		}
		if prepared.Status == "failed" {
			code := prepared.Code
			if code == "reference_expired" {
				code = "reference_stale"
			}
			return agentstate.Failure(code, "control changed before input", coreNext("page", tab.Handle))
		}
		if prepared.Status == "unconfirmed" {
			outcome = "performed"
			return coreDoUnobserved(command, tab, operation, "input effect was not confirmed")
		}
		verified = prepared.Status == "verified"
		if prepared.Point != nil && len(files) == 0 {
			err = session.Click(ctx, *prepared.Point, prepared.URL)
		}
		if len(files) > 0 {
			if prepared.Point != nil {
				err = session.UploadTrigger(ctx, *prepared.Point, prepared.URL, files)
			} else {
				err = coreUpload(ctx, session, reference.Raw, files)
			}
			verified = err == nil
		}
		for _, k := range keys {
			if err != nil {
				break
			}
			err = session.Press(ctx, k, reference.Raw, prepared.URL)
		}
		if err != nil {
			outcome = "performed"
			return coreDoUnobserved(command, tab, operation, "input may have been dispatched; verify before continuing")
		}
	}
	// Observe under the same registry/execute lease. Retry observation only,
	// never the input, while an immediate UI transition settles.
	after := browserfast.Result{}
	for i := 0; i < 8; i++ {
		after, err = session.Eval(ctx, browserfast.Spec{Operation: "page", Readable: true, MaxText: 8000, Limit: 40, WaitMS: 500, ReadMS: 1500})
		if err == nil && after.Status != "failed" && (after.ContentHash != before.ContentHash || after.URL != before.URL || verified) {
			break
		}
		select {
		case <-ctx.Done():
			i = 8
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err != nil || after.Status == "failed" {
		outcome = "performed"
		return coreDoUnobserved(command, tab, operation, "input dispatched; observation unavailable")
	}
	result, err := buildCorePage(store, tab, lease, after, nil, corePageOptions{})
	if err != nil {
		outcome = "performed"
		return coreDoUnobserved(command, tab, operation, "input dispatched; new references unavailable")
	}
	changed := after.ContentHash != before.ContentHash || after.URL != before.URL || after.Generation != before.Generation
	verified = verified || changed
	result.Status = "performed"
	if verified {
		result.Status = "performed_and_verified"
	}
	if result.Complete != nil && *result.Complete {
		result.Next = ""
	}
	if after.Generation == before.Generation && after.URL == before.URL {
		prior := map[string]bool{}
		for _, control := range before.Controls {
			key := control.Role + "\x00" + control.Name + "\x00" + control.URL
			encoded, _ := json.Marshal(control.HasValue)
			prior[key+string(encoded)] = true
		}
		controls := result.Untrusted.(map[string]any)["controls"].([]map[string]any)
		changedControls := []map[string]any{}
		for i, control := range after.Controls {
			key := control.Role + "\x00" + control.Name + "\x00" + control.URL
			encoded, _ := json.Marshal(control.HasValue)
			if !prior[key+string(encoded)] || !control.Enabled {
				changedControls = append(changedControls, controls[i])
			}
		}
		result.Untrusted.(map[string]any)["controls"] = changedControls
	}
	result.Meta = map[string]any{"operation": operation, "changed": changed, "verified": verified, "not_observed": []string{"native_dialog", "download_completion", "popup_contents"}}
	if observationError != "" {
		result.Meta.(map[string]any)["observation_error"] = observationError
		result.Next = coreNext("page", tab.Handle)
	}
	if after.Generation == before.Generation && after.URL == before.URL {
		delta, truncated := coreTextDeltaBounded(before.Text, after.Text)
		result.Untrusted.(map[string]any)["text"] = delta
		if truncated {
			result.Complete = boolPointer(false)
			result.Next = coreNext("page", tab.Handle)
			result.Meta.(map[string]any)["text_truncated"] = true
		}
		result.Untrusted.(map[string]any)["text_kind"] = "changed_lines"
	} else {
		result.Untrusted.(map[string]any)["text_kind"] = "navigated_page"
	}
	if after.Status == "loading" {
		result.Next = coreNext("page", tab.Handle)
	}
	if len(after.Focus) > 0 {
		result.Untrusted.(map[string]any)["focus"] = decodeObject(after.Focus)
	}
	var state struct {
		Dialogs int `json:"dialogs"`
	}
	_ = json.Unmarshal(after.PageState, &state)
	if state.Dialogs > 0 {
		result.Meta.(map[string]any)["dom_dialogs"] = state.Dialogs
	}
	outcome = result.Status
	return emitCore(command, result)
}
func coreTextDelta(before, after string) string {
	text, _ := coreTextDeltaBounded(before, after)
	return text
}
func coreTextDeltaBounded(before, after string) (string, bool) {
	existing := map[string]bool{}
	for _, line := range strings.Split(before, "\n") {
		existing[line] = true
	}
	changed := []string{}
	size := 0
	for _, line := range strings.Split(after, "\n") {
		if existing[line] || strings.TrimSpace(line) == "" {
			continue
		}
		if size+len(line) > 4000 {
			return strings.Join(changed, "\n"), true
		}
		changed = append(changed, line)
		size += len(line) + 1
	}
	return strings.Join(changed, "\n"), false
}
func coreDoUnobserved(command *cobra.Command, tab *agentstate.Tab, operation, reason string) error {
	result := coreTabResult(tab)
	result.Status = "performed"
	result.Next = coreNext("page", tab.Handle)
	result.Meta = map[string]any{"operation": operation, "observation_error": reason}
	return emitCore(command, result)
}
func coreUpload(ctx context.Context, session *browserfast.Session, ref string, files []string) error {
	// A guarded renderer marks the exact bound input; no selector supplied by the
	// page or caller can redirect DOM.setFileInputFiles to a different element.
	encoded, _ := json.Marshal(ref)
	owner, _ := json.Marshal(session.Owner)
	expression := "(()=>{const b=globalThis[Symbol.for('rep.fast.v1')]?.owners.get(" + string(owner) + ")?.refs.get(" + string(encoded) + ");if(!b||b.generation!==performance.timeOrigin||!b.el.isConnected||b.el.parentNode!==b.parent||b.el.type!=='file')throw Error('stale input');return b.el})()"
	var object struct {
		Result struct {
			ID string `json:"objectId"`
		} `json:"result"`
	}
	if err := session.Inspect(ctx, "", "Runtime.evaluate", map[string]any{"expression": expression}, &object); err != nil {
		return err
	}
	if object.Result.ID == "" {
		return coreArgument("file input disappeared")
	}
	var ignored any
	if err := session.Inspect(ctx, "", "DOM.setFileInputFiles", map[string]any{"objectId": object.Result.ID, "files": files}, &ignored); err != nil {
		return err
	}
	var readback struct {
		Result struct {
			Value int `json:"value"`
		} `json:"result"`
	}
	if err := session.Inspect(ctx, "", "Runtime.callFunctionOn", map[string]any{"objectId": object.Result.ID, "functionDeclaration": "function(){return this.files.length}", "returnByValue": true}, &readback); err != nil {
		return err
	}
	if readback.Result.Value != len(files) {
		return coreArgument("file input did not retain the requested files")
	}
	return nil
}
