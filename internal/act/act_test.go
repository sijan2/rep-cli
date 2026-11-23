package act

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/scene"
)

// script holds the page state the fake extension replies with. Each field is
// one thing that can go wrong between observe and act.
type script struct {
	loaderID   string
	resolveErr error
	connected  bool
	labelOK    bool
	keyOK      bool
	quads      [][]float64
	hitBackend int
	descendant bool
	reveal     []bool // per Runtime.evaluate: found?
	atEnd      bool
}

func happy() script {
	return script{loaderID: "L1", connected: true, labelOK: true, keyOK: true, quads: [][]float64{{100, 200, 180, 200, 180, 232, 100, 232}}, hitBackend: 18801, descendant: true}
}

type call struct {
	Method string
	Params map[string]any
}

type fake struct {
	t      *testing.T
	s      script
	calls  []call
	wire   []map[string]any
	evals  int
	failOn string // CDP method whose call returns a transport error
}

func (f *fake) Call(_ context.Context, method string, params any, out any) error {
	f.t.Helper()
	if method != "browser.cdp" {
		f.t.Fatalf("unexpected RPC %s", method)
	}
	p := params.(map[string]any)
	if p["tab_id"] != 7 || p["owner"] != "task-1" {
		f.t.Fatalf("wire lacks tab/owner: %v", p)
	}
	cdpMethod := p["method"].(string)
	cp, _ := p["command_params"].(map[string]any)
	if cp == nil {
		f.t.Fatalf("%s: command_params must be an object", cdpMethod)
	}
	f.calls = append(f.calls, call{cdpMethod, cp})
	f.wire = append(f.wire, p)
	if f.failOn == cdpMethod {
		return errors.New("transport closed")
	}
	result, err := f.reply(cdpMethod, cp)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"result": result})
	return json.Unmarshal(raw, out)
}

func (f *fake) reply(method string, cp map[string]any) (any, error) {
	switch method {
	case "Page.getFrameTree":
		return map[string]any{"frameTree": map[string]any{"frame": map[string]any{"id": "F1", "loaderId": f.s.loaderID, "url": "https://www.reddit.com/r/x/comments/1/"}}}, nil
	case "DOM.resolveNode":
		if f.s.resolveErr != nil {
			return nil, f.s.resolveErr
		}
		return map[string]any{"object": map[string]any{"objectId": "obj-" + jsonNumber(cp["backendNodeId"])}}, nil
	case "Runtime.callFunctionOn":
		args := cp["arguments"].([]map[string]any)
		if _, isValidation := args[0]["value"]; isValidation {
			return map[string]any{"result": map[string]any{"type": "object", "value": map[string]any{"connected": f.s.connected, "label": "Reply", "label_ok": f.s.labelOK, "key_ok": f.s.keyOK}}}, nil
		}
		return map[string]any{"result": map[string]any{"type": "boolean", "value": f.s.descendant}}, nil
	case "DOM.scrollIntoViewIfNeeded":
		return map[string]any{}, nil
	case "DOM.getContentQuads":
		return map[string]any{"quads": f.s.quads}, nil
	case "DOM.getNodeForLocation":
		return map[string]any{"backendNodeId": f.s.hitBackend, "frameId": "F1"}, nil
	case "Input.dispatchMouseEvent", "Input.dispatchKeyEvent":
		return map[string]any{}, nil
	case "Runtime.evaluate":
		i := f.evals
		f.evals++
		found := i < len(f.s.reveal) && f.s.reveal[i]
		atEnd := f.s.atEnd && i >= len(f.s.reveal)-1
		return map[string]any{"result": map[string]any{"type": "object", "value": map[string]any{"found": found, "at_end": atEnd, "y": float64(900 * i)}}}, nil
	}
	return nil, errors.New("unexpected CDP method " + method)
}

func jsonNumber(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (f *fake) methods() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.Method)
	}
	return out
}

