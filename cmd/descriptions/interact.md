# rep browser interact — verified UI runtime

```sh
rep --workspace PROJECT --task TASK browser interact FILE --tab ID --raw-json
rep --workspace PROJECT --task TASK browser interact FILE --tab ID --apply --raw-json
```

Versioned local JSON: `{ "version": 1, "url": "https://example.com/form", "steps": [...] }`.
Every step needs a unique `id` and `action`. Exact targets use `selector` or
`name` with optional `role`. Repeated labels use `within: {selector, text,
text_selector}`; text_selector identifies the scope's stable label. Names/text
normalize whitespace; input values remain exact. Open shadow roots are supported;
frames, closed roots, virtualized content, and arbitrary custom widgets are not.

Actions: `fill` with `value` (different nonempty text needs `replace: true`);
`replace` with unique `old` substring and `value`; `choose` with option-label
`values`; `check` with boolean `checked`; `click`; `press` with `keys`; `wait`.
Text controls include native inputs, textarea, contenteditable, and ACE editors.
Native clicks use DOM click; keys use CDP with focus checks. Other editors fail.

`click`, `press`, and `wait` require `after` conditions. Conditions are an array
of conjunctions: `url`, or `target` plus `text`, `value`, `checked`, `visible`,
`enabled`, or `absent`. Conditions cannot use semantic goals. Clicks/changed
inputs reject already-satisfied conditions before dispatch. Explicit `skip_if`
conditions support resume without repeating accepted work. For navigation,
require the destination URL AND its loaded control/content.

`target.goal` uses the existing Jev selector, without selector/within. Optional
name/role adds local checks. Only `selected` with adequate confidence and complete
coverage may act. Values and outcome conditions stay local. Exact targets need
no model/credentials. `--no-cache` bypasses Jev's existing decision cache.

Default previews: validate every step, inspect first target without input or
scrolling, defer dependent targets. `--apply` executes and stops on first failure.
Mutations are never retried. Transport errors after dispatch are `unconfirmed`;
inspect state before resuming. Only read-only waits retry across page navigation.

Reports: `status`, `steps` with `id/status/code/evidence/attempted/changed`,
`duration_ms`, `bridge_calls`, and `semantic_selections`. Evidence distinguishes
`input_readback` from application `postcondition` and `skip_condition`. UI
read-back alone does not establish saved server state. No form values are echoed.
Partial failures return the report and nonzero exit status.

One debugger attachment per run, released on exit; already-attached tabs are
rejected without detaching their owner. No extension reload or page installation.
No network capture is started; use `browser action` for archived network evidence.

Limits: version 1, HTTP(S) URLs without credentials, plan <=256 KiB, 1–100 steps,
values <=32 KiB, 16 conditions, 32 keys, per-step timeout_ms 0–30000 (0/default
means 10000), ten-minute overall deadline. All plan validation precedes effects.
`--browser arc|chrome|any|headless` reuses the selected bridge/profile.

Full examples and real-browser regression instructions: docs/interactions.md.

Compatibility: `rep jev act --plan FILE` uses this same executor. The older
`--plan` and tuning flags remain accepted but are omitted from ordinary help.
`--raw-json` selects JSON without requiring `-j`.
