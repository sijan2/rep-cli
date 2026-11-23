package commands

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/governor"
	"github.com/repplus/rep-cli/internal/ledger"
	"github.com/repplus/rep-cli/internal/records"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/sitepolicy"
	"github.com/spf13/cobra"
)

const (
	recordsLedgerBodyBytes = 8 << 20
	recordsLedgerRingBytes = 64 << 20
	recordsLedgerIdleMS    = 120000
	recordsLedgerWait      = 8 * time.Second
)

func recordsGovernor() (*governor.Governor, error) {
	dir, err := governor.DefaultDir()
	if err != nil {
		return nil, err
	}
	return governor.New(dir), nil
}

// recordsDecoderFor names the decoder the records package has for a policy.
// A policy can describe a site before its decoder exists; such sites are
// refused instead of being read as text.
func recordsDecoderFor(policy *sitepolicy.Policy) string {
	for _, op := range policy.BodyOps {
		if op.Op == policy.Primary.Op {
			switch op.Decoder {
			case "reddit_html":
				return "reddit"
			case "x_tweetdetail":
				return "x"
			}
		}
	}
	return ""
}

// coreRecordsLive reads a thread from a tab rep owns. The ledger observes what
// the page's own client loads; rep navigates once and sends no other input.
func coreRecordsLive(command *cobra.Command, target, kind string, options recordsOptions, started time.Time) error {
	store, err := coreStore()
	if err != nil {
		return err
	}
	selected, _ := scope.Current()
	owner := selected.Identity()
	var addressed *agentstate.Tab
	rawURL := target
	if kind == "tab" {
		addressed, err = store.Tab(target)
		if err != nil {
			return err
		}
		if addressed.Owner != owner {
			return agentstate.Failure("owned_by_other_task", "records reads only this task's tabs", coreNext("tabs"))
		}
		if !addressed.CreatedByRep {
			return agentstate.Failure("user_tab", "records refuses a tab that rep did not create", coreNext("tabs"))
		}
		rawURL = addressed.URL
	}
	policy, inputs, ok := sitepolicy.Match(rawURL)
	if !ok || !policy.Complete(inputs) {
		return agentstate.Failure("unsupported_site", "no thread policy matches this page; read it as a page instead", coreNext("page", target))
	}
	decoder := recordsDecoderFor(policy)
	if decoder == "" || (options.Site != "auto" && options.Site != decoder) {
		return agentstate.Failure("unsupported_site", "this build has no records decoder for the site", coreNext("page", target))
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return coreArgument("use an HTTP(S) thread URL")
	}
	site := sitepolicy.RegistrableDomain(parsed.Hostname())
	gov, err := recordsGovernor()
	if err != nil {
		return err
	}
	if err := governorCooldownError(gov, site); err != nil {
		return err
	}
	timing := map[string]int64{}
	phase := func(name string, from time.Time) { timing[name] = time.Since(from).Milliseconds() }
	base, cancel := coreTimeout(command, options.Timeout+15*time.Second)
	defer cancel()
	// Ctrl-C ends the read; cleanup below runs on its own fresh context.
	ctx, stop := signal.NotifyContext(base, os.Interrupt, syscall.SIGTERM)
	defer stop()
	connect := time.Now()
	client, browser, err := resolveCoreBrowser(ctx, store, addressed)
	if err != nil {
		return err
	}
	phase("connect", connect)
	var tab *agentstate.Tab
	lease := ""
	if kind == "url" {
		var created struct {
			TabID *int `json:"tab_id"`
		}
		if err := client.Call(ctx, "browser.create", map[string]any{"url": "about:blank", "active": false}, &created); err != nil {
			return err
		}
		if created.TabID == nil || *created.TabID < 0 {
			return errors.New("browser did not return a tab")
		}
		tab, err = store.Register(browser, *created.TabID, owner, selected.SessionKey, true)
		if err != nil {
			return err
		}
		tab, lease, err = store.Reserve(browser, owner, selected.SessionKey, tab.Handle, selected.TaskSource == "flag", true, options.Timeout+20*time.Second)
	} else {
		tab, lease, err = store.Reserve(browser, owner, selected.SessionKey, target, selected.TaskSource == "flag", false, options.Timeout+20*time.Second)
	}
	if err != nil {
		return err
	}
	defer store.Release(tab.Handle, lease)
	session := &browserfast.Session{Browser: client, Tab: tab.ChromeID, Owner: owner}
	var book *ledger.Client
	closeTab := kind == "url" && !options.KeepTab
	leased := false
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if closeTab {
			if book != nil {
				_ = book.Close(cleanup)
			}
			if leased {
				_ = session.CloseTab(cleanup, "")
			}
			_ = store.Change(tab.Handle, lease, func(t *agentstate.Tab) error { t.Closed, t.Ledger = true, ""; return nil })
		}
		_ = session.Close()
	}()
	attach := time.Now()
	if kind == "url" {
		// The ledger must exist before navigation or the document is never seen.
		book, err = ledger.Open(ctx, client, tab.ChromeID, owner, policy.LedgerParams(owner, recordsLedgerBodyBytes, recordsLedgerRingBytes, recordsLedgerIdleMS))
		if err != nil {
			return err
		}
		if err := store.Change(tab.Handle, lease, func(t *agentstate.Tab) error { t.Ledger = book.ID; return nil }); err != nil {
			return err
		}
	} else {
		if addressed.Ledger == "" {
			return agentstate.Failure("ledger_missing", "this tab has no network ledger; read the thread URL with rep records --keep-tab first", coreNext("records", rawURL, "--keep-tab"))
		}
		book = &ledger.Client{Caller: client, Tab: tab.ChromeID, Owner: owner, ID: addressed.Ledger}
	}
	if err := session.Acquire(ctx); err != nil {
		return err
	}
	leased = true
	phase("attach", attach)
	if kind == "url" {
		navigate := time.Now()
		var navigated struct {
			ErrorText string `json:"errorText"`
		}
		if err := session.Inspect(ctx, "", "Page.navigate", map[string]any{"url": rawURL}, &navigated); err != nil {
			return err
		}
		if navigated.ErrorText != "" {
			return agentstate.Failure("navigation_failed", "the browser refused the navigation", coreNext("tabs"))
		}
		tab.URL = rawURL
		_ = store.Change(tab.Handle, lease, func(t *agentstate.Tab) error { t.URL = rawURL; return nil })
		phase("commit", navigate)
	}
	primary := time.Now()
	deadline := time.Now().Add(options.Timeout)
	// The read cursor is a change cursor: an entry reappears with a higher
	// revision when its body completes, so the latest revision wins.
	latest := map[uint64]ledger.Entry{}
	var order []uint64
	var batch ledger.Batch
	var since uint64
	var primaryEntry *ledger.Entry
	stopCode := ""
	for primaryEntry == nil && stopCode == "" {
		wait := recordsLedgerWait
		if remaining := time.Until(deadline); remaining < wait {
			if remaining <= 0 {
				break
			}
			wait = remaining
		}
		batch, err = book.Read(ctx, since, ledger.Match{AdmittedOnly: true, Phase: "end"}, wait)
		if err != nil {
			if ctx.Err() != nil {
				stopCode = "interrupted"
				break
			}
			return err
		}
		since = batch.NextSeq
		if batch.State != "open" {
			stopCode = "ledger_lost"
			break
		}
		for _, nav := range batch.Navs {
			if code := recordsNavigationStop(policy, nav.URL); code != "" {
				stopCode = code
			}
		}
		for _, entry := range batch.Entries {
			if _, seen := latest[entry.Seq]; !seen {
				order = append(order, entry.Seq)
			}
			latest[entry.Seq] = entry
			if entry.Response == nil {
				continue
			}
			if _, admitted := policy.Admit(entry.Method, entry.URL, entry.Type); !admitted {
				continue
			}
			if stop := gov.Observe(site, entry.Response.Status, entry.HTTPHeader(), ""); stop != nil {
				stopCode = stop.Code
			}
			if policy.IsPrimary(entry.Method, entry.URL, entry.Type) && primaryEntry == nil && entry.Body.State == "complete" {
				found := entry
				primaryEntry = &found
			}
		}
	}
	phase("primary", primary)
	if primaryEntry == nil {
		code := stopCode
		if code == "" {
			code = "primary_timeout"
		}
		return &agentstate.Error{Code: code, Message: "the page did not deliver its thread response", Next: coreNext("page", tab.Handle), Retry: "needs-agent"}
	}
	// Fragments the page's client already loaded resolve the continuations
	// their own requests named; nothing is requested on their behalf. The
	// primary leads the merge whatever the arrival order.
	decode := time.Now()
	body, sha, err := book.Body(ctx, primaryEntry.Seq)
	if err != nil {
		return err
	}
	in := []recordsInput{recordsLedgerInput(*primaryEntry, body, sha)}
	for _, seq := range order {
		entry := latest[seq]
		if seq == primaryEntry.Seq || entry.Response == nil || entry.Body.State != "complete" {
			continue
		}
		if _, admitted := policy.Admit(entry.Method, entry.URL, entry.Type); !admitted {
			continue
		}
		body, sha, err := book.Body(ctx, seq)
		if err != nil {
			continue
		}
		// An admitted operation without a decoder (X's TweetResultByRestId) adds nothing.
		if detected, _ := records.Detect(entry.URL, entry.Response.Mime, body); detected == "" {
			continue
		}
		in = append(in, recordsLedgerInput(entry, body, sha))
	}
	thread, err := recordsDecodeAll(in, options.Site, recordsFocal(rawURL))
	if err != nil {
		return err
	}
	phase("decode", decode)
	rate := governor.ParseRate(primaryEntry.HTTPHeader(), time.Now())
	meta := map[string]any{"source": kind, "tab": tab.Handle, "policy": policy.ID + "@" + policy.Version, "inputs": inputs, "timing_ms": timing,
		"ledger": map[string]any{"id": book.ID, "entries": batch.Stats.Entries, "bodies": batch.Stats.Bodies, "body_bytes": batch.Stats.BodyBytes, "skipped": batch.Stats.SkippedByReason},
		"rate":   map[string]any{"site": site, "headers_present": rate.HeadersPresent, "family": rate.Family, "remaining": rate.Remaining, "reset_at": rate.ResetAt}}
	if stopCode != "" {
		meta["stop"] = stopCode
	}
	result := coreTabResult(tab)
	result.Browser, result.Profile = browser.Name, browser.Profile
	if !closeTab {
		result.Next = coreNext("page", tab.Handle)
	}
	return emitRecords(command, result, thread, meta, options.Max, started)
}

