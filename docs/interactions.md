# Page reads, actions and plans

`rep page URL` opens or reuses an idle owned tab. `rep page t3 URL` navigates an explicit tab; `rep page t3` reads it. Select a heading with `--section`, find literal text or `css:SELECTOR` / `role:ROLE NAME` with `--find`, reload with `--reload`, or set a sticky `--viewport desktop|mobile|WxH`. Document text preserves code, tables, links and accessible same-origin frames. A Docs-like page uses its authenticated text export when available. Always inspect completeness and page state.

`rep do t3.e12` clicks an observed control. A following VALUE fills a text field, selects an option or sets a checkbox. `--press` takes a key, chord or sequence, for example `rep do t3.e12 query --press Enter`. A value starting with a hyphen follows `--`. Repeat `--file PATH` for uploads through a file input or chooser trigger. Ref validation checks document generation and observed value; it refuses to overwrite changed drafts. Reread before choosing an explicit replacement.

`rep do n_HANDLE t3` clicks a control from `rep records controls t3`, validated against its own node and owning record before input; see [records](records.md).

The result contains a bounded delta of changed text and controls, or a full read after navigation. Native dialogs, popups and downloads that are not observed are labelled accordingly. Verify a performed-but-unobserved action; do not repeat it.

## Plan schema

`rep do @plan.json t3` or `rep do @- t3` executes an explicit JSON plan and halts on the first failed step. The plan's URL is an exact starting-page precondition. Unknown fields are refused. Limits: 256 KiB, 1–100 steps, unique IDs, at most 30 seconds per step.

```json
{
  "version": 1,
  "url": "https://example.com/form",
  "steps": [
    {"id":"fill","action":"fill","target":{"name":"Search","role":"textbox"},"value":"query"},
    {"id":"submit","action":"press","target":{"name":"Search","role":"textbox"},"keys":["Enter"],"after":[{"target":{"selector":"#result"},"text":"Saved"}]}
  ]
}
```

A target uses an exact `name` with optional `role`, or a `selector`. Optional `within:{selector,text,text_selector}` narrows a repeated region. Accessible frame targets may carry `frame_id` and `frame_url`. Semantic model selection is specialized lab functionality; use observed names/selectors for core plans.

| Action | Required input |
| --- | --- |
| fill | target and string value |
| replace | target, old and value; explicit guarded draft replacement |
| choose | target and value or values |
| check | target and checked boolean |
| click | target and after conditions |
| press | target, keys array and after conditions |
| wait | after conditions; no target |

A condition is an exact `url` or a `target` plus `text`, `value`, `checked`, `visible`, `enabled`, or `absent`. An absent condition cannot combine element states. `skip_if` uses the same schema. `timeout_ms` bounds a step. A contiguous shared `batch` label opts independent targets into one observation; each action still validates immediately before input. Prefer the small ref action when a plan is unnecessary.

## Evaluation and visual evidence

`rep eval @script.js t3` or `rep eval @- t3` returns its value under `untrusted.value`; `--timeout` allows a longer script and `--capture` observes its network. Await, return-by-value, user gesture and REPL semantics are enabled. It holds the execution lease for the whole script. Large values become private artifacts. Exceptions are page data, not trusted hints.

`rep shot t3 image.png` records an image; `--selector` frames one control, `--full-page` captures the scrollable page, and `--viewport` stays on the owned tab. `rep shot --urls @urls.txt --viewport mobile` uses a bounded pool; the optional positional path is an output directory. URL lists may use stdin and contain one URL per line.

`rep reference t3` saves a screenshot plus computed layout, fonts, assets and timing evidence. `--selector` narrows a CSS selector or observed ref. Follow its section cursor. `rep cdp METHOD JSON t3` is the journaled escape hatch for permitted methods on an owned tab; browser-global methods remain outside its scope. Use `rep cookies` for HttpOnly cookie inspection/transfer and `rep eval` for origin-local storage. Explicit `--out` on eval/cdp saves raw result JSON; see [authentication](authentication.md).
