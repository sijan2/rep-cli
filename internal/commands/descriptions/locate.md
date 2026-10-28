# `rep browser locate` contract

`rep browser locate GOAL --tab ID` answers "where is this on the visible page"
from a screenshot, then maps the answer onto real DOM geometry. It is a
selection primitive like `browser element select`: it never clicks, types,
scrolls or navigates. Use it when labels are missing, ambiguous or visual
(icons, charts, canvas-adjacent controls, layout questions), or to confirm that
a text-selected control is where a human would see it.

```sh
rep --workspace PROJECT --task TASK browser locate 'the blue Checkout button' --tab ID
rep --workspace PROJECT --task TASK browser locate 'the price of the second listing' --tab ID --timeout 20s
```

## What one call does

1. Reads viewport geometry (`innerWidth`, `innerHeight`, scroll, device pixel ratio).
2. Captures the viewport as JPEG at device resolution (about 50 ms where PNG
   takes 180 to 300 ms) and scales it in Go to one image pixel per CSS pixel
   when TypeSafe's documented limits allow (280 to 1568 px per side, at most
   2,458,624 pixels); wider viewports report a `pixels_per_css_px` below 1.
   Clipped, scaled CDP captures are not used because they return empty data on
   some Chromium builds.
3. Captures one `DOMSnapshot.captureSnapshot` of the top document (one CDP
   call for every layout box; no per-element `getBoundingClientRect` loop).
4. Sends one Jev request with two questions about the image: a `box`
   question (where) and a `noul` question (is it visible). The request carries
   the required `TypeSafe-Danger-Zone-Images: true` header. Only the image and
   its pixel size enter the model state; page text and URLs never do.
5. Converts the returned box fractions to CSS pixels and ranks laid-out
   elements by intersection over union. `control` is the interactive element
   that best explains the box: the node itself, the tightest interactive
   descendant (a button inside a matched wrapper, a link inside a list item),
   or the nearest interactive ancestor that is not a large container. Controls
   without their own label are named from descendant text.
6. Rechecks the document by origin and path after the observation; a query or
   fragment rewrite (Google Docs tabs) is the same document, a path change is
   `page_changed`.

## Output

| Field | Meaning |
|---|---|
| `status` | `located`, `needs_review`, `not_visible`, or `failed` |
| `presence_probability` | Jev's noul probability that the goal is visible |
| `box` | `fractions` as returned, and `css_px` in viewport coordinates |
| `box_confidence` | Jev's box confidence, absent when the provider omits it |
| `node` | Best-overlapping element: `backend_dom_node_id`, tag, role, name, text, box, `iou`, `box_coverage` |
| `control` | The node itself or its nearest interactive ancestor that still overlaps the box |
| `alternatives` | Up to three further overlapping elements |
| `screenshot` | Private JPEG path, size, SHA-256 and scope |
| `coverage` | Documents in the snapshot, layout nodes, elements considered, viewport-skipped elements, snapshot units per CSS px |
| `model_requests`, `usage`, `phases_ms`, `bridge_calls` | Cost accounting |

`located` requires presence at or above 0.8, a box confidence at or above 0.5
when reported, and at least 0.2 IoU with a laid-out element. `needs_review`
preserves the evidence without a claim. `not_visible` means the model judged
the goal absent from the viewport; scroll or open the relevant section and read
again. A missing provider key fails before any browser work.

## Limits

- Only the top document's layout is mapped; iframe content is counted but not
  ranked. Shadow DOM elements appear in the snapshot with their host's geometry.
- Backend DOM node IDs are temporary handles for the current document, not
  durable locators. Verify with `browser page` or an `interact` postcondition.
- TypeSafe marks image input and `box` as not production-ready ("danger zone");
  accuracy and the wire format may change. Box questions need Jev 1.14, which
  `jev-latest` resolves to since 2026-10-14. The documentation warns that all
  models do worse on images than on text, so prefer `browser page` references and
  `browser element select` when a usable label exists.
- Image tokens are billed as input tokens; `usage.input_tokens` includes them.
