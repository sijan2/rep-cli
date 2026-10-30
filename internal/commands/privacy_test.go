package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

func TestDefaultCaptureCommandsDoNotPrintCredentials(t *testing.T) {
	dir := t.TempDir()
	fixture := store.Export{Requests: []store.Request{{ID: "h_fixture", Method: "POST", URL: "https://fixture.invalid/api", Domain: "fixture.invalid", Headers: store.HeaderMap{"Cookie": {"fixture-cookie-secret"}, "Authorization": {"Bearer fixture-auth-secret"}, "Content-Type": {"application/json"}}, Body: `{"password":"fixture-password-secret"}`, Response: &store.Response{Status: 200, Headers: store.HeaderMap{"Set-Cookie": {"fixture-cookie-secret"}, "Content-Type": {"application/json"}}, Body: `{"access_token":"fixture-token-secret","data":[1]}`}}}}
	data, _ := json.Marshal(fixture)
	path := filepath.Join(dir, "capture.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"list", "--primary=false"}, {"detail", "h_fixture"}, {"body", "h_fixture"}, {"auth"}, {"curl", "h_fixture"}} {
		t.Run(args[0], func(t *testing.T) {
			arguments := append([]string{"-test.run=^TestEntryPointHelperProcess$", "--"}, args...)
			arguments = append(arguments, "--global")
			command := exec.Command(os.Args[0], arguments...)
			command.Dir = dir
			for _, item := range os.Environ() {
				key, _, _ := strings.Cut(item, "=")
				if key != "REP_WORKSPACE" && key != "REP_TASK" && key != "XDG_DATA_HOME" && key != "REPLIVE_PATH" {
					command.Env = append(command.Env, item)
				}
			}
			command.Env = append(command.Env, "REP_ENTRY_POINT_TEST=1", "XDG_DATA_HOME="+filepath.Join(dir, "data"), "REPLIVE_PATH="+path)
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			if err := command.Run(); err != nil {
				t.Fatalf("%v: stdout=%s stderr=%s", err, &stdout, &stderr)
			}
			if !json.Valid(stdout.Bytes()) {
				t.Fatalf("not one JSON result: %s", &stdout)
			}
			for _, secret := range []string{"fixture-cookie-secret", "fixture-auth-secret", "fixture-password-secret", "fixture-token-secret"} {
				if strings.Contains(stdout.String(), secret) {
					t.Fatalf("credential leaked: %s", secret)
				}
			}
		})
	}
}

func TestBodySelectionCannotBypassCredentialRedaction(t *testing.T) {
	old, oldSave, oldRequest, oldHead := bodyView, bodySave, bodyRequest, bodyHead
	t.Cleanup(func() { bodyView, bodySave, bodyRequest, bodyHead = old, oldSave, oldRequest, oldHead })
	bodyView = bodyViewFlags{Format: "json", Pointer: "/access_token", MaxBytes: 4096, Records: 20}
	bodySave, bodyRequest, bodyHead = false, false, 0
	command := &cobra.Command{}
	command.Flags().String("pointer", "", "")
	_ = command.Flags().Set("pointer", "/access_token")
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	req := &store.Request{ID: "h_fixture", Response: &store.Response{Headers: store.HeaderMap{"Content-Type": {"application/json"}}, Body: `{"access_token":"fixture-token-secret"}`}}
	if err := renderCapturedBody(command, req); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "fixture-token-secret") || !strings.Contains(stdout.String(), "REDACTED") {
		t.Fatalf("unsafe projection: %s", &stdout)
	}
}
