# rep browser — persistent semantic decisions

Use one `--workspace PROJECT --task TASK` identity throughout. These commands
operate on an existing owned `--tab ID`, with `--browser arc|chrome|any|headless`.
They perform no browser mutation. Model selection requires configured Jev
credentials; observation and validation do not.

```sh
rep browser observe --tab ID --raw-json
rep browser select-batch goals.json --tab ID --raw-json
rep browser validate --tab ID --generation ID --fingerprint HASH --raw-json
rep browser runtime --raw-json
```

`observe` returns schema version 1, session generation, semantic fingerprint,
candidates, and coverage. Candidate handles preserve frame/session/document and
backend-node identity. Names, roles, states, and row/entity relations are bounded
and scrubbed; editable values remain local. `--limit 64` (maximum 4096) only bounds
returned candidates. `coverage.output_truncated` distinguishes output limits
from incomplete browser evidence; the fingerprint covers the whole scope.

Scope flags shared by selection and observation: `--kind controls|text|all`,
`--origin ORIGIN`, `--frame ID`, `--frame-url EXACT_URL`, `--root-node BACKEND_ID`.
A frame URL must resolve uniquely. A root node requires an explicit frame and
restricts candidate eligibility to its subtree. Essential ancestor/sibling
context is still read and fingerprinted. Missing roots fail without broadening.
`observe --since HASH` requests changes from retained scoped evidence; if its
base has expired, consume the current full observation.

`validate` requires the old generation/fingerprint and the SAME original scope.
It returns `fresh` plus a refreshed snapshot. Freshness comes from current
accessibility reads; browser event hints alone are insufficient. A different
session generation, frame document, or relevant semantic evidence invalidates
reuse. IDs are temporary handles, not selectors or permanent locators.

`select-batch` takes a <=128 KiB local JSON file:

```json
{"version":1,"goals":[
  {"id":"name","goal":"The full name input"},
  {"id":"email","goal":"The email address input"}
]}
```

Provide 1–32 independent goals, each with a unique ID. Results retain input order
as `{id, decision}`. One shared observation supplies the goals. Independent
questions share requests when they fit the estimated token budgets. Shared
candidate descriptions and short aliases reduce repeated state when smaller;
each independent question still states its own goal, and aliases map back to
observed local handles after strict probability validation. Each
result preserves its own status, confidence, distribution, coverage, binding,
and timing. Request usage is attributed once; batch usage sums actual calls.
No operation, input value, or executable code is generated.

Defaults: `--limit 240` (max 480), `--confidence 0.8`, `--strategy auto`,
`--observation auto`. `--no-cache` bypasses decision reuse. Auto uses one Choice
for up to 254 candidates plus `none` when it fits 30k estimated tokens (documented
limit 32k); larger inputs use bounded groups and a final comparison. Packed requests
stay within 60k estimated tokens (limit 64k); a provider `max_tokens_exceeded`
reply repacks the same candidates into smaller questions. `legacy`
strategy preserves sequential 40-candidate grouping for comparisons. Legacy
observation preserves full-tree transport and cannot authorize semantic actions.

The host pools HTTP connections and coalesces identical in-flight requests for
the same task and credentials. Cancelling the last waiter cancels its work.
The cache stores private decision metadata, bounded to 128 entries/five minutes,
with task/credential/model/scope isolation. New inference is revalidated;
cache hits use the initial current observation. `runtime` exposes counts only,
without credentials or goal text. `timing.requests` counts logical model
evaluations; `timing.transport` records HTTP attempts, including retries.

Statuses: selected, needs_review, no_match, stale. Incomplete coverage, truncated
context, or insufficient confidence requires review. A selected result performs
no action. `browser interact` combines selection with exclusive execution leases,
frame routing, current-state guards, and declared postconditions. Consecutive
independent semantic steps can opt into a shared `batch` name. Every mutation
still checks its own current dependencies. See `rep describe interact`.

Delta responses contain `full: false` and `delta` with `base_fingerprint`,
`upserted`, and `removed`. Apply both lists to the matching base before advancing
to the new fingerprint. CLI output limits set `delta.truncated` and
`delta.can_advance: false`; consume a complete observation or raise `--limit`
before advancing. An expired base returns `full: true`.
