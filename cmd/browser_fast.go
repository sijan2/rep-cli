package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/browserflow"
	"github.com/repplus/rep-cli/internal/browserrpc"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

type fastFlags struct {
	tab     int
	timeout time.Duration
	maxText int
	limit   int
}

type fastOutput struct {
	browserfast.Result
	DispatchState     string                `json:"dispatch_state,omitempty"`
	Version           int                   `json:"version"`
	Workspace         string                `json:"workspace"`
	Task              string                `json:"task"`
	OperationEvidence *operationEvidenceRef `json:"operation_evidence,omitempty"`
	Tab               int                   `json:"tab_id"`
	Created           bool                  `json:"created,omitempty"`
	Rebound           bool                  `json:"rebound_after_url_change,omitempty"`
	TabState          string                `json:"tab_state,omitempty"`
	CleanupNext       string                `json:"cleanup_next,omitempty"`
	CleanupError      string                `json:"cleanup_error,omitempty"`
	DurationMS        int64                 `json:"duration_ms"`
	Phases            map[string]int64      `json:"phases_ms,omitempty"`
	BridgeCalls       int                   `json:"bridge_calls"`
	ModelRequests     int                   `json:"model_requests"`
	Next              string                `json:"next,omitempty"`
	Receipt           string                `json:"receipt,omitempty"`
	Error             *output.AgentError    `json:"error,omitempty"`
}

func fastTarget(args []string, tab int) (string, error) {
	if len(args) > 1 || (len(args) == 0 && tab < 0) {
		return "", errors.New("provide an HTTP(S) URL or --tab ID")
	}
	if len(args) == 0 {
		return "", nil
	}
	u, err := url.Parse(args[0])
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || len(args[0]) > 8192 {
		return "", errors.New("provide an absolute HTTP(S) URL without user credentials")
	}
	return u.String(), nil
}

func bindFastFlags(command *cobra.Command, flags *fastFlags) {
	command.Flags().IntVar(&flags.tab, "tab", -1, "Read or act in this explicit tab; a URL creates an owned tab")
	command.Flags().DurationVar(&flags.timeout, "timeout", 2*time.Second, "Navigation, action and observation budget; pending replies remain pending")
	command.Flags().IntVar(&flags.maxText, "max-text", 4096, "Maximum returned text bytes (256 to 16384)")
	command.Flags().IntVar(&flags.limit, "limit", 40, "Maximum returned controls (1 to 80)")
	advancedFlags(command, "limit", "max-text")
}

func validateFastFlags(flags fastFlags) error {
	if flags.timeout < 100*time.Millisecond || flags.timeout > 60*time.Second || flags.maxText < 256 || flags.maxText > 16384 || flags.limit < 1 || flags.limit > 80 || flags.tab < -1 {
		return errors.New("timeout must be 100ms to 60s, max-text 256 to 16384, limit 1 to 80, and tab nonnegative")
	}
	return nil
}

func emitFast(command *cobra.Command, result fastOutput, err error) error {
	selected, _ := scope.Current()
	result.Version, result.Workspace, result.Task = 1, selected.Workspace, selected.Task
	if err != nil {
		if result.Status == "" {
			result.Status = "failed"
		}
		ae := output.NewAgentError("browser_fast_failed", command.CommandPath(), err.Error(), result.Next)
		var rpcError *bridge.RPCError
		if errors.As(err, &rpcError) && rpcError.Code != "" {
			ae.Code = rpcError.Code
		}
		if result.Code != "" {
			ae.Code = result.Code
		}
		result.Error = &ae
	}
	var data any = result
	if len(result.Reference) > 0 {
		// Agents often bound tool output independently. Put reference facts and
		// provenance before lengthy page text and detailed coverage diagnostics.
		data = struct {
			Status    string          `json:"status"`
			URL       string          `json:"url"`
			Title     string          `json:"title,omitempty"`
			Tab       int             `json:"tab_id"`
			TabState  string          `json:"tab_state,omitempty"`
			Content   json.RawMessage `json:"content,omitempty"`
			Next      string          `json:"next,omitempty"`
			Reference json.RawMessage `json:"reference,omitempty"`
			fastOutput
		}{result.Status, result.URL, result.Title, result.Tab, result.TabState, result.Content, result.Next, result.Reference, result}
	}
	// Fast browser results are already structured agent output. Only an explicit
	// envelope request changes their shape; ordinary pipes need no extra flag.
	if forceEnvelope && !rawJSON {
		data = output.WrapData(command.CommandPath(), "browser", data)
	}
	encoder := json.NewEncoder(command.OutOrStdout())
	// Fast results are CLI JSON, never HTML. Keeping literal <, > and & avoids
	// multiplying long link data into \u00xx escapes and wasting agent context.
	encoder.SetEscapeHTML(false)
	if encodeErr := encoder.Encode(data); encodeErr != nil {
		return encodeErr
	}
	if err != nil {
		return output.MarkReported(*result.Error)
	}
	return nil
}

