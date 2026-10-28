package browserfast

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type callerFunc func(context.Context, string, any, any) error

func (f callerFunc) Call(ctx context.Context, method string, in, out any) error {
	return f(ctx, method, in, out)
}

func TestAcquireWaitsOutATransientDebuggerAttachment(t *testing.T) {
	attempts := 0
	caller := callerFunc(func(_ context.Context, method string, _ any, out any) error {
		if method != "browser.lease.acquire" {
			return errors.New("unexpected " + method)
		}
		attempts++
		if attempts < 3 {
			return errors.New("chrome_api_error: Another debugger is already attached to the tab with id: 7")
		}
		return json.Unmarshal([]byte(`{"lease_id":"l","generation":"g"}`), out)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session := &Session{Browser: caller, Tab: 7, Owner: "o"}
	if err := session.Acquire(ctx); err != nil || attempts != 3 || session.lease.LeaseID != "l" {
		t.Fatalf("expected the third attempt to succeed: err=%v attempts=%d", err, attempts)
	}
}

func TestAcquireGivesUpNearTheDeadlineAndOnOtherErrors(t *testing.T) {
	attempts := 0
	busy := callerFunc(func(context.Context, string, any, any) error {
		attempts++
		return errors.New("Another debugger is already attached to the tab")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 450*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := (&Session{Browser: busy, Tab: 7, Owner: "o"}).Acquire(ctx); err == nil {
		t.Fatal("a tab that never frees up must still fail")
	}
	if elapsed := time.Since(started); elapsed > 700*time.Millisecond || attempts < 2 {
		t.Fatalf("retries must stay inside the deadline: %v after %d attempts", elapsed, attempts)
	}
	other := callerFunc(func(context.Context, string, any, any) error {
		return errors.New("tab_busy: tab has an active capture")
	})
	count := 0
	counting := callerFunc(func(ctx context.Context, m string, in, out any) error { count++; return other(ctx, m, in, out) })
	if err := (&Session{Browser: counting, Tab: 7, Owner: "o"}).Acquire(context.Background()); err == nil || count != 1 {
		t.Fatalf("other errors are not retried: err=%v count=%d", err, count)
	}
}