func (f *fake) inputs() int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c.Method, "Input.") {
			n++
		}
	}
	return n
}

var (
	frame6  = scene.Frame6("F1", "L1")
	ref     = scene.Ref{Kind: "n", Frame6: frame6, Backend: 18801}
	expectC = scene.Control{H: scene.NodeHandle(frame6, 18801), Role: "button", Name: "Reply", Owner: "r_t1_jkl", Rel: "contains", Backend: 18801}
)

func TestClickSuccessDispatchesOneClickAtQuadCentre(t *testing.T) {
	f := &fake{t: t, s: happy()}
	out, err := Click(context.Background(), f, 7, "task-1", ref, expectC)
	if err != nil || out.Status != "ok" || out.Code != "" {
		t.Fatalf("outcome %+v err %v", out, err)
	}
	want := []string{"Page.getFrameTree", "DOM.resolveNode", "Runtime.callFunctionOn", "DOM.scrollIntoViewIfNeeded", "DOM.getContentQuads", "DOM.getNodeForLocation", "Input.dispatchMouseEvent", "Input.dispatchMouseEvent", "Input.dispatchMouseEvent"}
	if got := f.methods(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sequence %v", got)
	}
	types := []string{}
	for _, c := range f.calls[6:] {
		types = append(types, c.Params["type"].(string))
		if c.Params["x"] != 140.0 || c.Params["y"] != 216.0 {
			t.Fatalf("click not at the quad centre: %v", c.Params)
		}
		if c.Params["type"] != "mouseMoved" && (c.Params["button"] != "left" || c.Params["clickCount"] != 1) {
			t.Fatalf("button params %v", c.Params)
		}
	}
	if !reflect.DeepEqual(types, []string{"mouseMoved", "mousePressed", "mouseReleased"}) {
		t.Fatalf("event types %v", types)
	}
	// Validation ran against the node with the masked label and the owner's locator.
	v := f.calls[2]
	if v.Params["objectId"] != "obj-18801" || v.Params["returnByValue"] != true {
		t.Fatalf("callFunctionOn params %v", v.Params)
	}
	expected := v.Params["arguments"].([]map[string]any)[0]["value"].(map[string]any)
	if expected["label"] != "reply" || expected["attr"] != "thingid" || expected["value"] != "t1_jkl" {
		t.Fatalf("expected payload %v", expected)
	}
	if f.calls[5].Params["x"] != 140 || f.calls[5].Params["y"] != 216 || f.calls[5].Params["includeUserAgentShadowDOM"] != true {
		t.Fatalf("hit test params %v", f.calls[5].Params)
	}
	if out.Delta["frame6"] != frame6 || out.Delta["backend"] != 18801 {
		t.Fatalf("delta %v", out.Delta)
	}
}

func TestClickXOwnerUsesPermalinkLocator(t *testing.T) {
	f := &fake{t: t, s: happy()}
	x := expectC
	x.Owner = "r_tw_1851000000000000001"
	x.Name = "26 Replies. Reply"
	if _, err := Click(context.Background(), f, 7, "task-1", ref, x); err != nil {
		t.Fatal(err)
	}
	expected := f.calls[2].Params["arguments"].([]map[string]any)[0]["value"].(map[string]any)
	if expected["href"] != "/status/1851000000000000001" || expected["attr"] != "" || expected["label"] != "# replies. reply" {
		t.Fatalf("expected payload %v", expected)
	}
}

func TestClickStaleDocument(t *testing.T) {
	s := happy()
	s.loaderID = "L2"
	f := &fake{t: t, s: s}
	out, err := Click(context.Background(), f, 7, "task-1", ref, expectC)
	if err != nil || out.Status != "failed" || out.Code != "stale_document" {
		t.Fatalf("outcome %+v err %v", out, err)
	}
	if got := f.methods(); !reflect.DeepEqual(got, []string{"Page.getFrameTree"}) {
		t.Fatalf("stale document must stop before touching the node: %v", got)
	}
	if out.Delta["frame6"] != scene.Frame6("F1", "L2") || out.Delta["expected_frame6"] != frame6 {
		t.Fatalf("delta %v", out.Delta)
	}
}