func finishFast(command *cobra.Command, result fastOutput, record *browserOperationEvidence, started time.Time, err error) error {
	selected, _ := scope.Current()
	result.Version, result.Workspace, result.Task = 1, selected.Workspace, selected.Task
	result.OperationEvidence = record.ref()
	result.DurationMS = time.Since(started).Milliseconds()
	status, verification := "completed", "unverified"
	if err != nil || result.Status == "failed" {
		status, verification = "failed", "unknown"
	}
	if result.Status == "pending" || result.Status == "unconfirmed" || result.Status == "loading" || result.Status == "partial" {
		status, verification = "unknown", "unknown"
	}
	if result.Status == "verified" || result.Status == "completed" {
		verification = "satisfied"
	}
	if evidenceErr := record.finish(map[string]any{"result": result, "verification_scope": "ui_operation"}, status, verification, "", err); evidenceErr != nil {
		err = errors.Join(err, evidenceErr)
	}
	result.DurationMS = time.Since(started).Milliseconds()
	return emitFast(command, result, err)
}

// fastSession owns one connection and attachment lease for the whole command.
// There is no traffic capture, help discovery, AX scan, or model round trip.
func fastSession(ctx context.Context, browser browserrpc.Caller, owner, rawURL string, tab int) (*browserfast.Session, bool, error) {
	return fastSessionWithWait(ctx, browser, owner, rawURL, tab, "domcontentloaded")
}

func fastSessionWithWait(ctx context.Context, browser browserrpc.Caller, owner, rawURL string, tab int, navigationWait string) (*browserfast.Session, bool, error) {
	created := false
	session := &browserfast.Session{Browser: browser, Tab: tab, Owner: owner}
	if rawURL != "" {
		if tab < 0 {
			// Obtain ownership before waiting for navigation. A slow page or a
			// lost navigation acknowledgment must still return a resumable tab.
			var allocated struct {
				Tab *int `json:"tab_id"`
			}
			if err := session.Call(ctx, "browser.create", map[string]any{"url": "about:blank", "active": false}, &allocated); err != nil {
				return nil, false, err
			}
			if allocated.Tab == nil || *allocated.Tab < 0 {
				return nil, false, errors.New("browser creation returned no tab")
			}
			tab, created, session.Tab = *allocated.Tab, true, *allocated.Tab
		}
		var result struct {
			Tab           *int `json:"tab_id"`
			Created       bool `json:"created_tab"`
			LegacyCreated bool `json:"created"`
		}
		params := map[string]any{"url": rawURL, "wait": navigationWait, "timeout_ms": max(100, time.Until(deadlineOrNow(ctx)).Milliseconds()-250), "active": false}
		if tab >= 0 {
			params["tab_id"] = tab
		}
		if err := session.Call(ctx, "browser.navigate", params, &result); err != nil {
			return session, created, err
		}
		if result.Tab == nil || *result.Tab != tab {
			return session, created, errors.New("browser navigation returned an unexpected tab")
		}
	}
	session.Tab = tab
	if err := session.Acquire(ctx); err != nil {
		return session, created, err
	}
	return session, created, nil
}

func deadlineOrNow(ctx context.Context) time.Time {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline
	}
	return time.Now().Add(2 * time.Second)
}

// rebindAfterURLChange re-leases the current document once when a read-only
// evaluation reports page_changed right after a navigation: applications such
// as Google Docs rewrite their URL after commit. Reads have no side effects, so
// one retry on the same tab is safe; input and chat dispatch never use this.
func rebindAfterURLChange(ctx context.Context, session *browserfast.Session, result *fastOutput, err error, read func() (browserfast.Result, error)) error {
	if err != nil || session == nil || result.Status != "failed" || result.Code != "page_changed" {
		return err
	}
	if releaseErr := session.Close(); releaseErr != nil {
		return releaseErr
	}
	if acquireErr := session.Acquire(ctx); acquireErr != nil {
		return acquireErr
	}
	result.Result, err = read()
	result.Rebound = true
	return err
}

