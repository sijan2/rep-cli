package commands

import (
	"context"
	"encoding/json"
	"errors"

	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/browserfast"

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
	if client, ok := browser.(*bridge.Client); ok && client.Registry.ProfileID != "" {
		if _, err := registerLegacyTab(client, tab, created); err != nil {
			return session, created, err
		}
	}
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

func fastPageStateKind(result *browserfast.Result) string {
	var signals struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(result.PageState, &signals)
	return signals.Kind
}

func legacyHandle(command *cobra.Command, chromeID int) *agentstate.Tab {
	registry, err := coreStore()
	if err != nil {
		return nil
	}
	state, err := registry.Read()
	if err != nil {
		return nil
	}
	selected, _ := scope.Current()
	name := decisionBrowserName(command)
	var match *agentstate.Tab
	for _, tab := range state.Tabs {
		if tab.Owner == selected.Identity() && !tab.Closed && tab.ChromeID == chromeID && (name == "any" || tab.Browser.Name == name) {
			if match != nil {
				return nil
			}
			match = tab
		}
	}
	return match
}
func fastNext(command *cobra.Command, verb string, tab int, extra string) string {
	owned := legacyHandle(command, tab)
	if owned == nil {
		return coreNext("tabs")
	}
	switch verb {
	case "state", "screenshot":
		verb = "shot"
	case "page", "do":
	default:
		verb = "page"
	}
	return coreNext(verb, owned.Handle)
}

func validFastToken(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) == 34 && strings.IndexFunc(value[2:], func(r rune) bool { return !strings.ContainsRune("0123456789abcdef", r) }) < 0
}
