package commands

import (
	"encoding/json"
	"github.com/repplus/rep-cli/internal/output"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/browserflow"
)

func TestPageProtectionSurvivesFilteredAndPaginatedControls(t *testing.T) {
	store := &agentstate.Store{Root: t.TempDir()}
	browser := agentstate.Browser{Name: "headless", Profile: "protection", Epoch: "one"}
	tab, err := store.Register(browser, 1, "default/protection", "session", true)
	if err != nil {
		t.Fatal(err)
	}
	tab, lease, err := store.Reserve(browser, tab.Owner, tab.Session, tab.Handle, true, true, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Release(tab.Handle, lease)
	// The current slice has no input controls, but there is a filled control
	// elsewhere on the page and two visible dialogs. Neither may be adopted.
	observation := browserfast.Result{Status: "ready", Generation: "document", URL: "https://example.test/",
		PageState: json.RawMessage(`{"kind":"content","has_input":true,"dialogs":2}`)}
	if _, err := buildCorePage(store, tab, lease, observation, nil, corePageOptions{}); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Tab(tab.Handle)
	if err != nil || !stored.HasInput || !stored.Dialog {
		t.Fatalf("lost page protection: %+v %v", stored, err)
	}
	// A subsequent full-page signal can clear protection after the draft is
	// submitted and the dialogs are dismissed.
	observation.PageState = json.RawMessage(`{"kind":"content","has_input":false,"dialogs":0}`)
	if _, err := buildCorePage(store, tab, lease, observation, nil, corePageOptions{}); err != nil {
		t.Fatal(err)
	}
	stored, err = store.Tab(tab.Handle)
	if err != nil || stored.HasInput || stored.Dialog {
		t.Fatalf("protection did not clear: %+v %v", stored, err)
	}
}

func TestCoreActorKeyContract(t *testing.T) {
	for _, key := range []string{"Enter", "Meta+v", "Control+a", "Shift+Tab", "Control+Shift+ArrowLeft", "PageUp", "F12", "z", "9", "+", "Control++", "é", ":"} {
		event, ok := browserflow.KeyEvent(key)
		if !ok || event["key"] == "" {
			t.Fatalf("missing %s", key)
		}
	}
	for _, key := range []string{"", "Hyper+a", "Control+Control+a", "Enter+Shift", "hello"} {
		if _, ok := browserflow.KeyEvent(key); ok {
			t.Fatalf("accepted %s", key)
		}
	}
	event, _ := browserflow.KeyEvent("Enter")
	if event["text"] != "\r" {
		t.Fatal("native form submission needs Enter text")
	}
	event, _ = browserflow.KeyEvent("Meta+v")
	if _, ok := event["text"]; ok {
		t.Fatal("modifier chord must not insert text")
	}
}
func TestCoreArtifactSecurityAndBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.json")
	asset, err := coreArtifact(path, ".json", []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if len(asset["sha256"].(string)) != 64 {
		t.Fatal(asset)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	if _, err = coreArtifact(path, ".json", []byte("overwrite")); err == nil {
		t.Fatal("overwrote evidence")
	}
	for _, v := range []string{"desktop", "mobile", "1600x900"} {
		if _, _, err = coreViewport(v); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []string{"100x100", "800x99999", "unknown"} {
		if _, _, err = coreViewport(v); err == nil {
			t.Fatal(v)
		}
	}
}
func TestCDPCredentialAndGlobalIsolation(t *testing.T) {
	for _, method := range []string{"Network.getAllCookies", "Network.getCookies", "Storage.getCookies", "DOMStorage.getDOMStorageItems", "IndexedDB.requestData", "CacheStorage.requestCachedResponse", "Target.attachToTarget", "Browser.close", "Runtime.getCookies"} {
		if cdpAllowed(method) {
			t.Fatal(method)
		}
	}
	for _, method := range []string{"Page.addScriptToEvaluateOnNewDocument", "Emulation.setDeviceMetricsOverride", "Input.dispatchKeyEvent", "Runtime.evaluate", "DOM.setFileInputFiles"} {
		if !cdpAllowed(method) {
			t.Fatal(method)
		}
	}
}

func TestCoreDefaultStringValuesRedactStructuredCredentials(t *testing.T) {
	value := output.RedactValue(map[string]any{"value": `{"Authorization":"Bearer synthetic-secret","access_token":"synthetic-token"}`})
	encoded, _ := json.Marshal(value)
	for _, secret := range []string{"synthetic-secret", "synthetic-token"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("credential in default stdout")
		}
	}
}