// hydrationPending marks a cold read that returned nothing as still loading, so
// an agent re-reads with a longer budget instead of concluding the page is empty.
// Only short budgets qualify; a long budget that still finds nothing is final.
func hydrationPending(result *browserfast.Result, navigated bool, budget time.Duration) bool {
	// An application shell before hydration has no text at all and at most a
	// few scaffold controls (a skip link, a theme toggle). Real pages have text.
	return navigated && budget < 5*time.Second && result.Status == "ready" && len(result.Controls) <= 3 && strings.TrimSpace(result.Text) == ""
}

// A read can resume in the known tab after navigation/observation runs out of
// time. This is deliberately never used for input or chat dispatch errors.
func fastReadPending(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	var transport net.Error
	var rpc *bridge.RPCError
	return errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, &transport) && transport.Timeout() || errors.As(err, &rpc) && (rpc.Code == "navigation_timeout" || rpc.Code == "lease_acquisition_expired")
}

func fastNext(command *cobra.Command, verb string, tab int, extra string) string {
	selected, _ := scope.Current()
	prefix := fmt.Sprintf("rep --workspace %s --task %s", selected.Workspace, selected.Task)
	if browser := decisionBrowserName(command); browser != defaultBrowser {
		extra = " --browser " + shellQuote(browser) + extra
	}
	return fmt.Sprintf("%s browser %s --tab %d%s", prefix, verb, tab, extra)
}

func newBrowserPageCommand(deps jevDOMDependencies) *cobra.Command {
	flags := fastFlags{}
	var cursor string
	command := &cobra.Command{Use: "page [url]", Short: "Open and read a page with exact control references in one call", Args: cobra.MaximumNArgs(1)}
	command.SilenceUsage, command.SilenceErrors = true, true
	command.RunE = func(command *cobra.Command, args []string) (returnErr error) {
		started := time.Now()
		rawURL, err := fastTarget(args, flags.tab)
		if err == nil {
			err = validateFastFlags(flags)
		}
		if err == nil && cursor != "" && (rawURL != "" || flags.tab < 0 || !validFastToken(cursor, "p_")) {
			err = errors.New("a page cursor requires --tab and no navigation URL")
		}
		if err != nil {
			return reportJevError(command, err)
		}
		record, err := beginBrowserEvidence("browser.page", map[string]any{"intent": "read a page with exact references", "navigation": rawURL != ""}, decisionBrowserName(command), flags.tab)
		if err != nil {
			return reportJevError(command, err)
		}
		defer record.finishOnReturn(&returnErr)
		ctx, cancel := context.WithTimeout(command.Context(), flags.timeout)
		defer cancel()
		browser, err := deps.browser(ctx, decisionBrowserName(command))
		if err != nil {
			return reportJevError(command, err)
		}
		owner, err := decisionOwner()
		if err != nil {
			return reportJevError(command, err)
		}
		record.dispatch()
		session, created, err := fastSession(ctx, browser, owner, rawURL, flags.tab)
		result := fastOutput{Tab: flags.tab, Created: created}
		if session != nil {
			result.Tab = session.Tab
			result.Next = fastNext(command, "page", session.Tab, "")
		}
		if err == nil {
			// A quick read spends at most 400 ms on readiness; an explicit longer
			// budget (the continuation after awaiting_hydration) waits for content.
			readinessMS := max(int64(0), time.Until(deadlineOrNow(ctx)).Milliseconds()-750)
			if flags.timeout < 5*time.Second && readinessMS > 400 {
				readinessMS = 400
			}
			read := func() (browserfast.Result, error) {
				return session.Eval(ctx, browserfast.Spec{Operation: "page", MaxText: flags.maxText, Limit: flags.limit, Cursor: cursor, WaitMS: readinessMS, ReadMS: max(0, time.Until(deadlineOrNow(ctx)).Milliseconds()-250)})
			}
			result.Result, err = read()
			if rawURL != "" {
				err = rebindAfterURLChange(ctx, session, &result, err, read)
			}
			if err == nil && hydrationPending(&result.Result, rawURL != "", flags.timeout) {
				result.Status, result.Code = "loading", "awaiting_hydration"
			}
			if result.NextCursor != "" {
				result.Next = fastNext(command, "page", session.Tab, fmt.Sprintf(" --cursor %s --limit %d --max-text %d", result.NextCursor, flags.limit, flags.maxText))
			} else if err == nil && (result.Status == "ready" || result.Status == "partial") {
				result.Next = ""
			} else if result.Status == "loading" {
				result.Next = fastNext(command, "page", session.Tab, fmt.Sprintf(" --timeout 10s --max-text %d --limit %d", flags.maxText, flags.limit))
			}
			if err == nil && (result.Status == "failed" || result.Status == "unconfirmed") {
				err = errors.New(result.Code)
			}
		}
		if session != nil {
			if cleanupErr := session.Close(); err == nil {
				err = cleanupErr
			}
			result.BridgeCalls = session.Calls
		}
		if result.Tab >= 0 && fastReadPending(ctx, err) {
			result.Status, result.Code, err = "loading", "observation_pending", nil
			result.Next = fastNext(command, "page", result.Tab, fmt.Sprintf(" --timeout 10s --max-text %d --limit %d", flags.maxText, flags.limit))
		}
		return finishFast(command, result, record, started, err)
	}
	bindFastFlags(command, &flags)
	command.Flags().StringVar(&cursor, "cursor", "", "Read the next bounded slice of the returned page snapshot")
	return command
}

