# rep jev — typed decisions and semantic browser lookup

`rep jev status -j` checks local configuration. `rep jev config --env-file PATH`
remembers an absolute dotenv path for the CLI and native host; the key stays in
that file. `JEV`, `JEV_API_KEY`, and `TYPESAFE_API_KEY` environment variables take
precedence. `JEV_MODEL` can pin a published model. `rep jev doctor -j` makes one
synthetic API check.

## Semantic lookup

```sh
rep browser select --tab TAB_ID 'Find the course materials link' --browser arc -j
rep browser select --tab TAB_ID 'Instructions about the file format' --kind text -j
```

Reads an already-open tab with `Page.getFrameTree` and
`Accessibility.getFullAXTree`. It performs no browser action or navigation.
The model chooses among observed named nodes and an explicit `none` option.
Duplicate labels retain separate identities and ancestor context. Backend node
and frame IDs are local handles, never generated selectors or executable code.

Options: `--kind controls|text|all` (default controls), `--confidence 0.8`,
`--limit 240` (maximum 480), `--origin ORIGIN`, and `--no-cache`.
An origin constraint excludes child frames outside that origin. Inaccessible
frames, unsupported controls, and content absent from the loaded accessibility
tree are not inferred to exist or not exist.

The result includes `status`, `selected`, `needs_review`, `confidence`,
`probabilities`, `coverage`, `snapshot_fingerprint`, `cache_hit`, model and usage.

- `selected`: the observed candidate passed confidence and coverage checks.
- `needs_review`: insufficient confidence or incomplete coverage; inspect the
  evidence or narrow the goal rather than repeating the same lookup unchanged.
- `no_match`: no candidate matched the observed candidate set.
- `stale`: the observation changed while evaluating; obtain a fresh observation.

Model and cached decisions are rechecked against a fresh page snapshot. Short-lived local cached
decisions require matching goal, model, options, and observation fingerprint.
Every later browser operation still needs its own current-state check and task
authorization; a lookup result does not execute or authorize an action.

Only the goal and minimized accessible names, roles, context, and whitelisted
control states are shared with TypeSafe.
Input values, their editable text descendants, cookies, headers, and raw DOM
are excluded. Text can still contain personal information; scope the task.

## Captured-traffic classification

`rep jev classify CAPTURED_ID -j` categorizes one existing capture as `api`,
`document`, `static`, `analytics`, or `other`. It shares scrubbed metadata only,
preserves probabilities and confidence, and flags confidence below 0.8 for review.
It does not replay traffic or perform vulnerability analysis.

## Verified interactions

`rep browser interact FILE --tab ID [--apply]` integrates semantic selection with
typed, locally verified browser actions. The older `rep jev act --plan FILE` form remains compatible.
Exact targets skip the model; goal targets use the existing selector and cache.
The runtime verifies input, scopes duplicate labels, and waits for explicit
outcomes. It never retries a mutation after an uncertain response.
See `rep describe interact` and [the interaction guide](../../docs/interactions.md).
