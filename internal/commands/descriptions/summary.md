# rep summary / rep context

Both commands return the same bounded JSON projection. They read this task's
latest capture, or one explicitly requested `--saved ID` archive. They never
union saved history or insert notes into an agent's context.

Select `--workspace NAME --task NAME`, or equivalent `REP_WORKSPACE`/`REP_TASK`.
`rep scope` shows the selection. Reading legacy shared data requires `--global`.
An empty task returns `provenance.source_status: no_capture`; that describes
its dataset, not whether the browser can access the requested website.

## Budget and progressive retrieval

`--max-bytes 4096` bounds the complete JSON response plus its newline. Default
8192; accepted range 2048–65536. This is an exact byte budget, not a tokenizer
estimate. `--envelope` is included in the budget; ordinary output is bare JSON.

Requests are grouped by method, canonical origin, and normalized route. Query
values, credentials, raw bodies, headers, and notes are omitted. Numeric and
opaque path components are generalized; this is not exhaustive PII detection.
Groups retain counts, response statuses, types, and a few real request IDs.
Selection spreads the budget across origins, methods, and response statuses.

Read `provenance`, `totals`, `complete`, and `omitted` before interpreting the
groups. Counts describe the selected source even when only some groups fit.
Use a group's request ID with `rep body ID --head 4096` for deliberate detail.
Use `--domain HOST` or `--primary` to narrow the view. Primary domains are
task-local filters, not isolation or browser-authorization boundaries.

## Incremental cursors

Pass the returned cursor to `rep context --since CURSOR`. It belongs to the
same workspace/task, live or saved source, and filter set. Changed filters,
foreign cursors, missing checkpoints, and expired checkpoints produce errors;
they never silently reset into a different dataset.

The cursor acknowledges only groups and removals actually returned. When
`complete` is false, another call with that cursor can drain omitted changes.
Do not repeatedly reuse the old cursor; use the newest returned one.
Unchanged results have `no_change: true`; counts and provenance remain visible.

Local content hashes detect late response updates with an unchanged request ID.
No model call is used for grouping or change detection. Checkpoints contain
hashes, not response bodies or notes, and expire after 24 hours with a 64-entry
bound. If a cursor expires, request a new baseline without `--since`.

`--saved latest` resolves an actual archive identity; a later archive needs a
new baseline. Use full saved hash IDs when timestamp-based names are ambiguous. Pin a body
lookup to the same archive with `rep body REQUEST_ID --saved SAVED_HASH_ID`.
