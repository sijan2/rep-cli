package commands

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/scope"
)

func bridgeProfileFixture(t *testing.T, dir, name, profile, epoch string) {
	t.Helper()
	socket := filepath.Join(dir, profile+".sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				var request bridge.RPCRequest
				if json.NewDecoder(connection).Decode(&request) == nil {
					_ = json.NewEncoder(connection).Encode(bridge.RPCResponse{ID: request.ID, Result: json.RawMessage(`{}`)})
				}
			}()
		}
	}()
	data, _ := json.Marshal(bridge.Registry{PID: os.Getpid(), Browser: name, Socket: socket, ProfileID: profile, Epoch: epoch, LastSeen: time.Now().UTC().Format(time.RFC3339Nano)})
	if err := os.WriteFile(filepath.Join(dir, profile+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestCoreResolverKeepsProfileIdentityAcrossReconnectAndRefusesFallback(t *testing.T) {
	directory, err := os.MkdirTemp("", "rep-resolver-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	t.Setenv("REP_BRIDGE_DIR", directory)
	t.Setenv("REP_BROWSER", "profile-a")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	selected, err := scope.Configure(scope.Options{Workspace: "fixture", Task: "a"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scope.Reset)
	store, _ := coreStore()
	bridgeProfileFixture(t, directory, "dia", "profile-a", "epoch-a")
	bridgeProfileFixture(t, directory, "dia", "profile-b", "epoch-b")
	_, browser, err := resolveCoreBrowser(context.Background(), store, nil)
	if err != nil || browser.Profile != "profile-a" {
		t.Fatalf("explicit profile %v %+v", err, browser)
	}
	tab, err := store.Register(browser, 1, selected.Identity(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("REP_BROWSER", "profile-b")
	_, chosen, err := resolveCoreBrowser(context.Background(), store, tab)
	if err != nil || chosen.Profile != browser.Profile {
		t.Fatalf("handle lost browser identity: %+v %v", chosen, err)
	}
	_ = os.Remove(filepath.Join(directory, "profile-a.json"))
	_, _, err = resolveCoreBrowser(context.Background(), store, nil)
	if err == nil {
		t.Fatal("remembered unavailable profile fell back to another account")
	}
	if problem, ok := err.(*agentstate.Error); !ok || problem.Code != "browser_unavailable" {
		t.Fatalf("wrong missing-profile error: %v", err)
	}
}
func TestCoreUnpinnedSameBrowserProfilesAreAmbiguous(t *testing.T) {
	directory, err := os.MkdirTemp("", "rep-profiles-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	t.Setenv("REP_BRIDGE_DIR", directory)
	t.Setenv("REP_BROWSER", "dia")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	_, _ = scope.Configure(scope.Options{Workspace: "fixture", Task: "b"})
	t.Cleanup(scope.Reset)
	bridgeProfileFixture(t, directory, "dia", "a", "one")
	bridgeProfileFixture(t, directory, "dia", "b", "two")
	store, _ := coreStore()
	_, _, err = resolveCoreBrowser(context.Background(), store, nil)
	if problem, ok := err.(*agentstate.Error); !ok || problem.Code != "ambiguous_browser" {
		t.Fatalf("same browser profiles silently selected: %v", err)
	}
}
