package browserrpc

import (
	"context"
	"errors"
)

// Lease is owned by one execution or observation scope, rather than by an
// individual RPC. Releasing it must not detach another observer's attachment.
type Lease struct {
	Owner      string `json:"owner"`
	LeaseID    string `json:"lease_id"`
	Generation string `json:"generation"`
}

func Acquire(ctx context.Context, caller Caller, tab int, owner, purpose string) (Lease, error) {
	lease := Lease{Owner: owner}
	if owner == "" || (purpose != "observe" && purpose != "execute") {
		return lease, errors.New("valid lease owner and purpose required")
	}
	if err := caller.Call(ctx, "browser.lease.acquire", map[string]any{"tab_id": tab, "owner": owner, "purpose": purpose}, &lease); err != nil {
		return lease, err
	}
	lease.Owner = owner
	if lease.LeaseID == "" || lease.Generation == "" {
		return lease, errors.New("browser returned invalid attachment lease")
	}
	return lease, nil
}

func Release(ctx context.Context, caller Caller, tab int, lease Lease) error {
	var ignored any
	return caller.Call(ctx, "browser.lease.release", map[string]any{"tab_id": tab, "owner": lease.Owner, "lease_id": lease.LeaseID}, &ignored)
}
