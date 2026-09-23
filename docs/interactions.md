# Verified browser interactions

`rep browser interact` is Rep's typed browser interaction primitive.
`rep jev act` remains a compatibility entrypoint for the same runtime. Both operate on an existing owned tab through
the normal browser bridge; the runtime is embedded in the CLI binary. No extra
bot, extension reload, or page-specific script installation is needed.

```sh
rep --workspace my-project --task my-task browser interact interaction.json \
  --tab TAB_ID --raw-json
rep --workspace my-project --task my-task browser interact interaction.json \
  --tab TAB_ID --apply --raw-json
rep describe interact
```

Without `--apply`, the entire plan is structurally validated and the first step's
current target is inspected. Dependent steps are reported as planned because
prior actions may create or replace their controls. Preview does not focus,
scroll, fill, click, or submit. It can make a Jev request for a semantic target.

With `--apply`, steps run in order. A failure stops the plan. Submission and other
mutations are never automatically replayed, including when an RPC response is
lost. A lost response is an **unconfirmed** result, not proof that nothing happened.

## Explicit plans

```json
{
  "version": 1,
  "url": "https://example.com/profile",
  "steps": [
    {
      "id": "display-name",
      "action": "fill",
      "target": {"name": "Display name", "role": "textbox"},
      "value": "Sijan"
    },
    {
      "id": "save",
      "action": "click",
      "target": {"name": "Save", "role": "button"},
      "skip_if": [{"target": {"selector": "#save-state"}, "text": "Saved"}],
      "after": [{"target": {"selector": "#save-state"}, "text": "Saved"}]
    }
  ]
}
```

Plans are private local files. Values and success conditions never go to Jev and
are not echoed in reports. Unknown fields, unsupported actions, duplicate step
IDs, malformed targets, and excessive budgets are rejected before actions.
Limits: 256 KiB per plan, 100 steps, 32 KiB per text value, 16 conditions per
list, 32 keys per press, and a ten-minute run deadline. `timeout_ms` sets each
postcondition wait from 1 to 30000 ms; omitted/zero defaults to 10000 ms.

Targets use an exact accessible `name`, optional `role`, or a supplied CSS
`selector`. A selector plus name/role means all constraints must match. The
runtime resolves fresh elements for every step and rejects missing or ambiguous
matches. It supports the top document and open shadow roots, with bounded DOM
traversal. It does not guess missing/virtualized controls or cross frame contexts.

Repeated labels need a scope. `text_selector` identifies the stable label inside
each possible scope, avoiding a match against changing button/feedback text:

```json
{
  "name": "Check",
  "role": "button",
  "within": {
    "selector": ".question",
    "text_selector": ".prompt",
    "text": "2) Create a Restaurant object."
  }
}
```

An optional target `goal` invokes the existing Jev selector. It cannot combine
with `selector` or `within`; optional exact name/role constraints provide an
additional local guard. Jev chooses an observed backend node. The runtime binds
that real node, rechecks page/control state, and acts only after a `selected`
result with complete coverage and adequate confidence. `needs_review`,
`no_match`, and `stale` stop the operation. Explicit exact targets require no
model request or Jev credentials. Goal selections retain the existing cache;
`--no-cache` bypasses it. No values, code, or postconditions are model-generated.

## Actions and evidence

| Action | Explicit payload | Verification |
|---|---|---|
| `fill` | `value`; optional `replace: true` | Exact read-back after input/change events |
| `replace` | Unique `old` substring and replacement `value` | Exact complete value, preserving surrounding text |
| `choose` | `values` containing exact option labels | Selected options match; single/multiple selects supported |
| `check` | `checked: true/false` | Actual checkbox/radio state; radios require true |
| `click` | `after` conditions required | Declared application outcome |
| `press` | `keys` and `after` conditions | Focus guards, CDP key down/up, declared application outcome |
| `wait` | `after` conditions required | Read-only observed state; no action target |

Text adapters cover native text/search/email/URL/tel/password/number inputs,
textareas, contenteditable, and ACE's editor API. Different existing text is
preserved unless the plan explicitly permits replacement. `replace` requires
exactly one matching source substring. Other editor implementations and custom
widgets need a supported adapter; unknown controls stop rather than pretending
the edit worked. Native clicks are DOM clicks; keyboard input uses CDP events.
The runtime does not claim trusted mouse input, occlusion detection, or universal
framework compatibility.

Keys: `Enter`, `Tab`, `Escape`, `Space`, arrow keys, `Backspace`, `Delete`,
`Home`, and `End`. A single step can express a keyboard-accessible block movement:
`["Space", "ArrowRight", "ArrowDown", "Space"]`. Focus must remain on the
observed target before each key.

Conditions form a conjunction. Each condition can require an exact `url`, or a
target with `text`, `value`, `checked`, `visible`, `enabled`, or `absent: true`.
Text comparisons normalize whitespace; value comparisons preserve exact text.
Conditions use deterministic targets, never repeated model calls. A condition
that is already true cannot confirm a new click or changed input: the runtime
rejects it before dispatch. Use explicit `skip_if` conditions for work that is
already complete. A skipped step does not advance the expected page URL.

For navigation, require both the expected URL and the next page's real content:

```json
{
  "id": "next-section",
  "action": "click",
  "target": {"name": "Next section", "role": "link"},
  "after": [
    {"url": "https://example.com/section/2"},
    {"target": {"selector": "#section-2 input"}, "visible": true, "enabled": true}
  ]
}
```

Observers wait on mutations, with a 100 ms fallback for property changes. Full
navigation may destroy an observer; only the read-only observation is retried in
the new document. There is no fixed network-idle delay per action and no new
CLI process per field. The runtime acquires one debugger attachment and releases
it on success and failure. It rejects a tab that is already attached, preserving
its existing owner. Do not run simultaneous interactions against one tab.

Reports separate `input_readback`, `postcondition`, and `skip_condition` evidence.
Input read-back proves entry, not application acceptance or durable server
storage. Add an appropriate application condition, and verify from a fresh page
when persistence matters. These fast interactions do not start a network capture;
use `browser action` when an archived network trace is required.

## Lessons encoded in the runtime

- Selecting a control is separate from entering data and observing acceptance.
- Check/Submit labels are scoped to their actual question or form.
- Readiness includes loaded controls, not just a changed URL.
- Earlier success text cannot prove that a new submission succeeded.
- A failed response must not cause a duplicate submission.
- Resume uses declared completion evidence, never empty/nonempty fields alone.
- Browser ownership, stale nodes, disabled fields, and existing user text remain
  explicit checks even when the model is confident.

## Validation

```sh
go test ./...
go build -o /tmp/rep-jev-interaction .
node scripts/verify_interaction.mjs
```

The live regression script creates and closes its own localhost fixture tab.
Use `REP_TEST_BROWSER=headless` and `REP_TEST_TASK=NAME` to target an already
started task-owned headless profile. Arc remains the default. It
checks the adapters, repeated labels, actual input/change events, preview,
preservation, disabled/read-only controls, delayed feedback, stale success,
resume, CDP keyboard input, canonical and compatibility command forms, real Jev selection, SPA content loading, and full
navigation. It needs a running Arc bridge and configured Jev credentials.
`REP_TEST_BINARY` overrides the tested binary; `REP_TEST_REPORT` overrides the
private report path (default `/tmp/rep-interaction-verification.json`). Timings
and bridge call counts are measured, not assumed. The ACE fixture uses its public
API shape; it does not certify every ACE version or other editors.
