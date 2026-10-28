// Package browserfast provides bounded page reads and exact DOM-reference actions.
// It never calls a model or retries an input operation.
package browserfast

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/browserflow"
	"github.com/repplus/rep-cli/internal/browserrpc"
)

//go:embed runtime.js
var renderer string

//go:embed content.js
var contentRenderer string

//go:embed reference.js
var referenceRenderer string

type Spec struct {
	Owner     string `json:"owner"`
	Operation string `json:"operation"`
	URL       string `json:"url,omitempty"`
	Ref       string `json:"ref,omitempty"`
	Action    string `json:"action,omitempty"`
	Text      string `json:"text,omitempty"`
	Replace   bool   `json:"replace,omitempty"`
	Turn      string `json:"turn,omitempty"`
	WaitMS    int64  `json:"wait_ms,omitempty"`
	ReadMS    int64  `json:"read_ms,omitempty"`
	MaxText   int    `json:"max_text"`
	Limit     int    `json:"limit"`
	Cursor    string `json:"cursor,omitempty"`
	Selector  string `json:"selector,omitempty"`
}

type Result struct {
	Status             string          `json:"status"`
	Code               string          `json:"code,omitempty"`
	URL                string          `json:"url"`
	Title              string          `json:"title,omitempty"`
	Text               string          `json:"text,omitempty"`
	Controls           []Control       `json:"controls,omitempty"`
	Coverage           json.RawMessage `json:"coverage,omitempty"`
	Chat               json.RawMessage `json:"chat,omitempty"`
	Turn               string          `json:"turn,omitempty"`
	ComposerRef        string          `json:"composer_ref,omitempty"`
	Submitted          bool            `json:"submitted"`
	SubmissionEvidence string          `json:"submission_evidence,omitempty"`
	MessageID          string          `json:"message_id,omitempty"`
	Response           string          `json:"response,omitempty"`
	ResponseTruncated  bool            `json:"response_truncated,omitempty"`
	ReplyComplete      *bool           `json:"reply_complete,omitempty"`
	Evidence           string          `json:"evidence,omitempty"`
	Changed            bool            `json:"changed,omitempty"`
	NextCursor         string          `json:"next_cursor,omitempty"`
	ObservedAt         string          `json:"observed_at,omitempty"`
	Point              *Point          `json:"point,omitempty"`
	Content            json.RawMessage `json:"content,omitempty"`
	Reference          json.RawMessage `json:"reference,omitempty"`
	Stack              json.RawMessage `json:"stack,omitempty"`
	DataRequests       json.RawMessage `json:"data_requests,omitempty"`
	PageState          json.RawMessage `json:"page_state,omitempty"`
}

