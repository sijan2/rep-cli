package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/spf13/cobra"
)

type captureSurfaceResult struct {
	url     string
	open    *browserOpenFlags
	fetch   *browserFetchFlags
	command string
	method  string
	params  map[string]interface{}
	timeout time.Duration
}

func captureSurfaceFixture(t *testing.T, args []string) (captureSurfaceResult, string, string) {
	t.Helper()
	var result captureSurfaceResult
	open := func(cmd *cobra.Command, url string, flags browserOpenFlags) error {
		result.url, result.open = url, &flags
		_, err := fmt.Fprintln(cmd.OutOrStdout(), `{"ok":true}`)
		return err
	}
	fetch := func(cmd *cobra.Command, url string, flags browserFetchFlags) error {
		result.url, result.fetch = url, &flags
		_, err := fmt.Fprintln(cmd.OutOrStdout(), `{"ok":true}`)
		return err
	}
	action := func(cmd *cobra.Command, command, method string, params map[string]interface{}, timeout time.Duration) error {
		result.command, result.method, result.params, result.timeout = command, method, params, timeout
		_, err := fmt.Fprintln(cmd.OutOrStdout(), `{"ok":true}`)
		return err
	}
	root := &cobra.Command{Use: "rep"}
	browser := &cobra.Command{Use: "browser"}
	oldBrowser := browserSelector
	t.Cleanup(func() { browserSelector = oldBrowser })
	browser.PersistentFlags().StringVar(&browserSelector, "browser", defaultBrowser, "Browser")
	browser.AddCommand(newBrowserCaptureCommand(open, fetch, action))
	root.AddCommand(browser)
	var out, errs bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errs)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	return result, out.String(), errs.String()
}

func TestCaptureParentDefaultsAndHelpDoNotExecute(t *testing.T) {
	custom, _, _ := captureSurfaceFixture(t, []string{"browser", "capture", "--timeout", "2s", "--max-body", "12", "action", "42", "--tab", "7"})
	if custom.timeout != 12*time.Second || custom.params["timeout_ms"] != int64(2000) || custom.params["max_body_bytes"] != 12 || custom.command != "browser action" || custom.method != "browser.action" {
		t.Fatalf("capture RPC deadline or identity changed: %#v", custom)
	}
	for _, operation := range []string{"open", "fetch", "action"} {
		args := []string{"browser", "capture", operation, "https://example.test/"}
		if operation == "action" {
			args = []string{"browser", "capture", "action", "42", "--tab", "7"}
		}
		got, _, _ := captureSurfaceFixture(t, args)
		switch operation {
		case "open":
			if got.open.Timeout != 30*time.Second || got.open.MaxBodyBytes != 8*1024*1024 || got.open.Idle != 800*time.Millisecond || got.open.KeepTab || got.open.Active || got.open.TabID != -1 {
				t.Fatalf("open defaults changed: %#v", got.open)
			}
		case "fetch":
			if got.fetch.Timeout != 30*time.Second || got.fetch.MaxBodyBytes != 8*1024*1024 || got.fetch.Credentials != "include" || got.fetch.Cache != "default" || got.fetch.TabID != -1 {
				t.Fatalf("fetch defaults changed: %#v", got.fetch)
			}
		case "action":
			if got.timeout != 40*time.Second || got.params["max_body_bytes"] != 8*1024*1024 || got.params["idle_ms"] != int64(300) || got.params["settle_ms"] != int64(1500) || got.params["protocol_payloads"] != false || got.params["webrtc_media"] != false {
				t.Fatalf("action defaults changed: %#v", got)
			}
		}
	}
	for _, args := range [][]string{{"browser", "capture"}, {"browser", "capture", "action", "--help"}} {
		got, out, errs := captureSurfaceFixture(t, args)
		if !reflect.DeepEqual(got, captureSurfaceResult{}) || !strings.Contains(out, "Usage:") || errs != "" {
			t.Fatalf("help executed capture or emitted deprecation: args=%v result=%#v stdout=%q stderr=%q", args, got, out, errs)
		}
	}
}

func TestCaptureTreeKeepsScopeAndRestrictsCaptureFlags(t *testing.T) {
	for _, operation := range []string{"open", "fetch", "action"} {
		command, _, err := rootCmd.Find([]string{"browser", "capture", operation})
		if err != nil || command.Name() != operation || command.Parent().Name() != "capture" || !commandRequiresScope(command) {
			t.Fatalf("capture scope guard missing for %s: %v", operation, err)
		}
		for _, name := range []string{"timeout", "max-body", "protocol-payloads", "webrtc-media"} {
			if command.LocalNonPersistentFlags().Lookup(name) != nil || command.InheritedFlags().Lookup(name) == nil {
				t.Fatalf("%s does not inherit %s from capture", operation, name)
			}
		}
	}
	for _, operation := range []string{"eval", "tabs", "create"} {
		command, _, err := rootCmd.Find([]string{"browser", operation})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"max-body", "protocol-payloads", "webrtc-media"} {
			if command.Flags().Lookup(name) != nil || command.InheritedFlags().Lookup(name) != nil {
				t.Fatalf("ordinary browser %s acquired capture flag %s", operation, name)
			}
		}
	}
}

