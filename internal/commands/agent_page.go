package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

type corePageOptions struct {
	Section, Find, Viewport, Selector string
	Reload, Capture, Reference        bool
}

func newCorePageCommand() *cobra.Command {
	options := corePageOptions{}
	command := &cobra.Command{Use: "page [TAB] [URL|/path|p_CURSOR]", Short: "Read or navigate a page; continue with its returned cursor", Args: cobra.MaximumNArgs(2)}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := corePage(cmd, args, options); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}
func corePage(command *cobra.Command, args []string, options corePageOptions) error {
	store, err := coreStore()
	if err != nil {
		return err
	}
	selected, _ := scope.Current()
	target, rawURL := "", ""
	var cursor *agentstate.Cursor
	for _, arg := range args {
		switch {
		case agentstate.TabPattern.MatchString(arg):
			if target != "" {
				return coreArgument("provide only one tab")
			}
			target = arg
		case agentstate.CursorPattern.MatchString(arg):
			if cursor != nil || rawURL != "" {
				return coreArgument("a cursor cannot navigate")
			}
			value, lookupErr := store.Cursor(arg)
			if lookupErr != nil {
				return lookupErr
			}
			cursor = &value
			if target != "" && target != value.Tab {
				return coreArgument("cursor belongs to another tab")
			}
			target = value.Tab
		case coreHTTPURL(arg) || strings.HasPrefix(arg, "/"):
			if rawURL != "" || cursor != nil {
				return coreArgument("provide one URL or cursor")
			}
			rawURL = arg
		default:
			return coreArgument("expected a tab handle, HTTP(S) URL, relative path, or page cursor")
		}
	}
	if rawURL != "" && !strings.HasPrefix(rawURL, "/") {
		parsed, e := url.Parse(rawURL)
		if e != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return coreArgument("use an HTTP(S) URL without credentials")
		}
	}
	if cursor != nil && cursor.Kind == "reference-legacy" {
		return coreLegacyReferenceContinuation(command, store, *cursor)
	}
	if cursor != nil && cursor.Kind == "reference-sections" {
		return coreReferenceContinuation(command, store, *cursor)
	}
	var addressed *agentstate.Tab
	if target != "" {
		addressed, err = store.Tab(target)
		if err != nil {
			return err
		}
	}
	budget := 10 * time.Second
	if options.Reference {
		budget = 20 * time.Second
	}
	ctx, cancel := coreTimeout(command, budget)
	defer cancel()
	client, browser, err := resolveCoreBrowser(ctx, store, addressed)
	if err != nil {
		return err
	}
	tab, lease, err := store.Reserve(browser, selected.Identity(), selected.SessionKey, target, selected.TaskSource == "flag", rawURL != "" || options.Reload, budget+time.Second)
	if err != nil {
		return err
	}
	defer store.Release(tab.Handle, lease)
	if rawURL != "" {
		parsed, parseErr := url.Parse(rawURL)
		if parseErr != nil {
			return coreArgument("invalid navigation URL")
		}
		if strings.HasPrefix(rawURL, "/") {
			base, baseErr := url.Parse(tab.URL)
			if baseErr != nil || base.Host == "" {
				return coreArgument("relative navigation requires an observed page")
			}
			parsed = base.ResolveReference(parsed)
		}
		if parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return coreArgument("use an HTTP(S) URL without credentials")
		}
		rawURL = parsed.String()
	}
	if tab.ChromeID < 0 {
		var created struct {
			Tab struct {
				ID int `json:"id"`
			} `json:"tab"`
			TabID *int `json:"tab_id"`
		}
		if err := client.Call(ctx, "browser.create", map[string]any{"url": "about:blank", "active": false}, &created); err != nil {
			return err
		}
		tab.ChromeID = created.Tab.ID
		if created.TabID != nil {
			tab.ChromeID = *created.TabID
		}
		if tab.ChromeID <= 0 {
			return errors.New("browser did not return a tab")
		}
		if err := store.Change(tab.Handle, lease, func(stored *agentstate.Tab) error { stored.ChromeID = tab.ChromeID; return nil }); err != nil {
			return err
		}
	}
	session := &browserfast.Session{Browser: client, Tab: tab.ChromeID, Owner: selected.Identity()}
	defer session.Close()
	if err := session.Acquire(ctx); err != nil {
		return err
	}
	if rawURL != "" {
		var ignored any
		if err := session.Inspect(ctx, "", "Page.navigate", map[string]any{"url": rawURL}, &ignored); err != nil {
			return err
		}
	}
	if options.Reference || cursor != nil && strings.HasPrefix(cursor.Kind, "reference-") {
		return coreReadReference(ctx, command, store, tab, lease, session, cursor, options)
	}
	return coreReadPage(ctx, command, store, tab, lease, session, cursor, options)
}
func coreReadPage(ctx context.Context, command *cobra.Command, store *agentstate.Store, tab *agentstate.Tab, lease string, session *browserfast.Session, cursor *agentstate.Cursor, options corePageOptions) error {
	spec := browserfast.Spec{Operation: "page", MaxText: 10000, Limit: 40, WaitMS: 4000, ReadMS: 4000}
	if cursor != nil {
		spec.TextOffset = cursor.Offset
		spec.ControlOffset = cursor.Controls
		if cursor.Budget > 0 {
			spec.MaxText = cursor.Budget
		}
		if cursor.Kind == "loading" {
			spec.WaitMS = 7000
			spec.ReadMS = 7000
		}
	}
	observed, err := session.Eval(ctx, spec)
	if err != nil {
		return err
	}
	if observed.Status == "failed" {
		return agentstate.Failure(observed.Code, "page observation failed", coreNext("page", tab.Handle))
	}
	return projectCorePage(command, store, tab, lease, observed, cursor, options)
}
func projectCorePage(command *cobra.Command, store *agentstate.Store, tab *agentstate.Tab, lease string, observed browserfast.Result, cursor *agentstate.Cursor, options corePageOptions) error {
	raw := make([]string, len(observed.Controls))
	for i, control := range observed.Controls {
		raw[i] = control.Ref
	}
	refs, err := store.MintRefs(tab.Handle, lease, observed.Generation, raw)
	if err != nil {
		return err
	}
	controls := make([]map[string]any, len(raw))
	hasInput := false
	for i, control := range observed.Controls {
		item := map[string]any{"ref": refs[i], "role": control.Role, "name": control.Name}
		if !control.Enabled {
			item["enabled"] = false
		}
		if control.HasValue != nil && *control.HasValue {
			item["has_value"] = true
			hasInput = true
		}
		if control.URL != "" {
			item["url"] = control.URL
		}
		controls[i] = item
	}
	var coverage struct {
		Offset            int `json:"text_offset"`
		Controls          int `json:"control_offset"`
		Remaining         int `json:"remaining_text_bytes"`
		RemainingControls int `json:"remaining_controls"`
	}
	_ = jsonDecode(observed.Coverage, &coverage)
	result := coreTabResult(tab)
	result.Status = observed.Status
	result.PageState = decodeObject(observed.PageState)
	kind := fastPageStateKind(&observed)
	complete := observed.Status == "ready" && kind == "content" && coverage.Remaining == 0 && coverage.RemainingControls == 0
	result.Complete = boolPointer(complete)
	if cursor != nil {
		result.ContentChanged = cursor.Digest != observed.ContentHash || cursor.URL != observed.URL
	}
	if observed.Status == "loading" {
		if cursor != nil && cursor.Kind == "loading" {
			result.Next = coreNext("shot", tab.Handle)
		} else {
			id, e := store.SaveCursor(agentstate.Cursor{Tab: tab.Handle, Owner: tab.Owner, Kind: "loading", URL: observed.URL, Digest: observed.ContentHash, Budget: 10000})
			if e != nil {
				return e
			}
			result.Next = coreNext("page", id)
		}
	} else if coverage.Remaining > 0 || coverage.RemainingControls > 0 {
		id, e := store.SaveCursor(agentstate.Cursor{Tab: tab.Handle, Owner: tab.Owner, Kind: "page", URL: observed.URL, Digest: observed.ContentHash, Offset: coverage.Offset + len(observed.Text), Controls: coverage.Controls + len(controls), Budget: 10000, Section: options.Section, Find: options.Find})
		if e != nil {
			return e
		}
		result.Next = coreNext("page", id)
	} else if kind != "content" {
		result.Next = coreNext("shot", tab.Handle)
	}
	if err := store.Change(tab.Handle, lease, func(stored *agentstate.Tab) error {
		stored.URL = observed.URL
		stored.HasInput = hasInput
		stored.Dialog = strings.Contains(string(observed.PageState), `"dialogs":1`)
		return nil
	}); err != nil {
		return err
	}
	result.Untrusted = map[string]any{"url": observed.URL, "title": observed.Title, "controls": controls, "text": observed.Text}
	return emitCore(command, result)
}

func jsonDecode(raw []byte, out any) error { return json.Unmarshal(raw, out) }

// Used by the migration path to register a legacy numeric tab once its bridge
// has the new profile handshake. No invented profile identity is accepted.
func registerLegacyTab(client *bridge.Client, chromeID int, created bool) (*agentstate.Tab, error) {
	store, err := coreStore()
	if err != nil {
		return nil, err
	}
	selected, _ := scope.Current()
	name := client.Registry.Browser
	privateName := ""
	if browserSelector == "headless" {
		name = "headless"
		privateName = strings.TrimSpace(os.Getenv("REP_PROFILE"))
		if privateName == "" {
			privateName = "default"
		}
	}
	return store.Register(browserIdentity(client, name, privateName), chromeID, selected.Identity(), selected.SessionKey, created)
}
