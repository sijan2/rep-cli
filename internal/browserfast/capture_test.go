package browserfast

import (
	"context"
	"testing"

	"github.com/repplus/rep-cli/internal/browserrpc"
)

func TestCapturePlanBorrowsOnlyItsExistingExecutionLease(t *testing.T) {
	expected := browserrpc.Lease{Owner: "default/fixture", LeaseID: "l_fixture", Generation: "generation"}
	session := &Session{Tab: 42, Owner: expected.Owner, lease: expected}
	caller := session.CaptureCaller()
	lease, err := browserrpc.Acquire(context.Background(), caller, 42, expected.Owner, "execute")
	if err != nil || lease != expected {
		t.Fatalf("borrow: %+v %v", lease, err)
	}
	if err = browserrpc.Release(context.Background(), caller, 42, lease); err != nil {
		t.Fatal(err)
	}
	if session.lease != expected {
		t.Fatal("nested plan released the capture lease")
	}
	for _, test := range []struct {
		tab            int
		owner, purpose string
	}{{43, expected.Owner, "execute"}, {42, "other", "execute"}, {42, expected.Owner, "observe"}} {
		if _, err = browserrpc.Acquire(context.Background(), caller, test.tab, test.owner, test.purpose); err == nil {
			t.Fatal("foreign or read-only lease accepted")
		}
	}
	if err = browserrpc.Release(context.Background(), caller, 42, browserrpc.Lease{Owner: expected.Owner, LeaseID: "other"}); err == nil {
		t.Fatal("foreign lease release accepted")
	}
}
