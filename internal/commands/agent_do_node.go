package commands

import (
	"time"

	"github.com/repplus/rep-cli/internal/act"
	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/scene"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

// coreDoNode acts on a scene handle from `rep records controls`. The handle is
// validated against the node itself and its owning record at click time, never
// against a whole-page fingerprint, and input is native and never retried.
func coreDoNode(command *cobra.Command, args []string, keys, files []string) error {
	ref, err := scene.ParseHandle(args[0])
	if err != nil || ref.Kind != "n" {
		return coreArgument("node handles look like n_<frame>.<node>; records own r_ handles are not clickable")
	}
	if len(args) != 2 || !agentstate.TabPattern.MatchString(args[1]) {
		return coreArgument("a node handle needs its tab: rep do n_HANDLE tN")
	}
	if len(files) > 0 {
		return coreArgument("node handles support click and --press only")
	}
	target := args[1]
	store, err := coreStore()
	if err != nil {
		return err
	}
	selected, _ := scope.Current()
	owner := selected.Identity()
	addressed, err := store.Tab(target)
	if err != nil {
		return err
	}
	if addressed.Owner != owner || !addressed.CreatedByRep {
		return agentstate.Failure("owned_by_other_task", "node actions run only in this task's own tabs", coreNext("tabs"))
	}
	index, err := readSceneIndex(selected.DataDir, target)
	if err != nil {
		return agentstate.Failure("scene_missing", "observe the tab's controls before acting on a handle", coreNext("records", "controls", target))
	}
	expect, known := index.Controls[args[0]]
	if !known || index.Frame6 != ref.Frame6 {
		return agentstate.Failure("stale_document", "the handle is not from the tab's last controls view", coreNext("records", "controls", target))
	}
	ctx, cancel := coreTimeout(command, 20*time.Second)
	defer cancel()
	client, browser, err := resolveCoreBrowser(ctx, store, addressed)
	if err != nil {
		return err
	}
	tab, lease, err := store.Reserve(browser, owner, selected.SessionKey, target, selected.TaskSource == "flag", false, 25*time.Second)
	if err != nil {
		return err
	}
	defer store.Release(tab.Handle, lease)
	if err := store.Journal(tab.Owner, tab.Handle, "do", "requested"); err != nil {
		return err
	}
	outcome := "failed"
	defer func() { _ = store.Journal(tab.Owner, tab.Handle, "do", outcome) }()
	session := &browserfast.Session{Browser: client, Tab: tab.ChromeID, Owner: owner}
	defer session.Close()
	if err := session.Acquire(ctx); err != nil {
		return err
	}
	caller := leasedCDP{session}
	clicked, err := act.Click(ctx, caller, tab.ChromeID, owner, ref, expect)
	if err != nil {
		outcome = "outcome_unknown"
		return agentstate.Failure("act_failed", "the browser did not complete the action; verify the page before acting again", coreNext("records", "controls", target))
	}
	if clicked.Status != "ok" {
		outcome = "not_dispatched"
		return &agentstate.Error{Code: clicked.Code, Message: "the control no longer matches what was observed; nothing was clicked", Next: coreNext("records", "controls", target), Retry: "needs-agent"}
	}
	pressed := []string{}
	for _, key := range keys {
		result, err := act.Press(ctx, caller, tab.ChromeID, owner, key)
		if err != nil || result.Status != "ok" {
			outcome = "performed"
			return coreDoUnobserved(command, tab, "press", "click dispatched; a key press did not complete")
		}
		pressed = append(pressed, key)
	}
	outcome = "performed"
	result := coreTabResult(tab)
	result.Browser, result.Profile = browser.Name, browser.Profile
	result.Status = "performed"
	result.Complete = boolPointer(false)
	result.Next = coreNext("records", "controls", target)
	result.Meta = map[string]any{"operation": "click", "handle": args[0], "control": map[string]any{"role": expect.Role, "name": expect.Name, "owner": expect.Owner}, "pressed": pressed, "act": clicked.Delta, "model_requests": 0}
	return emitCore(command, result)
}
