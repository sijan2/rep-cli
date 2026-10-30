package commands

import (
	"encoding/json"
	"github.com/repplus/rep-cli/internal/output"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/browserflow"
)

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
func TestActionDeltaIncludesConfirmationWithoutUnchangedText(t *testing.T) {
	result := coreTextDelta("Heading\nPrior evidence", "Heading\nSaved: fixture\nPrior evidence")
	if result != "Saved: fixture" {
		t.Fatal(result)
	}
	if got := coreTextDelta("", strings.Repeat("line\n", 2000)); len(got) > 4000 {
		t.Fatal(len(got))
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
