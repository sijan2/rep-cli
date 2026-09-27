# Persistent browser decisions

Rep keeps the decision loop in the browser's native host. Short CLI commands
share the host's bounded HTTP client pool and browser session. The extension
projects Chromium's accessibility data into named candidates before sending it
across native messaging. The host receives roles, names, relations, coverage,
and opaque handles instead of the entire accessibility tree.

This reduces repeated network setup, model calls, and bridge payloads. It does
not remove provider latency or make a model selection proof of task completion.
The interaction executor verifies the target and the declared outcome locally.

## Observe and resolve

Use the same workspace and task on every call:

```sh
rep --workspace PROJECT --task TASK browser observe --tab ID --browser headless --raw-json
rep --workspace PROJECT --task TASK browser select 'Buy the Widget Pro' --tab ID --browser headless --raw-json
rep --workspace PROJECT --task TASK browser select-batch goals.json --tab ID --browser headless --raw-json
rep --workspace PROJECT --task TASK browser runtime --browser headless --raw-json
rep describe decisions
```

`goals.json` contains independent targets, with caller-supplied identifiers:

```json
{"version":1,"goals":[
  {"id":"name","goal":"The full name input"},
  {"id":"email","goal":"The email address input"},
  {"id":"city","goal":"The city input"}
]}
```

One batch accepts up to 32 goals and shares a coherent observation. Independent
questions share provider requests when the token budgets permit. Each result
retains its confidence, probability distribution, coverage, and review status.
Usage is attributed once, rather than repeated for every question.

When several questions share candidates, the host can put their descriptions
once in shared state and offer short local aliases in each question. It uses
that representation only when the exact serialized request is smaller and all
per-question budgets still fit. Each question states its own goal; questions
never depend on each other's answers. Probability distributions are validated
against the offered aliases and mapped back to observed node IDs locally.
Single-question selection retains its direct candidate descriptions.

`auto` packing uses one Choice for up to 254 candidates plus `none` when the
state/question fits the 30k estimated-token budget (documented model limit: 32k).
Larger questions use bounded groups followed by a comparison of shortlisted
candidates. Independent groups are packed into requests up to 60k estimated
tokens (documented limit: 64k). A provider `max_tokens_exceeded` response makes
the same candidates repack into smaller questions. Group uncertainty remains
visible; group probabilities are never treated as comparable global rankings. `--strategy
legacy` retains sequential forty-candidate grouping for controlled comparisons.
`--observation legacy` retains the old full-tree bridge path for read-only
comparisons; verified semantic execution requires a compact session binding.

## Scope and evidence

`--frame ID` restricts candidates to one frame. `--frame-url URL` requires a
unique exact frame URL; ambiguity fails. `--origin ORIGIN` additionally requires
that root origin and excludes other origins. Frames outside an explicit scope
are not evidence about the scoped goal.

`--root-node BACKEND_ID` with a frame restricts eligible candidates to an
explicit subtree. The browser reads its accessibility ancestors and descendants;
when a row or entity supplies essential sibling context, that context is read
too. Context nodes outside the subtree cannot become selectable candidates.
Small scoped reads can save work; large scopes can require more CDP calls than
one full-tree read. Missing roots and exhausted traversal budgets are explicit.

Candidates retain the nearest useful row/entity relations. Identically named
Buy buttons therefore keep their product context. Fingerprints include those
relations: changing a product label invalidates its binding even if the button
itself has not changed. Editable values and editable descendants are excluded.

An observation has a session generation and semantic fingerprint. To inspect
changes, pass a prior fingerprint to `observe --since HASH`; an unavailable base
requires consuming the full observation. To check a binding, use `browser
validate --tab ID --generation GENERATION --fingerprint HASH` with the same kind,
origin, frame, and subtree scope. Validation refreshes current evidence. Browser
events invalidate indexes early but are not assumed to report every semantic
change. `--limit` bounds returned candidates without changing the fingerprint;
coverage reports output truncation separately.

The browser retains bounded frame/session indexes and observation history.
Whole-page goals still refresh their whole observation scope. There is no
unverified event-only freshness shortcut and no claim that rereading one chosen
node proves a broad goal still selects the right target.

## Reuse, execution, and lifecycle

The host coalesces identical in-flight requests within the same task, model,
credentials, and observation scope. It bounds active decisions to four, queued
flights to 64, and pooled clients to sixteen. Cancelling the last waiter cancels
its decision; one caller cannot cancel another caller's shared work.

The private decision cache is globally bounded to 128 entries and five minutes.
Task, credential, model, options, scope, and fresh evidence all partition reuse.
Cache hits need no new provider request. An initial fresh observation validates
cached evidence; decisions made during inference are checked again afterward.
Provider connection reuse and evaluation counts are measured in result timings.
`timing.requests` and interaction `decision_requests` count logical model
evaluations; `timing.transport` records individual HTTP attempts, including
retries. A logical evaluation can therefore have more than one HTTP attempt.

`browser interact` holds an exclusive execution lease; read-only observers share
sessions without detaching an active owner. Handles carry frame, CDP session,
loader generation, backend node ID, and original observation scope. Same-process
frames and out-of-process frames route through their appropriate contexts and
sessions. Detach, native-host reconnect, or navigation invalidates old bindings.

Set the same explicit `batch` string on consecutive independent semantic steps
to resolve their goals together. Each step is still validated before mutation;
a preceding edit that changes semantic dependencies forces reselection. Exact
targets require no model. Reused semantic bindings avoid new inference only
while their evidence remains current. See [interactions](interactions.md).

Actions guard frame/document identity, target geometry and occlusion, including
ancestor iframe visibility. Postcondition observers are armed before dispatch.
There is no automatic retry after an uncertain mutation. Arbitrarily transformed
iframes, unsupported custom controls, inaccessible frames, virtualized content,
and canvas-only semantics remain limitations, reported rather than inferred.

## Validation and measurement

```sh
go test ./...
cd ../rep && npm test
```

`scripts/verify_decision_runtime.py --help` describes the isolated Chromium
harness. It starts local fixtures and a private task/profile, then stops its
browser. Deterministic checks cover frame routing, shadow controls, occlusion,
dynamic replacement, and independently read-back outcomes. `--with-jev` adds
paid, pinned-model decisions and labeled auto/legacy comparisons. Its report
records failures and all attempted timings, not just successful selections.
Frozen-observation comparisons isolate selection from browser execution; neither
small fixture suite establishes broad accuracy on arbitrary real sites.

Delta responses contain `full: false` and `delta` with `base_fingerprint`,
`upserted`, and `removed`. Apply both lists to the matching base before advancing
to the new fingerprint. CLI output limits set `delta.truncated` and
`delta.can_advance: false`; consume a complete observation or raise `--limit`
before advancing. An expired base returns `full: true`.
