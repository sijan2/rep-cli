package commands

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
)

type fakeAsker struct {
	request  jev.AskRequest
	response jev.Response
	err      error
}

func (f *fakeAsker) Ask(_ context.Context, request jev.AskRequest) (jev.Response, error) {
	f.request = request
	return f.response, f.err
}

func locateTestPNG(t *testing.T, width, height int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(1, 1, color.RGBA{R: 200, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buffer.Bytes())
}

// Viewport 400x300 at scroll 0; snapshot in CSS units. BODY > DIV > A(href) > SPAN "Save".
const locateTestSnapshot = `{"strings":["HTML","BODY","DIV","A","SPAN","href","/save","Save","#text","aria-label","Save document"],"documents":[{"scrollOffsetX":0,"scrollOffsetY":0,"contentWidth":400,"nodes":{"parentIndex":[-1,0,1,2,3,4],"nodeType":[1,1,1,1,1,3],"nodeName":[0,1,2,3,4,8],"nodeValue":[-1,-1,-1,-1,-1,7],"backendNodeId":[1,2,3,4,5,6],"attributes":[[],[],[],[5,6,9,10],[],[]],"isClickable":{"index":[3]}},"layout":{"nodeIndex":[0,1,2,3,4],"bounds":[[0,0,400,300],[0,0,400,300],[20,20,360,200],[40,40,120,30],[44,44,100,22]]}}]}`

func locateTestCaller(t *testing.T, shot string, snapshot string) fastTestCaller {
	return func(_ context.Context, method string, in, out any) error {
		switch method {
		case "browser.lease.acquire":
			return fastTestDecode(out, `{"lease_id":"lease","generation":"generation"}`)
		case "browser.lease.release":
			return fastTestDecode(out, `{}`)
		case "browser.cdp":
			p := in.(map[string]any)
			switch p["method"] {
			case "Runtime.evaluate":
				if p["command_params"].(map[string]any)["expression"] == "location.href" {
					return fastTestDecode(out, `{"result":{"result":{"type":"string","value":"https://example.test/doc?tab=2#heading"}}}`)
				}
				return fastTestDecode(out, `{"result":{"result":{"type":"object","value":{"href":"https://example.test/doc","title":"Doc","dpr":2,"w":400,"h":300,"x":0,"y":0,"sw":400}}}}`)
			case "Page.captureScreenshot":
				params := p["command_params"].(map[string]any)
				if _, clipped := params["clip"]; clipped || params["format"] != "jpeg" {
					t.Fatalf("locate must capture the full viewport as JPEG and scale locally: %v", params)
				}
				return fastTestDecode(out, `{"result":{"data":"`+shot+`"}}`)
			case "DOMSnapshot.captureSnapshot":
				return fastTestDecode(out, `{"result":`+snapshot+`}`)
			}
		}
		return errors.New("unexpected RPC " + method)
	}
}

func TestLocateMapsBoxOntoDOMAndControl(t *testing.T) {
	fastTestScope(t)
	asker := &fakeAsker{response: jev.Response{Model: "jev-1.14.0", Usage: jev.Usage{InputTokens: 900}, Answers: map[string]jev.TypedAnswer{
		"location": {Type: "box", Box: &jev.Box{Left: 0.11, Right: 0.36, Top: 0.147, Bottom: 0.22}},
		"visible":  {Type: "noul", Noul: ptr(0.96)},
	}}}
	caller := locateTestCaller(t, locateTestPNG(t, 800, 600), locateTestSnapshot)
	command := newBrowserLocateCommand(jevDOMDependencies{
		loadConfig: func() (jev.Config, error) { return jev.Config{APIKey: "k", Model: "jev-latest"}, nil },
		browser:    func(context.Context, string) (jevdom.Browser, error) { return caller, nil },
		asker:      func(jev.Config) jev.Asker { return asker },
	})
	addDecisionBrowserFlag(command)
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"the Save button", "--tab", "7"})
	if err := command.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stdout.String())
	}
	var result locateOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "located" || result.Node == nil || result.Node.Tag != "span" || result.Control == nil || result.Control.Tag != "a" || result.Control.BackendDOMNodeID != 4 || result.Control.Name != "Save document" {
		t.Fatalf("wrong mapping: %s", stdout.String())
	}
	if result.URL != "https://example.test/doc?tab=2#heading" {
		t.Fatalf("a query or fragment rewrite is the same document and must be tolerated: %s", result.URL)
	}
	if result.ModelRequests != 1 || result.Usage.InputTokens != 900 || result.Screenshot == nil || result.Screenshot.Width != 400 || result.Screenshot.Height != 300 || result.Screenshot.PixelsPerCSSPx != 1 {
		t.Fatalf("accounting lost: %s", stdout.String())
	}
	if _, err := os.Stat(result.Screenshot.Path); err != nil {
		t.Fatalf("screenshot artifact missing: %v", err)
	}
	if asker.request.Assets == nil || len(asker.request.Questions) != 2 || asker.request.Questions["location"].Type != "box" {
		t.Fatalf("model request not built as documented: %+v", asker.request)
	}
	if encoded, _ := json.Marshal(asker.request.State); strings.Contains(string(encoded), "example.test") {
		t.Fatal("page URL must not enter model state")
	}
	for _, name := range []string{"connect", "lease", "page_info", "screenshot", "dom_snapshot", "model", "mapping"} {
		if _, ok := result.Phases[name]; !ok {
			t.Fatalf("phase %s missing: %v", name, result.Phases)
		}
	}
}