func TestCaptureSpellingsRetainRunEvidenceAndVerifiedArchive(t *testing.T) {
	for _, path := range [][]string{
		{"browser", "capture", "open"},
		{"browser", "capture", "fetch"},
		{"browser", "capture", "action"},
	} {
		t.Run(strings.Join(path, "-"), func(t *testing.T) {
			store, run := operationEvidenceFixture(t)
			t.Setenv("REPLIVE_PATH", "")
			fixture := createBrowserSnapshotFixture(t, "surface-capture", "surface-request")
			operation := path[len(path)-1]
			if operation == "browse" {
				operation = "open"
			}
			method := "browser." + operation
			server := newFakeBrowserBridgeServer(t, func(request bridge.RPCRequest) fakeBrowserBridgeReply {
				switch request.Method {
				case "bridge.ping":
					return fakeBrowserBridgeReply{result: map[string]any{"connected": true}}
				case "bridge.capture.capabilities":
					return fakeBrowserBridgeReply{result: map[string]any{"schema": 1, "immutable_snapshots": true, "host_instance": "fixture-host", "request_chunks": true, "max_snapshot_bytes": browserSnapshotMaxBytes}}
				case method:
					page, err := store.ListOperations(run.ID, "", 10)
					if err != nil || len(page.Operations) != 1 || page.Operations[0].Kind != method || page.Operations[0].Status != "pending" {
						t.Errorf("capture dispatched before its pending operation: %+v %v", page, err)
					}
					return fakeBrowserBridgeReply{result: map[string]any{"session_id": "surface-capture", "requests": 1, "tab_id": 12}}
				case "bridge.capture.get":
					return fakeBrowserBridgeReply{result: fixture.reference}
				default:
					t.Errorf("unexpected browser method %s", request.Method)
					return fakeBrowserBridgeReply{rpcErr: &bridge.RPCError{Code: "fixture_failed", Message: "unexpected method"}}
				}
			})
			root := &cobra.Command{Use: "rep"}
			root.PersistentFlags().StringVar(&evidenceRunID, "run", "", "Evidence run")
			browser := &cobra.Command{Use: "browser"}
			oldBrowser := browserSelector
			t.Cleanup(func() { browserSelector = oldBrowser })
			browser.PersistentFlags().StringVar(&browserSelector, "browser", defaultBrowser, "Browser")
			browser.AddCommand(newBrowserCaptureCommand(runBrowserOpen, runBrowserFetch, callBrowserRPC))
			root.AddCommand(browser)
			args := append([]string{"--run", run.ID}, path...)
			operand := "https://surface-request.test/"
			if operation == "action" {
				operand = "42"
			}
			args = append(args, operand, "--tab", "12")
			root.SetArgs(args)
			var stderr bytes.Buffer
			root.SetErr(&stderr)
			out, err := os.CreateTemp(t.TempDir(), "output")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = out.Close() })
			oldStdout, oldRaw := os.Stdout, rawJSON
			os.Stdout, rawJSON = out, true
			err = root.Execute()
			os.Stdout, rawJSON = oldStdout, oldRaw
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(out.Name())
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Evidence                operationEvidenceRef `json:"evidence"`
				SavedHashID             string               `json:"saved_hash_id"`
				CaptureSnapshotVerified bool                 `json:"capture_snapshot_verified"`
			}
			if err := json.Unmarshal(data, &result); err != nil || result.Evidence.RunID != run.ID || result.Evidence.OperationID == "" || result.SavedHashID == "" || !result.CaptureSnapshotVerified {
				t.Fatalf("capture lost output evidence or archive: %s %v", data, err)
			}
			page, err := store.ListOperations(run.ID, "", 10)
			if err != nil || len(page.Operations) != 2 {
				t.Fatalf("capture evidence missing: %+v %v", page, err)
			}
			finished := page.Operations[1]
			if finished.Kind != method || finished.Status != "completed" || finished.Verification != "unverified" || finished.Metadata["attempted"] != true || len(finished.References) != 1 || finished.References[0].ID != result.SavedHashID || finished.References[0].SHA256 != fixture.reference.SHA256 {
				t.Fatalf("capture fabricated verification or lost immutable archive binding: %+v", finished)
			}
			if len(server.Requests()) != 4 {
				t.Fatalf("command rename added browser operations: %+v", server.Requests())
			}
			legacy := len(path) < 3
			if strings.Contains(stderr.String(), "deprecated:") != legacy {
				t.Fatalf("unexpected compatibility warning: %q", stderr.String())
			}
		})
	}
}
