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

Reads an already-open tab through the native host and compact extension
accessibility projection. It performs no browser action or navigation.
The model chooses among observed named nodes and an explicit `none` option.
Duplicate labels retain separate identities, ancestor context, and sibling
row/entity relations. Backend node
and frame IDs are local handles, never generated selectors or executable code.

Options: `--kind controls|text|all` (default controls), `--confidence 0.8`,
`--limit 240` (maximum 480), `--origin ORIGIN`, and `--no-cache`. Advanced scope
flags are `--frame ID`, `--frame-url URL`, and `--root-node BACKEND_ID` (requires
a frame). `--strategy legacy` and `--observation legacy` support comparisons.
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

A fresh initial observation checks cached decisions; new model decisions are
rechecked after inference. Short-lived cached decisions require matching task,
credentials, goal, model, options, scope, and observation fingerprint.
Every later browser operation still needs its own current-state check and task
authorization; a lookup result does not execute or authorize an action.

Only the goal and minimized accessible names, roles, context (named ancestors,
row/entity siblings, and the nearest preceding section heading), and whitelisted
control states are shared with TypeSafe. The top frame's page title is never used
as candidate context.
Input values, their editable text descendants, cookies, headers, and raw DOM
are excluded. Text can still contain personal information; scope the task.

One question can include up to 254 candidates plus `none` if it fits the token
budget (30k estimated tokens; documented model limit 32k). Independent goals can share observation and provider requests with
`browser select-batch`. `browser observe` and `browser validate` need no model.
See `rep describe decisions` for their contracts.

## Failures

Every failure is a structured error with a stable `code`, a message naming the
cause, and next steps (JSON on stdout with `-j`, text on stderr otherwise).
Provider codes: `jev_not_configured`, `jev_unauthorized`, `jev_rate_limited`,
`jev_overloaded`, `jev_server_error`, `jev_timeout`, `jev_connection_failed`,
`jev_context_exceeded`, `jev_request_rejected`, `jev_invalid_request`, and
`jev_invalid_response` (the message names the failed check). `host_outdated`
means the browser's rep-host predates the CLI. Transient codes were already
retried three times; a failed lookup never acted on the page. Probability totals
are validated against rounding to hundredths rather than a fixed tolerance.

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