func TestClickControlChanged(t *testing.T) {
	s := happy()
	s.labelOK = false
	f := &fake{t: t, s: s}
	out, err := Click(context.Background(), f, 7, "task-1", ref, expectC)
	if err != nil || out.Code != "control_changed" || out.Delta["expected_label"] != "Reply" {
		t.Fatalf("outcome %+v err %v", out, err)
	}
	if got := f.methods(); !reflect.DeepEqual(got, []string{"Page.getFrameTree", "DOM.resolveNode", "Runtime.callFunctionOn"}) {
		t.Fatalf("sequence %v", got)
	}

	// A node the browser no longer knows is the same failure.
	s = happy()
	s.resolveErr = errors.New("No node with given id found")
	f = &fake{t: t, s: s}
	out, err = Click(context.Background(), f, 7, "task-1", ref, expectC)
	if err != nil || out.Code != "control_changed" || f.inputs() != 0 {
		t.Fatalf("outcome %+v err %v", out, err)
	}

	s = happy()
	s.connected = false
	f = &fake{t: t, s: s}
	if out, _ := Click(context.Background(), f, 7, "task-1", ref, expectC); out.Code != "control_changed" {
		t.Fatalf("detached node: %+v", out)
	}
}

func TestClickPageChangedWhenRecordKeyMoved(t *testing.T) {
	s := happy()
	s.keyOK = false
	f := &fake{t: t, s: s}
	out, err := Click(context.Background(), f, 7, "task-1", ref, expectC)
	if err != nil || out.Code != "page_changed" || out.Delta["expected_owner"] != "r_t1_jkl" {
		t.Fatalf("outcome %+v err %v", out, err)
	}
	if f.inputs() != 0 {
		t.Fatal("input dispatched after a key mismatch")
	}
}

func TestClickOccluded(t *testing.T) {
	s := happy()
	s.hitBackend = 999
	s.descendant = false
	f := &fake{t: t, s: s}
	out, err := Click(context.Background(), f, 7, "task-1", ref, expectC)
	if err != nil || out.Code != "occluded" || out.Delta["hit_backend"] != 999 {
		t.Fatalf("outcome %+v err %v", out, err)
	}
	want := []string{"Page.getFrameTree", "DOM.resolveNode", "Runtime.callFunctionOn", "DOM.scrollIntoViewIfNeeded", "DOM.getContentQuads", "DOM.getNodeForLocation", "DOM.resolveNode", "Runtime.callFunctionOn"}
	if got := f.methods(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sequence %v", got)
	}
	// The descendant check is a `contains` call with the hit node as argument.
	args := f.calls[7].Params["arguments"].([]map[string]any)
	if args[0]["objectId"] != "obj-999" {
		t.Fatalf("descendant check args %v", args)
	}

	// Hitting a child of the target (the icon inside the button) is fine.
	s.descendant = true
	f = &fake{t: t, s: s}
	out, err = Click(context.Background(), f, 7, "task-1", ref, expectC)
	if err != nil || out.Status != "ok" || f.inputs() != 3 || out.Delta["hit_backend"] != 999 {
		t.Fatalf("descendant hit: %+v err %v inputs %d", out, err, f.inputs())
	}
}

func TestClickNotVisibleWithoutQuads(t *testing.T) {
	s := happy()
	s.quads = nil
	f := &fake{t: t, s: s}
	out, err := Click(context.Background(), f, 7, "task-1", ref, expectC)
	if err != nil || out.Code != "not_visible" || f.inputs() != 0 {
		t.Fatalf("outcome %+v err %v", out, err)
	}
}

