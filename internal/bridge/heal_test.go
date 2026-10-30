package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func healSocket(t *testing.T, reply func(RPCRequest) *RPCResponse) (Registry, *atomic.Int32) {
	t.Helper()
	dir, err := os.MkdirTemp("", "bh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	count := new(atomic.Int32)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var request RPCRequest
				if json.NewDecoder(conn).Decode(&request) == nil {
					count.Add(1)
					if response := reply(request); response != nil {
						_ = json.NewEncoder(conn).Encode(response)
					}
				}
			}()
		}
	}()
	return Registry{ProfileID: "synthetic-profile", Epoch: "epoch", Browser: "dia", Socket: socket}, count
}
func TestHealRetriesOnlyBeforeDispatch(t *testing.T) {
	registry, count := healSocket(t, func(r RPCRequest) *RPCResponse { return &RPCResponse{ID: r.ID, Result: json.RawMessage(`{"ok":true}`)} })
	original := registry
	original.Socket += "-missing"
	reconnects := 0
	health := []bool{}
	client := &Client{Registry: original, Reconnect: func(ctx context.Context, old Registry) (Registry, error) { reconnects++; return registry, nil }, Health: func(live bool) { health = append(health, live) }}
	var out map[string]bool
	if err := client.Call(context.Background(), "browser.cdp", map[string]string{"method": "Input.dispatchKeyEvent"}, &out); err != nil || !out["ok"] {
		t.Fatalf("heal: %v %+v", err, out)
	}
	if count.Load() != 1 || reconnects != 1 || len(health) != 2 || health[0] || !health[1] {
		t.Fatalf("dispatch/reconnect/health: %d %d %v", count.Load(), reconnects, health)
	}
	if client.Registry.Socket != original.Socket {
		t.Fatal("shared client mutated")
	}
}
func TestLostReplyAndNativeTimeoutNeverRetry(t *testing.T) {
	for _, kind := range []string{"lost", "rpc_timeout", "native_write_failed", "wrong-id"} {
		t.Run(kind, func(t *testing.T) {
			registry, count := healSocket(t, func(r RPCRequest) *RPCResponse {
				switch kind {
				case "lost":
					return nil
				case "wrong-id":
					return &RPCResponse{ID: "other"}
				default:
					return &RPCResponse{ID: r.ID, Error: &RPCError{Code: kind, Message: "fixture"}}
				}
			})
			reconnects := 0
			client := &Client{Registry: registry, Reconnect: func(context.Context, Registry) (Registry, error) { reconnects++; return registry, nil }}
			err := client.Call(context.Background(), "browser.cdp", nil, nil)
			if !PossibleDispatch(err) || count.Load() != 1 || reconnects != 0 {
				t.Fatalf("unsafe retry: %v count=%d reconnects=%d", err, count.Load(), reconnects)
			}
		})
	}
}
func TestHealRejectsDifferentProfileOrEpoch(t *testing.T) {
	for _, field := range []string{"profile", "epoch"} {
		t.Run(field, func(t *testing.T) {
			registry, count := healSocket(t, func(r RPCRequest) *RPCResponse { return &RPCResponse{ID: r.ID} })
			original := registry
			original.Socket += "-missing"
			if field == "profile" {
				registry.ProfileID = "other"
			} else {
				registry.Epoch = "other"
			}
			client := &Client{Registry: original, Reconnect: func(context.Context, Registry) (Registry, error) { return registry, nil }}
			if err := client.Call(context.Background(), "browser.close", nil, nil); err == nil || PossibleDispatch(err) || count.Load() != 0 {
				t.Fatalf("cross-profile dispatch: %v %d", err, count.Load())
			}
		})
	}
}
func TestHealNoHandlerAndCanceledContextDoNotRetry(t *testing.T) {
	client := &Client{Registry: Registry{Socket: "/missing-fixture-bridge"}}
	if err := client.Call(context.Background(), "browser.close", nil, nil); err == nil || PossibleDispatch(err) {
		t.Fatalf("connect classification: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client.Reconnect = func(context.Context, Registry) (Registry, error) {
		t.Fatal("reconnected canceled call")
		return Registry{}, errors.New("fixture")
	}
	_ = client.Call(ctx, "browser.close", nil, nil)
}
func TestHealWaitIsBoundedByCaller(t *testing.T) {
	client := &Client{Registry: Registry{Socket: "/missing-fixture-bridge"}, Reconnect: func(ctx context.Context, _ Registry) (Registry, error) { <-ctx.Done(); return Registry{}, ctx.Err() }}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_ = client.Call(ctx, "browser.close", nil, nil)
	if time.Since(start) > time.Second {
		t.Fatal("unbounded recovery")
	}
}
