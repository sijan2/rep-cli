package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testRegistryServer(t *testing.T) *bridgeServer {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &bridgeServer{ctx: ctx, cancel: cancel, registryPath: filepath.Join(dir, "bridge-1.json"), socketPath: filepath.Join(dir, "bridge-1.sock"),
		registrySaved: time.Now(), registryDirty: make(chan struct{}, 1), registry: Registry{PID: 1, Socket: filepath.Join(dir, "bridge-1.sock")}}
}

func pendingRegistryWrite(server *bridgeServer) bool {
	select {
	case <-server.registryDirty:
		return true
	default:
		return false
	}
}

// Every RPC result passes through Touch before it is routed. It must not
// schedule disk work for ordinary traffic inside the refresh interval.
func TestTouchCoalescesLivenessButPersistsIdentity(t *testing.T) {
	server := testRegistryServer(t)
	for range 100 {
		server.Touch(&Message{Action: "rpc_result", ID: "x"})
	}
	if pendingRegistryWrite(server) {
		t.Fatal("ordinary messages inside the refresh interval scheduled a registry write")
	}
	server.Touch(&Message{Action: "hello", ExtensionID: "abc", ExtensionVersion: "1.2.3"})
	if !pendingRegistryWrite(server) {
		t.Fatal("an identity change was not scheduled for persistence")
	}
	if err := server.writeRegistry(); err != nil {
		t.Fatal(err)
	}
	var saved Registry
	data, err := os.ReadFile(server.registryPath)
	if err != nil || json.Unmarshal(data, &saved) != nil || saved.ExtensionID != "abc" || saved.ExtensionVer != "1.2.3" {
		t.Fatalf("identity was not persisted: %s %v", data, err)
	}
	if info, err := os.Stat(server.registryPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("registry must stay private: %v %v", info, err)
	}
	server.registryMu.Lock()
	server.registrySaved = time.Now().Add(-registryRefreshInterval)
	server.registryMu.Unlock()
	server.Touch(&Message{Action: "rpc_result"})
	if !pendingRegistryWrite(server) {
		t.Fatal("stale liveness was not refreshed after the interval")
	}
}

func TestClosedRegistryIsNeverRecreated(t *testing.T) {
	server := testRegistryServer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server.listener = listener
	if err := server.writeRegistry(); err != nil {
		t.Fatal(err)
	}
	server.Close()
	if err := server.writeRegistry(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(server.registryPath); !os.IsNotExist(err) {
		t.Fatalf("a write after Close recreated the registry: %v", err)
	}
}