func recordsLedgerInput(entry ledger.Entry, body []byte, sha string) recordsInput {
	mime := ""
	if entry.Response != nil {
		mime = entry.Response.Mime
	}
	post := ""
	if !entry.Request.PostData.Redacted {
		post = entry.Request.PostData.Value
	}
	return recordsInput{body: body, url: entry.URL, mime: mime, source: records.Source{RequestID: entry.RequestID, SHA256: sha}, requestBody: post, requestURL: entry.URL}
}

// recordsNavigationStop classifies main-frame navigations the site policy names
// as login walls or challenges. Anything else is the page's own routing.
func recordsNavigationStop(policy *sitepolicy.Policy, rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	for _, prefix := range policy.Stop.RedirectPathPrefixes {
		if strings.HasPrefix(parsed.Path, prefix) {
			return "auth"
		}
	}
	for _, key := range policy.Stop.RedirectQueryKeys {
		if parsed.Query().Has(key) {
			return "challenge"
		}
	}
	return ""
}

// governorCooldownError turns an active cooldown into the refusal every rep
// transport returns before sending anything to that site.
func governorCooldownError(gov *governor.Governor, site string) error {
	until, reason, active := gov.Cooldown(site)
	if !active {
		return nil
	}
	return &agentstate.Error{Code: "cooldown", Message: fmt.Sprintf("%s is cooling down until %s (%s); nothing was sent", site, until.UTC().Format(time.RFC3339), reason), Next: coreNext("scope"), Retry: "retryable"}
}
