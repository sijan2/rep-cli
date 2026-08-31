package bridge

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClientCall(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "bridge.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		var request RPCRequest
		_ = json.NewDecoder(conn).Decode(&request)
		_ = json.NewEncoder(conn).Encode(RPCResponse{
			ID:     request.ID,
			Result: json.RawMessage(`{"ok":true,"method":"` + request.Method + `"}`),
		})
	}()

	client := Client{Registry: Registry{Browser: "arc", Socket: socket}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var result struct {
		OK     bool   `json:"ok"`
		Method string `json:"method"`
	}
	if err := client.Call(ctx, "browser.status", nil, &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Method != "browser.status" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestDiscoverFiltersMissingSocket(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REP_BRIDGE_DIR", dir)
	data, _ := json.Marshal(Registry{PID: 99, Browser: "arc", Socket: filepath.Join(dir, "missing.sock")})
	if err := os.WriteFile(filepath.Join(dir, "bridge-99.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	registries, err := Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(registries) != 0 {
		t.Fatalf("expected missing socket to be ignored, got %+v", registries)
	}
}

func TestDiscoverCleansRegistryForDeadProcess(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "repbridge-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("REP_BRIDGE_DIR", dir)
	socket := filepath.Join(dir, "bridge-99999999.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	data, _ := json.Marshal(Registry{PID: 99999999, Browser: "arc", Socket: socket})
	registryPath := filepath.Join(dir, "bridge-99999999.json")
	if err := os.WriteFile(registryPath, data, 0600); err != nil {
		t.Fatal(err)
	}

	registries, err := Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(registries) != 0 {
		t.Fatalf("expected dead registry to be ignored, got %+v", registries)
	}
	if _, err := os.Stat(registryPath); !os.IsNotExist(err) {
		t.Fatalf("stale registry was not removed: %v", err)
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("stale socket was not removed: %v", err)
	}
}