func newBrowserDoCommand(deps jevDOMDependencies) *cobra.Command {
	flags := fastFlags{}
	var text, key string
	var click, replace bool
	command := &cobra.Command{Use: "do <ref> --tab ID", Short: "Fill, click, or press an exact reference returned by browser page", Args: cobra.ExactArgs(1)}
	command.SilenceUsage, command.SilenceErrors = true, true
	command.RunE = func(command *cobra.Command, args []string) (returnErr error) {
		started := time.Now()
		if err := validateFastFlags(flags); err != nil {
			return reportJevError(command, err)
		}
		choices := 0
		action := ""
		if command.Flags().Changed("text") {
			choices++
			action = "fill"
		}
		if click {
			choices++
			action = "click"
		}
		if key != "" {
			choices++
			action = "press"
		}
		if choices != 1 || flags.tab < 0 || len(text) > 32768 || (replace && action != "fill") {
			return reportJevError(command, errors.New("provide --tab and exactly one of --text, --click, or --press; --replace requires --text"))
		}
		if key != "" {
			if _, ok := browserflow.KeyEvent(key); !ok {
				return reportJevError(command, errors.New("unsupported key"))
			}
		}
		record, err := beginBrowserEvidence("browser.do", map[string]any{"action": action, "reference": args[0]}, decisionBrowserName(command), flags.tab)
		if err != nil {
			return reportJevError(command, err)
		}
		defer record.finishOnReturn(&returnErr)
		ctx, cancel := context.WithTimeout(command.Context(), flags.timeout)
		defer cancel()
		browser, err := deps.browser(ctx, decisionBrowserName(command))
		if err != nil {
			return reportJevError(command, err)
		}
		owner, err := decisionOwner()
		if err != nil {
			return reportJevError(command, err)
		}
		session, _, err := fastSession(ctx, browser, owner, "", flags.tab)
		result := fastOutput{Tab: flags.tab, Next: fastNext(command, "page", flags.tab, "")}
		if err == nil {
			record.dispatch()
			result.Result, err = session.Eval(ctx, browserfast.Spec{Operation: "do", Ref: args[0], Action: action, Text: text, Replace: replace, MaxText: flags.maxText})
			if err == nil && (result.Status == "failed" || result.Status == "unconfirmed") {
				err = errors.New(result.Code)
			}
			if err == nil && result.Status == "prepared" {
				err = session.Press(ctx, key, args[0], result.URL)
				result.Status, result.Evidence = "performed", "dispatch"
				if err != nil {
					result.Status, result.Code = "unconfirmed", "input_outcome_unknown"
				}
			}
			if err == nil && result.Status == "prepared_pointer" {
				if result.Point == nil {
					err = errors.New("browser returned no guarded click point")
				} else {
					err = session.Click(ctx, *result.Point, result.URL)
				}
				result.Status, result.Evidence = "performed", "native_pointer_dispatch"
				if err != nil {
					result.Status, result.Code = "unconfirmed", "input_outcome_unknown"
				}
			}
		}
		if session != nil {
			if cleanupErr := session.Close(); err == nil {
				err = cleanupErr
			}
			result.BridgeCalls = session.Calls
		}
		return finishFast(command, result, record, started, err)
	}
	bindFastFlags(command, &flags)
	command.Flags().StringVar(&text, "text", "", "Exact text to enter (existing drafts are preserved)")
	command.Flags().BoolVar(&click, "click", false, "Click this observed control once")
	command.Flags().StringVar(&key, "press", "", "Press one real browser key, e.g. Enter")
	command.Flags().BoolVar(&replace, "replace", false, "Explicitly replace an existing field value")
	_ = command.MarkFlagRequired("tab")
	return command
}

