// Package act performs single-shot, validated input on scene handles. Every
// CDP command travels through the leased browser.cdp RPC so the tab's owner is
// enforced by the extension, and every check happens on the target node
// itself: there is no whole-page fingerprint to go stale between observe and
// act. A dispatched input is never retried, because a click that did land is
// not distinguishable from one that did not.
package act

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/repplus/rep-cli/internal/scene"
)

// Caller is the browser RPC client (browserrpc.Caller has this shape).
type Caller interface {
	Call(ctx context.Context, method string, params any, out any) error
}

// Outcome reports what happened. Status is "ok" or "failed"; Code names the
// failure (stale_document, page_changed, control_changed, occluded,
// not_visible, not_found, not_revealed); Delta carries the facts behind it.
type Outcome struct {
	Status string
	Code   string
	Delta  map[string]any
}

const defaultRevealSteps = 10

var (
	digits    = regexp.MustCompile(`[0-9]+`)
	spaces    = regexp.MustCompile(`\s+`)
	attrName  = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_:-]*$`)
	keyTable  = map[string]keySpec{"Enter": {13, "\r"}, "Escape": {27, ""}, "Tab": {9, ""}, "Backspace": {8, ""}, "Delete": {46, ""}, "ArrowUp": {38, ""}, "ArrowDown": {40, ""}, "ArrowLeft": {37, ""}, "ArrowRight": {39, ""}, "Home": {36, ""}, "End": {35, ""}, "PageUp": {33, ""}, "PageDown": {34, ""}, "Space": {32, " "}}
	errNoQuad = errors.New("node has no content quads")
)

type keySpec struct {
	vk   int
	text string
}

type session struct {
	ctx   context.Context
	c     Caller
	tab   int
	owner string
}

// cdp sends one command on the leased path and decodes the extension's
// {result} envelope.
func (s session) cdp(method string, params map[string]any, out any) error {
	if params == nil {
		params = map[string]any{}
	}
	var wrapper struct {
		Result json.RawMessage `json:"result"`
	}
	wire := map[string]any{"tab_id": s.tab, "owner": s.owner, "method": method, "command_params": params}
	if err := s.c.Call(s.ctx, "browser.cdp", wire, &wrapper); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if out == nil {
		return nil
	}
	if len(wrapper.Result) == 0 {
		return fmt.Errorf("%s: empty CDP result", method)
	}
	if err := json.Unmarshal(wrapper.Result, out); err != nil {
		return fmt.Errorf("%s: decode result: %w", method, err)
	}
	return nil
}

func failed(code string, delta map[string]any) Outcome {
	if delta == nil {
		delta = map[string]any{}
	}
	return Outcome{Status: "failed", Code: code, Delta: delta}
}

// fail turns a CDP error into an Outcome unless the context is gone, in which
// case nothing can be said about the page and the error propagates.
func (s session) fail(code, stage string, err error) (Outcome, error) {
	if s.ctx.Err() != nil {
		return Outcome{}, err
	}
	return failed(code, map[string]any{"stage": stage, "error": err.Error()}), nil
}

type frameTree struct {
	FrameTree struct {
		Frame struct {
			ID       string `json:"id"`
			LoaderID string `json:"loaderId"`
			URL      string `json:"url"`
		} `json:"frame"`
	} `json:"frameTree"`
}

func (s session) frame() (frameTree, string, error) {
	var tree frameTree
	if err := s.cdp("Page.getFrameTree", nil, &tree); err != nil {
		return tree, "", err
	}
	f := tree.FrameTree.Frame
	return tree, scene.Frame6(f.ID, f.LoaderID), nil
}

type remoteObject struct {
	Object struct {
		ObjectID string `json:"objectId"`
	} `json:"object"`
}

type callResult struct {
	Result struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	} `json:"result"`
	ExceptionDetails json.RawMessage `json:"exceptionDetails"`
}

func (s session) callOn(objectID, fn string, args []map[string]any, out any) error {
	var res callResult
	err := s.cdp("Runtime.callFunctionOn", map[string]any{"objectId": objectID, "functionDeclaration": fn, "arguments": args, "returnByValue": true}, &res)
	if err != nil {
		return err
	}
	if len(res.ExceptionDetails) > 0 {
		return fmt.Errorf("page script threw: %s", string(res.ExceptionDetails))
	}
	if out != nil && len(res.Result.Value) > 0 {
		return json.Unmarshal(res.Result.Value, out)
	}
	return nil
}

// maskLabel is the identity of a control name once the numbers in it are
// ignored: "26 Replies. Reply" and "27 Replies. Reply" are the same button.
func maskLabel(s string) string {
	s = strings.ToLower(strings.TrimSpace(spaces.ReplaceAllString(s, " ")))
	s = digits.ReplaceAllString(s, "#")
	if len(s) > 60 {
		s = s[:60]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// validateScript runs on the target node. It recomputes the accessible name
// with the same precedence scene used, masks digits, and walks up through
// shadow hosts to the nearest record container to confirm the key.
const validateScript = `function(expected) {
  const collapse = (s) => String(s == null ? '' : s).replace(/\s+/g, ' ').trim();
  const mask = (s) => collapse(s).replace(/[0-9]+/g, '#').toLowerCase().slice(0, 60);
  const el = this;
  if (!el || !(el instanceof Node) || !el.isConnected) return { connected: false };
  let label = '';
  if (el.nodeType === 1) {
    label = el.getAttribute('aria-label') || '';
    if (!collapse(label)) {
      const root = el.getRootNode && el.getRootNode();
      const lookup = (id) => (root && root.getElementById ? root.getElementById(id) : null) || document.getElementById(id);
      label = (el.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean).map((id) => { const n = lookup(id); return n ? n.textContent : ''; }).join(' ');
    }
    if (!collapse(label)) label = el.textContent || '';
    if (!collapse(label)) label = el.getAttribute('title') || '';
    if (!collapse(label)) { const img = el.querySelector('img[alt]'); if (img) label = img.getAttribute('alt'); }
    if (!collapse(label)) { const t = el.querySelector('svg title'); if (t) label = t.textContent; }
    if (!collapse(label)) label = el.getAttribute('placeholder') || '';
  }
  label = collapse(label);
  const got = mask(label);
  const want = mask(expected.label);
  const labelOk = !want || got === want || got.startsWith(want) || want.startsWith(got);
  let keyOk = true;
  if (expected.attr || expected.href || expected.value) {
    keyOk = false;
    let node = el;
    while (node && node.nodeType !== 9) {
      if (node.nodeType === 1) {
        if (expected.attr) {
          if (node.hasAttribute(expected.attr)) { keyOk = node.getAttribute(expected.attr) === expected.value; break; }
        } else if (expected.href) {
          const container = node.localName === 'article' || (node.getAttribute('role') === 'link' && node.querySelector('time'));
          if (container) {
            keyOk = Array.from(node.querySelectorAll('a[href]')).some((a) => (a.getAttribute('href') || '').includes(expected.href) && a.querySelector('time'));
            break;
          }
        } else {
          for (const a of node.attributes) { if (a.value === expected.value) { keyOk = true; break; } }
          if (keyOk) break;
        }
      }
      node = node.parentNode || node.host || null;
    }
  }
  return { connected: true, label, label_ok: labelOk, key_ok: keyOk };
}`

// containsScript answers whether `other` is this node or inside it, crossing
// shadow boundaries, which Node.contains does not.
const containsScript = `function(other) {
  let node = other;
  while (node) { if (node === this) return true; node = node.parentNode || node.host || null; }
  return false;
}`

type validation struct {
	Connected bool   `json:"connected"`
	Label     string `json:"label"`
	LabelOK   bool   `json:"label_ok"`
	KeyOK     bool   `json:"key_ok"`
}

// Click validates ref against expect and clicks the centre of its first
// content quad. The steps and their failure codes follow the G2 design:
// frame identity (stale_document), node-local label and record key
// (control_changed, page_changed), hit test (occluded), then one
// mouseMoved/mousePressed/mouseReleased sequence.
func Click(ctx context.Context, c Caller, tab int, owner string, ref scene.Ref, expect scene.Control) (Outcome, error) {
	if ref.Kind != "n" || ref.Backend <= 0 || ref.Frame6 == "" {
		return Outcome{}, errors.New("act: Click needs a node handle (n_<frame6>.<backendNodeId>)")
	}
	s := session{ctx: ctx, c: c, tab: tab, owner: owner}

	tree, frame6, err := s.frame()
	if err != nil {
		return Outcome{}, err
	}
	if frame6 != ref.Frame6 {
		return failed("stale_document", map[string]any{"expected_frame6": ref.Frame6, "frame6": frame6, "url": tree.FrameTree.Frame.URL}), nil
	}

	var obj remoteObject
	if err := s.cdp("DOM.resolveNode", map[string]any{"backendNodeId": ref.Backend}, &obj); err != nil {
		return s.fail("control_changed", "resolve", err)
	}
	if obj.Object.ObjectID == "" {
		return failed("control_changed", map[string]any{"stage": "resolve", "error": "node did not resolve"}), nil
	}

	expected := map[string]any{"label": maskLabel(expect.Name)}
	if expect.Owner != "" {
		loc, err := scene.Locate(expect.Owner)
		if err != nil {
			return Outcome{}, fmt.Errorf("act: owner handle: %w", err)
		}
		expected["attr"], expected["value"], expected["href"] = loc.Attr, loc.Value, loc.Href
	}
	var v validation
	if err := s.callOn(obj.Object.ObjectID, validateScript, []map[string]any{{"value": expected}}, &v); err != nil {
		return s.fail("control_changed", "validate", err)
	}
	if !v.Connected {
		return failed("control_changed", map[string]any{"stage": "validate", "error": "node detached"}), nil
	}
	if expect.Name != "" && !v.LabelOK {
		return failed("control_changed", map[string]any{"expected_label": expect.Name, "label": v.Label}), nil
	}
	if expect.Owner != "" && !v.KeyOK {
		return failed("page_changed", map[string]any{"expected_owner": expect.Owner, "label": v.Label}), nil
	}

	if err := s.cdp("DOM.scrollIntoViewIfNeeded", map[string]any{"backendNodeId": ref.Backend}, nil); err != nil {
		return s.fail("not_visible", "scroll", err)
	}
	x, y, err := s.centre(ref.Backend)
	if err != nil {
		return s.fail("not_visible", "quads", err)
	}

	var hit struct {
		BackendNodeID int `json:"backendNodeId"`
	}
	if err := s.cdp("DOM.getNodeForLocation", map[string]any{"x": int(math.Round(x)), "y": int(math.Round(y)), "includeUserAgentShadowDOM": true}, &hit); err != nil {
		return s.fail("occluded", "hit_test", err)
	}
	if hit.BackendNodeID != ref.Backend {
		inside, err := s.descendant(obj.Object.ObjectID, hit.BackendNodeID)
		if err != nil {
			return s.fail("occluded", "hit_test", err)
		}
		if !inside {
			return failed("occluded", map[string]any{"hit_backend": hit.BackendNodeID, "x": x, "y": y}), nil
		}
	}

	for _, kind := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		params := map[string]any{"type": kind, "x": x, "y": y}
		if kind != "mouseMoved" {
			params["button"] = "left"
			params["clickCount"] = 1
		}
		if err := s.cdp("Input.dispatchMouseEvent", params, nil); err != nil {
			return Outcome{}, fmt.Errorf("act: input dispatched up to %s, not retried: %w", kind, err)
		}
	}
	return Outcome{Status: "ok", Delta: map[string]any{"x": x, "y": y, "backend": ref.Backend, "frame6": frame6, "hit_backend": hit.BackendNodeID, "label": v.Label}}, nil
}

// centre is the midpoint of the first content quad, in viewport CSS pixels,
// the frame DOM.getNodeForLocation and Input events share.
func (s session) centre(backend int) (float64, float64, error) {
	var quads struct {
		Quads [][]float64 `json:"quads"`
	}
	if err := s.cdp("DOM.getContentQuads", map[string]any{"backendNodeId": backend}, &quads); err != nil {
		return 0, 0, err
	}
	if len(quads.Quads) == 0 || len(quads.Quads[0]) != 8 {
		return 0, 0, errNoQuad
	}
	q := quads.Quads[0]
	x := (q[0] + q[2] + q[4] + q[6]) / 4
	y := (q[1] + q[3] + q[5] + q[7]) / 4
	if math.IsNaN(x) || math.IsNaN(y) {
		return 0, 0, errNoQuad
	}
	return x, y, nil
}

func (s session) descendant(targetObjectID string, hitBackend int) (bool, error) {
	if hitBackend <= 0 {
		return false, nil
	}
	var other remoteObject
	if err := s.cdp("DOM.resolveNode", map[string]any{"backendNodeId": hitBackend}, &other); err != nil {
		return false, err
	}
	var inside bool
	if err := s.callOn(targetObjectID, containsScript, []map[string]any{{"objectId": other.Object.ObjectID}}, &inside); err != nil {
		return false, err
	}
	return inside, nil
}

// Press sends one key: a named key from the table or a single printable
// character. No validation precedes it because the target is the focused
// element, which the page owns.
func Press(ctx context.Context, c Caller, tab int, owner string, key string) (Outcome, error) {
	s := session{ctx: ctx, c: c, tab: tab, owner: owner}
	down := map[string]any{"type": "rawKeyDown", "key": key}
	up := map[string]any{"type": "keyUp", "key": key}
	if spec, ok := keyTable[key]; ok {
		code := key
		down["code"], up["code"] = code, code
		down["windowsVirtualKeyCode"], up["windowsVirtualKeyCode"] = spec.vk, spec.vk
		if spec.text != "" {
			down["type"] = "keyDown"
			down["text"], down["unmodifiedText"] = spec.text, spec.text
		}
	} else if r, size := utf8.DecodeRuneInString(key); size == len(key) && r != utf8.RuneError && unicode.IsPrint(r) && !unicode.IsSpace(r) {
		down["type"] = "keyDown"
		down["text"], down["unmodifiedText"] = key, key
	} else {
		return Outcome{}, fmt.Errorf("act: unsupported key %q", key)
	}
	for _, params := range []map[string]any{down, up} {
		if err := s.cdp("Input.dispatchKeyEvent", params, nil); err != nil {
			return Outcome{}, fmt.Errorf("act: key dispatched up to %s, not retried: %w", params["type"], err)
		}
	}
	return Outcome{Status: "ok", Delta: map[string]any{"key": key}}, nil
}

// revealScript checks for the record, and when it is absent scrolls one
// viewport and waits two frames so virtualized lists get a chance to render
// before the next check. It never mutates the DOM.
func revealScript(loc scene.Locator) string {
	var finder string
	switch {
	case loc.Attr != "":
		finder = fmt.Sprintf("return document.querySelector(%s);", jsString("["+loc.Attr+"="+jsString(loc.Value)+"]"))
	case loc.Href != "":
		finder = fmt.Sprintf("return Array.from(document.querySelectorAll(%s)).find((a) => a.querySelector('time')) || null;", jsString("a[href*="+jsString(loc.Href)+"]"))
	default:
		v := jsString(loc.Value)
		selector := fmt.Sprintf("[thingid=%s],[id=%s],[data-testid=%s],[data-id=%s],[name=%s]", v, v, v, v, v)
		finder = fmt.Sprintf("return document.querySelector(%s) || Array.from(document.querySelectorAll(%s)).find((a) => a.querySelector('time')) || null;", jsString(selector), jsString("a[href*="+jsString("/status/"+loc.Value)+"]"))
	}
	return `(async () => {
  const find = () => { ` + finder + ` };
  const max = () => Math.max(0, document.documentElement.scrollHeight - window.innerHeight);
  if (find()) return { found: true, at_end: false, y: window.scrollY, scrolled: false };
  if (window.scrollY >= max() - 1) return { found: false, at_end: true, y: window.scrollY, scrolled: false };
  window.scrollBy(0, window.innerHeight);
  await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  return { found: Boolean(find()), at_end: window.scrollY >= max() - 1, y: window.scrollY, scrolled: true };
})()`
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Reveal scrolls toward a record (handle or bare key) in at most maxSteps
// viewport-sized steps until its container renders. It returns as soon as the
// record exists; the caller takes a fresh snapshot to get its controls.
func Reveal(ctx context.Context, c Caller, tab int, owner string, key string, maxSteps int) (Outcome, error) {
	loc, err := scene.Locate(key)
	if err != nil {
		return Outcome{}, fmt.Errorf("act: %w", err)
	}
	if loc.Attr != "" && !attrName.MatchString(loc.Attr) {
		return Outcome{}, fmt.Errorf("act: bad key attribute %q", loc.Attr)
	}
	if maxSteps <= 0 {
		maxSteps = defaultRevealSteps
	}
	s := session{ctx: ctx, c: c, tab: tab, owner: owner}
	expression := revealScript(loc)
	for step := 1; step <= maxSteps; step++ {
		var res callResult
		if err := s.cdp("Runtime.evaluate", map[string]any{"expression": expression, "awaitPromise": true, "returnByValue": true}, &res); err != nil {
			return Outcome{}, err
		}
		if len(res.ExceptionDetails) > 0 {
			return failed("reveal_failed", map[string]any{"step": step, "error": string(res.ExceptionDetails)}), nil
		}
		var state struct {
			Found bool    `json:"found"`
			AtEnd bool    `json:"at_end"`
			Y     float64 `json:"y"`
		}
		if len(res.Result.Value) > 0 {
			if err := json.Unmarshal(res.Result.Value, &state); err != nil {
				return Outcome{}, fmt.Errorf("act: reveal state: %w", err)
			}
		}
		if state.Found {
			return Outcome{Status: "ok", Delta: map[string]any{"steps": step, "y": state.Y, "key": key}}, nil
		}
		if state.AtEnd {
			return failed("not_found", map[string]any{"steps": step, "y": state.Y, "key": key}), nil
		}
	}
	return failed("not_revealed", map[string]any{"steps": maxSteps, "key": key}), nil
}