// Control is a compact guarded page control. Decode strings before the CLI
// re-encodes them so an upstream JSON \u00xx representation cannot multiply
// terminal output size for long URLs.
type Control struct {
	Ref      string `json:"ref"`
	Role     string `json:"role"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	HasValue *bool  `json:"has_value,omitempty"`
	URL      string `json:"url,omitempty"`
}

type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Session struct {
	Browser browserrpc.Caller
	Tab     int
	Owner   string
	lease   browserrpc.Lease
	Calls   int
}

func (s *Session) Call(ctx context.Context, method string, params any, out any) error {
	s.Calls++
	return s.Browser.Call(ctx, method, params, out)
}

// Acquire takes the execution lease. A navigation that the CLI stopped waiting
// for can still hold the tab's debugger for a moment inside the extension, so
// "another debugger is already attached" is retried until the command's own
// deadline instead of failing a follow-up read a few hundred milliseconds early.
func (s *Session) Acquire(ctx context.Context) error {
	for {
		lease, err := browserrpc.Acquire(ctx, s, s.Tab, s.Owner, "execute")
		s.lease = lease
		if err == nil || !attachmentBusy(err) {
			return err
		}
		remaining := time.Until(deadlineOr(ctx, time.Now().Add(-time.Second)))
		if remaining < 200*time.Millisecond {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func attachmentBusy(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "another debugger is already attached")
}

func deadlineOr(ctx context.Context, fallback time.Time) time.Time {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline
	}
	return fallback
}

func (s *Session) Close() error {
	if s.lease.LeaseID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	return browserrpc.Release(ctx, s, s.Tab, s.lease)
}

func (s *Session) Eval(ctx context.Context, spec Spec) (Result, error) {
	spec.Owner = s.Owner
	if spec.MaxText == 0 {
		spec.MaxText = 4096
	}
	if spec.Limit == 0 {
		spec.Limit = 40
	}
	var result Result
	encoded, err := json.Marshal(spec)
	if err != nil {
		return result, err
	}
	route := browserrpc.Route{Owner: s.lease.Owner, LeaseID: s.lease.LeaseID, Generation: s.lease.Generation, ExpectedRootURL: spec.URL}
	var wire struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	script := renderer
	if spec.Operation == "reference" {
		script = referenceRenderer
	}
	expression := "(()=>{const repReadContent=(" + contentRenderer + ");return (" + script + ")(" + string(encoded) + ")})()"
	err = browserrpc.CDPRoute(ctx, s, s.Tab, route, "Runtime.evaluate", map[string]any{"expression": expression, "awaitPromise": true, "returnByValue": true, "userGesture": spec.Operation == "do" || spec.Operation == "chat_fill" || spec.Operation == "chat_send"}, &wire)
	if err != nil {
		return result, err
	}
	if len(wire.Exception) > 0 || len(wire.Result.Value) == 0 {
		return result, errors.New("page renderer unavailable")
	}
	if err = json.Unmarshal(wire.Result.Value, &result); err != nil {
		return result, err
	}
	return result, nil
}

// Inspect runs one read-only protocol method within this command's lease.
func (s *Session) Inspect(ctx context.Context, page, method string, params any, out any) error {
	route := browserrpc.Route{Owner: s.lease.Owner, LeaseID: s.lease.LeaseID, Generation: s.lease.Generation, ExpectedRootURL: page}
	return browserrpc.CDPRoute(ctx, s, s.Tab, route, method, params, out)
}

// CloseTab closes only the document bound to the current execution lease.
// Page.close preserves the browser's beforeunload handling.
func (s *Session) CloseTab(ctx context.Context, page string) error {
	route := browserrpc.Route{Owner: s.lease.Owner, LeaseID: s.lease.LeaseID, Generation: s.lease.Generation, ExpectedRootURL: page}
	var ignored any
	return browserrpc.CDPRoute(ctx, s, s.Tab, route, "Page.close", map[string]any{}, &ignored)
}

// Press uses real browser key events. Lost replies are never retried.
func (s *Session) Press(ctx context.Context, key, ref, page string) error {
	guard, err := s.Eval(ctx, Spec{Operation: "focus_guard", Ref: ref, URL: page})
	if err != nil {
		return err
	}
	if guard.Status != "verified" {
		return errors.New(guard.Code)
	}
	event, ok := browserflow.KeyEvent(key)
	if !ok {
		return errors.New("unsupported key")
	}
	route := browserrpc.Route{Owner: s.lease.Owner, LeaseID: s.lease.LeaseID, Generation: s.lease.Generation, ExpectedRootURL: page}
	var first error
	for _, kind := range []string{"keyDown", "keyUp"} {
		callCtx := ctx
		cleanupCancel := func() {}
		if kind == "keyUp" {
			route.ExpectedRootURL = ""
			if ctx.Err() != nil {
				callCtx, cleanupCancel = context.WithTimeout(context.Background(), 150*time.Millisecond)
			}
		}
		event["type"] = kind
		var ignored any
		if err := browserrpc.CDPRoute(callCtx, s, s.Tab, route, "Input.dispatchKeyEvent", event, &ignored); err != nil && first == nil {
			first = err
		}
		cleanupCancel()
	}
	return first
}

// Click emits one native press/release pair at the freshly guarded point.
// A lost acknowledgment is never retried; release is still attempted.
func (s *Session) Click(ctx context.Context, point Point, page string) error {
	route := browserrpc.Route{Owner: s.lease.Owner, LeaseID: s.lease.LeaseID, Generation: s.lease.Generation, ExpectedRootURL: page}
	var first error
	for _, kind := range []string{"mousePressed", "mouseReleased"} {
		callCtx := ctx
		cleanupCancel := func() {}
		if kind == "mouseReleased" {
			route.ExpectedRootURL = ""
			if ctx.Err() != nil {
				callCtx, cleanupCancel = context.WithTimeout(context.Background(), 150*time.Millisecond)
			}
		}
		var ignored any
		err := browserrpc.CDPRoute(callCtx, s, s.Tab, route, "Input.dispatchMouseEvent", map[string]any{"type": kind, "x": point.X, "y": point.Y, "button": "left", "clickCount": 1}, &ignored)
		cleanupCancel()
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}