type fastChatReceipt struct {
	Version       int    `json:"version"`
	Tab           int    `json:"tab_id"`
	Browser       string `json:"browser"`
	Turn          string `json:"turn"`
	Status        string `json:"status"`
	URL           string `json:"url"`
	DispatchState string `json:"dispatch_state"`
}

func validFastToken(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) == 34 && strings.IndexFunc(value[2:], func(r rune) bool { return !strings.ContainsRune("0123456789abcdef", r) }) < 0
}

func chatReceiptPath(turn string) (string, error) {
	if !validFastToken(turn, "t_") {
		return "", errors.New("invalid chat turn")
	}
	selected, err := scope.Current()
	if err != nil {
		return "", err
	}
	if !selected.Scoped {
		return "", errors.New("chat requires a workspace and task")
	}
	return filepath.Join(selected.DataDir, "fast-chat", turn+".json"), nil
}

func newBrowserChatCommand(deps jevDOMDependencies) *cobra.Command {
	flags := fastFlags{}
	var message, turn string
	command := &cobra.Command{Use: "chat [url] --message TEXT", Short: "Send one chat message and observe its reply without command discovery", Args: cobra.MaximumNArgs(1)}
	command.SilenceUsage, command.SilenceErrors = true, true
	command.RunE = func(command *cobra.Command, args []string) (returnErr error) {
		started := time.Now()
		rawURL, err := fastTarget(args, flags.tab)
		if err == nil {
			err = validateFastFlags(flags)
		}
		if err != nil {
			return reportJevError(command, err)
		}
		if (turn == "") == !command.Flags().Changed("message") || (turn != "" && rawURL != "") || strings.TrimSpace(message) == "" && turn == "" || len(message) > 32768 {
			return reportJevError(command, errors.New("provide --message TEXT to send, or --turn ID with --tab to observe without sending"))
		}
		browserName := decisionBrowserName(command)
		var receipt fastChatReceipt
		if turn == "" {
			receipt.DispatchState = "not_sent"
		}
		if turn != "" {
			path, pathErr := chatReceiptPath(turn)
			if pathErr != nil {
				return reportJevError(command, pathErr)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil || json.Unmarshal(data, &receipt) != nil || receipt.Version != 1 || receipt.Tab != flags.tab || receipt.Browser != browserName || receipt.Turn != turn {
				return reportJevError(command, errors.New("chat receipt does not match this task, tab, browser and turn"))
			}
		}
		record, err := beginBrowserEvidence("browser.chat", map[string]any{"send": turn == "", "observe_turn": turn}, browserName, flags.tab)
		if err != nil {
			return reportJevError(command, err)
		}
		defer record.finishOnReturn(&returnErr)
		ctx, cancel := context.WithTimeout(command.Context(), flags.timeout)
		defer cancel()
		browser, err := deps.browser(ctx, browserName)
		if err != nil {
			return reportJevError(command, err)
		}
		owner, err := decisionOwner()
		if err != nil {
			return reportJevError(command, err)
		}
		session, created, err := fastSession(ctx, browser, owner, rawURL, flags.tab)
		result := fastOutput{Tab: flags.tab, Created: created}
		if session == nil {
			result.DispatchState = receipt.DispatchState
			return finishFast(command, result, record, started, err)
		}
		result.Tab = session.Tab
		if err == nil && turn == "" {
			readinessMS := max(int64(0), time.Until(deadlineOrNow(ctx)).Milliseconds()-1000)
			result.Result, err = session.Eval(ctx, browserfast.Spec{Operation: "chat_prepare", Text: message, MaxText: flags.maxText, WaitMS: readinessMS})
			if err == nil && (result.Status == "failed" || result.Status == "unconfirmed") {
				err = errors.New(result.Code)
			}
			if err == nil {
				turn = result.Turn
				path, pathErr := chatReceiptPath(turn)
				if pathErr != nil {
					err = pathErr
				} else if dirErr := privateDirectory(filepath.Dir(path)); dirErr != nil {
					err = dirErr
				} else {
					receipt = fastChatReceipt{Version: 1, Tab: session.Tab, Browser: browserName, Turn: turn, Status: "prepared", URL: result.URL, DispatchState: "not_sent"}
					data, _ := json.Marshal(receipt)
					err = writePrivateFile(path, data)
					result.Receipt = path
				}
			}
			if err == nil {
				composerRef := result.ComposerRef
				record.dispatch()
				result.Result, err = session.Eval(ctx, browserfast.Spec{Operation: "chat_fill", URL: result.URL, Ref: composerRef, Turn: turn, Text: message, MaxText: flags.maxText})
				if err == nil && (result.Status == "failed" || result.Status == "unconfirmed") {
					err = errors.New(result.Code)
				}
				if err == nil {
					receipt.DispatchState = "dispatch_pending"
					data, _ := json.Marshal(receipt)
					err = writePrivateFile(result.Receipt, data)
					if err == nil {
						// Hydration can expose the composer before its Send control.
						// Spend the available readiness budget instead of failing after
						// a fixed 250ms while usable time remains. Input is still once.
						sendReadinessMS := max(int64(0), time.Until(deadlineOrNow(ctx)).Milliseconds()-400)
						result.Result, err = session.Eval(ctx, browserfast.Spec{Operation: "chat_send", URL: result.URL, Turn: turn, WaitMS: sendReadinessMS, MaxText: flags.maxText})
						if err == nil && (result.Status == "failed" || result.Status == "unconfirmed") {
							err = errors.New(result.Code)
						}
						if err == nil && result.Status == "prepared_pointer" {
							if result.Point == nil {
								err = errors.New("browser returned no guarded send point")
							} else {
								err = session.Click(ctx, *result.Point, result.URL)
							}
							result.Status, result.Evidence = "performed", "native_send_control_dispatch"
						}
					}
					if err != nil {
						if result.Status == "failed" {
							receipt.DispatchState = "not_sent"
						} else {
							receipt.DispatchState = "unknown"
							result.Status, result.Code = "unconfirmed", "input_outcome_unknown"
						}
					} else {
						receipt.DispatchState = "send_control_dispatched"
					}
				}
			}
		}
		if err == nil {
			remaining := max(int64(0), time.Until(deadlineOrNow(ctx)).Milliseconds()-750)
			result.Result, err = session.Eval(ctx, browserfast.Spec{Operation: "chat_wait", Turn: turn, WaitMS: remaining, MaxText: flags.maxText})
			if err == nil && result.Status == "failed" {
				err = errors.New(result.Code)
			}
		}
		if err != nil && result.Status == "" {
			result.Status = "failed"
		}
		result.Turn = turn
		if turn != "" {
			if result.Status != "completed" {
				result.Next = fastNext(command, "chat", session.Tab, " --turn "+turn+" --timeout 30s")
			}
			if receipt.DispatchState == "not_sent" || result.Code == "turn_missing" || result.Code == "page_changed" {
				result.Next = fastNext(command, "page", session.Tab, "")
			}
			path, _ := chatReceiptPath(turn)
			result.Receipt = path
			receipt.Status, receipt.URL = result.Status, result.URL
			data, _ := json.Marshal(receipt)
			if writeErr := writePrivateFile(path, data); err == nil {
				err = writeErr
			}
		} else {
			result.Next = fastNext(command, "page", session.Tab, "")
		}
		result.DispatchState = receipt.DispatchState
		if cleanupErr := session.Close(); err == nil {
			err = cleanupErr
		}
		result.BridgeCalls = session.Calls
		return finishFast(command, result, record, started, err)
	}
	bindFastFlags(command, &flags)
	command.Flags().StringVar(&message, "message", "", "Message to send exactly once; existing drafts are preserved")
	command.Flags().StringVar(&turn, "turn", "", "Observe this returned turn without sending again")
	return command
}

func init() {
	deps := defaultJevDOMDependencies()
	browserCmd.AddCommand(newBrowserPageCommand(deps), newBrowserDoCommand(deps), newBrowserChatCommand(deps))
}