func TestNoInputAfterAnyValidationFailure(t *testing.T) {
	cases := map[string]func(*script){
		"stale":      func(s *script) { s.loaderID = "L7" },
		"gone":       func(s *script) { s.resolveErr = errors.New("gone") },
		"detached":   func(s *script) { s.connected = false },
		"label":      func(s *script) { s.labelOK = false },
		"key":        func(s *script) { s.keyOK = false },
		"no quads":   func(s *script) { s.quads = nil },
		"occluded":   func(s *script) { s.hitBackend = 5; s.descendant = false },
		"bad quad":   func(s *script) { s.quads = [][]float64{{1, 2}} },
		"all broken": func(s *script) { s.loaderID = ""; s.labelOK = false; s.keyOK = false; s.descendant = false },
	}
	for name, mutate := range cases {
		s := happy()
		mutate(&s)
		f := &fake{t: t, s: s}
		out, err := Click(context.Background(), f, 7, "task-1", ref, expectC)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out.Status != "failed" || out.Code == "" {
			t.Fatalf("%s: %+v", name, out)
		}
		if f.inputs() != 0 {
			t.Fatalf("%s: %d inputs dispatched after a validation failure", name, f.inputs())
		}
	}
}

func TestClickTransportErrorsPropagateAndNeverRetry(t *testing.T) {
	f := &fake{t: t, s: happy(), failOn: "Input.dispatchMouseEvent"}
	out, err := Click(context.Background(), f, 7, "task-1", ref, expectC)
	if err == nil || out.Status != "" || !strings.Contains(err.Error(), "not retried") {
		t.Fatalf("outcome %+v err %v", out, err)
	}
	if f.inputs() != 1 {
		t.Fatalf("a failed dispatch was retried: %d inputs", f.inputs())
	}
	f = &fake{t: t, s: happy(), failOn: "Page.getFrameTree"}
	if _, err := Click(context.Background(), f, 7, "task-1", ref, expectC); err == nil {
		t.Fatal("frame tree transport error swallowed")
	}
	// A cancelled context is an error, not a page verdict.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := happy()
	s.resolveErr = errors.New("context canceled")
	if _, err := Click(ctx, &fake{t: t, s: s}, 7, "task-1", ref, expectC); err == nil {
		t.Fatal("cancelled context reported as control_changed")
	}
}

func TestClickRejectsRecordHandles(t *testing.T) {
	f := &fake{t: t, s: happy()}
	if _, err := Click(context.Background(), f, 7, "task-1", scene.Ref{Kind: "r", Key: "t1_abc"}, expectC); err == nil || len(f.calls) != 0 {
		t.Fatal("record handle accepted as a click target")
	}
}

func TestRevealFindsRecordWithinThreeSteps(t *testing.T) {
	s := happy()
	s.reveal = []bool{false, false, true}
	f := &fake{t: t, s: s}
	out, err := Reveal(context.Background(), f, 7, "task-1", "r_t1_mno", 10)
	if err != nil || out.Status != "ok" || out.Delta["steps"] != 3 {
		t.Fatalf("outcome %+v err %v", out, err)
	}
	if got := f.methods(); !reflect.DeepEqual(got, []string{"Runtime.evaluate", "Runtime.evaluate", "Runtime.evaluate"}) {
		t.Fatalf("sequence %v", got)
	}
	if f.inputs() != 0 {
		t.Fatal("reveal must not dispatch input")
	}
	expr := f.calls[0].Params["expression"].(string)
	if !strings.Contains(expr, `[thingid=\"t1_mno\"]`) || !strings.Contains(expr, "scrollBy") || f.calls[0].Params["awaitPromise"] != true {
		t.Fatalf("expression %q", expr)
	}
	for _, c := range f.calls {
		if c.Params["expression"] != expr {
			t.Fatal("reveal script must be identical on every step")
		}
	}
}

