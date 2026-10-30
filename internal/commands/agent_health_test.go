package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

func TestScopeOfflineInventoryAndBoundedJournal(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("REP_BROWSER", "headless")
	selected, err := scope.ConfigureInvocation(scope.Options{Task: "fixture/a", TaskSet: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scope.Reset)
	store, _ := coreStore()
	browser := agentstate.Browser{Name: "headless", Profile: "fixture-profile", Epoch: "e", PrivateName: "fixture"}
	_ = store.Remember(selected.Identity(), selected.SessionKey, browser)
	tab, _ := store.Register(browser, 7, selected.Identity(), selected.SessionKey, true)
	for i := 0; i < 25; i++ {
		_ = store.Journal(selected.Identity(), tab.Handle, "do", "performed")
	}
	_ = store.Journal("fixture/other", "t999", "eval", "performed")
	_ = store.Health(selected.Identity(), false)
	_ = os.MkdirAll(filepath.Join(selected.DataDir, "body-exports"), 0700)
	_ = os.WriteFile(filepath.Join(selected.DataDir, "body-exports", "fixture"), []byte("abc"), 0600)
	_ = os.Symlink("/not-a-task", filepath.Join(selected.DataDir, "outside"))
	result, err := readCoreScope(selected)
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile != browser.Profile || len(result.Tabs) != 1 || result.Tabs[0] != tab.Handle || result.Storage["body-exports"] != 3 || len(result.Journal) != 20 || result.JournalComplete || result.BridgeDownSince == nil {
		t.Fatalf("scope: %+v", result)
	}
	for _, entry := range result.Journal {
		if entry["task"] != selected.Identity() || entry["op_id"] == "" {
			t.Fatalf("journal crossed task: %+v", entry)
		}
	}
}
func TestDoctorManifestRepairIsPrivateAndMergesOrigins(t *testing.T) {
	dir := t.TempDir()
	host := filepath.Join(dir, "rep-host")
	_ = os.WriteFile(host, []byte("fixture"), 0700)
	path := filepath.Join(dir, "manifests", "host.json")
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	data, _ := json.Marshal(nativeHostManifest{Name: "com.repplus.host", Path: "old", Type: "stdio", AllowedOrigins: []string{"chrome-extension://other/"}})
	_ = os.WriteFile(path, data, 0600)
	changed, err := repairCoreManifest(path, host, "fixture")
	if err != nil || !changed {
		t.Fatalf("repair: %v %v", changed, err)
	}
	var got nativeHostManifest
	data, _ = os.ReadFile(path)
	_ = json.Unmarshal(data, &got)
	if got.Path != host || len(got.AllowedOrigins) != 2 {
		t.Fatalf("manifest: %+v", got)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("manifest is not private")
	}
	changed, err = repairCoreManifest(path, host, "fixture")
	if err != nil || changed {
		t.Fatalf("repair not idempotent: %v %v", changed, err)
	}
	before := string(data)
	if _, err = repairCoreManifest(path, filepath.Join(dir, "absent"), "fixture"); err == nil {
		t.Fatal("accepted absent host")
	}
	data, _ = os.ReadFile(path)
	if string(data) != before {
		t.Fatal("failed repair overwrote manifest")
	}
}
func TestOutcomeUnknownHasVerificationNext(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	_, _ = scope.ConfigureInvocation(scope.Options{Task: "fixture/a", TaskSet: true}, "")
	t.Cleanup(scope.Reset)
	store, _ := coreStore()
	b := agentstate.Browser{Name: "dia", Profile: "p", Epoch: "e"}
	tab, _ := store.Register(b, 1, "fixture/a", "", true)
	_ = store.Remember("fixture/a", "", b)
	cmd := &cobra.Command{Use: "eval"}
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	err := coreFailure(cmd, &bridge.TransportError{Dispatched: true, Cause: errors.New("lost reply")})
	if err == nil {
		t.Fatal("lost reply claimed success")
	}
	var got coreResult
	if json.Unmarshal(out.Bytes(), &got) != nil || got.Error.Code != "outcome_unknown" || got.Error.Retry != "do-not-retry" || got.Error.Next != "rep page "+tab.Handle+" --task fixture/a" {
		t.Fatalf("unsafe failure: %s", out)
	}
}
func TestResolverWaitsForSameProfileWithoutRestart(t *testing.T) {
	dir, err := os.MkdirTemp("", "health-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("REP_BRIDGE_DIR", dir)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("REP_BROWSER", "dia")
	selected, _ := scope.ConfigureInvocation(scope.Options{Task: "fixture/a", TaskSet: true}, "")
	t.Cleanup(scope.Reset)
	store, _ := coreStore()
	browser := agentstate.Browser{Name: "dia", Profile: "fixture", Epoch: "e"}
	_ = store.Remember(selected.Identity(), selected.SessionKey, browser)
	_ = store.Health(selected.Identity(), true)
	socket := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			go func() {
				defer conn.Close()
				var req bridge.RPCRequest
				if json.NewDecoder(conn).Decode(&req) == nil {
					_ = json.NewEncoder(conn).Encode(bridge.RPCResponse{ID: req.ID, Result: json.RawMessage(`{"connected":true}`)})
				}
			}()
		}
	}()
	published := make(chan struct{})
	go func() {
		defer close(published)
		time.Sleep(50 * time.Millisecond)
		data, _ := json.Marshal(bridge.Registry{PID: os.Getpid(), Browser: "dia", ProfileID: "fixture", Epoch: "e", Socket: socket})
		_ = os.WriteFile(filepath.Join(dir, "bridge.json"), data, 0600)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, got, err := resolveCoreBrowser(ctx, store, nil)
	<-published
	if err != nil || client == nil || got.Profile != "fixture" || got.Epoch != "e" {
		t.Fatalf("reconnect: %+v %v", got, err)
	}
	state, _ := store.Read()
	if state.Tasks[selected.Identity()].BridgeDownSince != nil {
		t.Fatal("heal left down marker")
	}
}
func TestCloseUserTabDoesNotAdoptIt(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	_, _ = scope.ConfigureInvocation(scope.Options{Task: "fixture/a", TaskSet: true}, "")
	t.Cleanup(scope.Reset)
	store, _ := coreStore()
	b := agentstate.Browser{Name: "dia", Profile: "p", Epoch: "e"}
	_ = store.Inventory(b, []int{7})
	err := coreClose(&cobra.Command{Use: "close"}, []string{"t1"})
	if err == nil || !strings.Contains(err.Error(), "did not create") {
		t.Fatalf("user close: %v", err)
	}
	tab, _ := store.Tab("t1")
	if tab.Owner != "" {
		t.Fatal("refused close adopted a user tab")
	}
}
