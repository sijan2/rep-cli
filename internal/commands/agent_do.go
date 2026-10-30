package commands

import (
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

func newCoreDoCommand() *cobra.Command {
	var key string
	command := &cobra.Command{Use: "do REF [VALUE]", Short: "Act on an observed control reference", Args: cobra.RangeArgs(1, 2)}
	command.Flags().StringVar(&key, "press", "", "Key or chord to press after filling")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := coreDo(cmd, args, key); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}
func coreDo(command *cobra.Command, args []string, key string) error {
	if !agentstate.RefPattern.MatchString(args[0]) {
		return coreArgument("provide an observed control reference")
	}
	store, err := coreStore()
	if err != nil {
		return err
	}
	reference, err := store.ResolveRef(args[0])
	if err != nil {
		return err
	}
	tab, err := store.Tab(args[0])
	if err != nil {
		return err
	}
	ctx, cancel := coreTimeout(command, 10*time.Second)
	defer cancel()
	client, browser, err := resolveCoreBrowser(ctx, store, tab)
	if err != nil {
		return err
	}
	selected, _ := scope.Current()
	tab, lease, err := store.Reserve(browser, selected.Identity(), selected.SessionKey, tab.Handle, true, true, 11*time.Second)
	if err != nil {
		return err
	}
	defer store.Release(tab.Handle, lease)
	session := &browserfast.Session{Browser: client, Tab: tab.ChromeID, Owner: tab.Owner}
	defer session.Close()
	if err := session.Acquire(ctx); err != nil {
		return err
	}
	var document struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := session.Inspect(ctx, "", "Runtime.evaluate", map[string]any{"expression": "String(performance.timeOrigin)", "returnByValue": true}, &document); err != nil {
		return err
	}
	if document.Result.Value != reference.Generation {
		return agentstate.Failure("reference_stale", "the reference belongs to an earlier document", coreNext("page", tab.Handle))
	}
	spec := browserfast.Spec{Operation: "do", Ref: reference.Raw, URL: tab.URL, Replace: true, Action: "click"}
	if len(args) > 1 {
		spec.Action, spec.Text = "fill", args[1]
	} else if key != "" {
		spec.Action = "press"
	}
	prepared, err := session.Eval(ctx, spec)
	if err != nil {
		return err
	}
	if prepared.Status == "failed" {
		code := prepared.Code
		if code == "reference_expired" || code == "reference_stale" {
			code = "reference_stale"
		}
		return agentstate.Failure(code, "control changed before input", coreNext("page", tab.Handle))
	}
	result := coreTabResult(tab)
	result.Status = "performed"
	result.Next = coreNext("page", tab.Handle)
	if prepared.Status == "unconfirmed" {
		result.Meta = map[string]any{"observation_error": "input effect was not confirmed"}
		return emitCore(command, result)
	}
	if prepared.Point != nil {
		err = session.Click(ctx, *prepared.Point, prepared.URL)
	} else if key != "" {
		err = session.Press(ctx, key, reference.Raw, prepared.URL)
	}
	if err != nil {
		result.Meta = map[string]any{"observation_error": "input may have been dispatched; verify before continuing"}
	} else {
		result.Meta = map[string]any{"observed": prepared.Evidence}
	}
	return emitCore(command, result)
}