func TestRevealStopsAtEndAndAtMaxSteps(t *testing.T) {
	s := happy()
	s.reveal = []bool{false, false}
	s.atEnd = true
	f := &fake{t: t, s: s}
	out, err := Reveal(context.Background(), f, 7, "task-1", "t1_zzz", 10)
	if err != nil || out.Code != "not_found" || out.Delta["steps"] != 2 || len(f.calls) != 2 {
		t.Fatalf("outcome %+v err %v calls %d", out, err, len(f.calls))
	}

	s = happy()
	s.reveal = []bool{false, false, false, false, false}
	f = &fake{t: t, s: s}
	out, err = Reveal(context.Background(), f, 7, "task-1", "r_tw_1851000000000000003", 3)
	if err != nil || out.Code != "not_revealed" || out.Delta["steps"] != 3 || len(f.calls) != 3 {
		t.Fatalf("outcome %+v err %v calls %d", out, err, len(f.calls))
	}
	expr := f.calls[0].Params["expression"].(string)
	if !strings.Contains(expr, `/status/1851000000000000003`) || !strings.Contains(expr, "querySelector('time')") {
		t.Fatalf("X finder %q", expr)
	}
	if _, err := Reveal(context.Background(), &fake{t: t, s: happy()}, 7, "task-1", `t1_a"b`, 1); err == nil {
		t.Fatal("unsafe key accepted into a page script")
	}
}

func TestRevealGenericKey(t *testing.T) {
	s := happy()
	s.reveal = []bool{true}
	f := &fake{t: t, s: s}
	out, err := Reveal(context.Background(), f, 7, "task-1", "r_dom_item-7", 0)
	if err != nil || out.Status != "ok" || out.Delta["steps"] != 1 {
		t.Fatalf("outcome %+v err %v", out, err)
	}
	expr := f.calls[0].Params["expression"].(string)
	if !strings.Contains(expr, `[id=\"item-7\"]`) || !strings.Contains(expr, `[data-testid=\"item-7\"]`) {
		t.Fatalf("generic finder %q", expr)
	}
}

func TestPress(t *testing.T) {
	f := &fake{t: t, s: happy()}
	out, err := Press(context.Background(), f, 7, "task-1", "Enter")
	if err != nil || out.Status != "ok" {
		t.Fatalf("outcome %+v err %v", out, err)
	}
	if got := f.methods(); !reflect.DeepEqual(got, []string{"Input.dispatchKeyEvent", "Input.dispatchKeyEvent"}) {
		t.Fatalf("sequence %v", got)
	}
	down, up := f.calls[0].Params, f.calls[1].Params
	if down["type"] != "keyDown" || down["text"] != "\r" || down["windowsVirtualKeyCode"] != 13 || up["type"] != "keyUp" || up["key"] != "Enter" {
		t.Fatalf("key events %v %v", down, up)
	}
	f = &fake{t: t, s: happy()}
	if _, err := Press(context.Background(), f, 7, "task-1", "Escape"); err != nil || f.calls[0].Params["type"] != "rawKeyDown" {
		t.Fatalf("escape: %v %v", err, f.calls[0].Params)
	}
	f = &fake{t: t, s: happy()}
	if _, err := Press(context.Background(), f, 7, "task-1", "j"); err != nil || f.calls[0].Params["text"] != "j" {
		t.Fatalf("printable: %v", err)
	}
	f = &fake{t: t, s: happy()}
	if _, err := Press(context.Background(), f, 7, "task-1", "Meta+Q"); err == nil || len(f.calls) != 0 {
		t.Fatal("unsupported key dispatched")
	}
	f = &fake{t: t, s: happy(), failOn: "Input.dispatchKeyEvent"}
	if _, err := Press(context.Background(), f, 7, "task-1", "Enter"); err == nil || len(f.calls) != 1 {
		t.Fatalf("failed key dispatch retried: %d calls", len(f.calls))
	}
}