func TestLocateReportsAbsenceAndFailsFastWithoutProvider(t *testing.T) {
	fastTestScope(t)
	asker := &fakeAsker{response: jev.Response{Model: "jev-1.14.0", Answers: map[string]jev.TypedAnswer{
		"location": {Type: "box", Box: &jev.Box{Left: 0.5, Right: 0.6, Top: 0.5, Bottom: 0.6}},
		"visible":  {Type: "noul", Noul: ptr(0.12)},
	}}}
	caller := locateTestCaller(t, locateTestPNG(t, 800, 600), locateTestSnapshot)
	command := newBrowserLocateCommand(jevDOMDependencies{
		loadConfig: func() (jev.Config, error) { return jev.Config{APIKey: "k"}, nil },
		browser:    func(context.Context, string) (jevdom.Browser, error) { return caller, nil },
		asker:      func(jev.Config) jev.Asker { return asker },
	})
	addDecisionBrowserFlag(command)
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"a purple elephant", "--tab", "7"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var result locateOutput
	_ = json.Unmarshal(stdout.Bytes(), &result)
	if result.Status != "not_visible" || result.Code != "goal_not_visible" || result.Next == "" {
		t.Fatalf("absence not reported: %s", stdout.String())
	}

	calls := 0
	unconfigured := newBrowserLocateCommand(jevDOMDependencies{
		loadConfig: func() (jev.Config, error) {
			return jev.Config{}, &jev.Error{Code: jev.CodeNotConfigured, Message: "no key"}
		},
		browser: func(context.Context, string) (jevdom.Browser, error) {
			calls++
			return caller, nil
		},
		asker: func(jev.Config) jev.Asker { return asker },
	})
	addDecisionBrowserFlag(unconfigured)
	stdout.Reset()
	unconfigured.SetOut(&stdout)
	unconfigured.SetArgs([]string{"anything", "--tab", "7"})
	if err := unconfigured.Execute(); err == nil || calls != 0 || !strings.Contains(stdout.String(), jev.CodeNotConfigured) {
		t.Fatalf("missing provider must fail before browser work: err=%v calls=%d out=%s", err, calls, stdout.String())
	}
}

func ptr(value float64) *float64 { return &value }
