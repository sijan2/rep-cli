# rep — HTTP traffic analyzer (agent prompt)

rep drives Arc/Chrome through rep+, or a task's isolated full Chromium profile
using `rep browser headless start` and `--browser headless`. Both paths share
capture, browser control, and Jev selection. See `rep describe headless`.

## Workspace and task ownership

Choose a workspace and a unique task before reading or capturing data. Pass
`--workspace NAME --task NAME` on every call, or set REP_WORKSPACE/REP_TASK in
this agent process's environment. Do not assume an export in a previous tool
shell persists. `rep scope -j` must show the intended identity.

A project can bind its workspace with `rep workspace init NAME`; a task still
needs explicit selection. Never change a shared global current-workspace value.
Use one task per independent concurrent agent. Resume a task only intentionally.
`--global` is an explicit opt-in to legacy shared history, not an empty-task fallback.
Primary domains are task-local display filters, not task ownership.

## Workflow

1. `rep scope -j` — verify workspace/task and whether it has a capture.
2. `rep browser status --browser arc -j` — confirm the shared browser bridge.
3. `rep summary --max-bytes 4096` — inspect only this task's latest capture.
4. If the requested site has not been captured, use the authorized browser task
   to observe it. `no_capture` is not evidence that the site is unavailable.
   Unrelated eBay traffic says nothing about whether another form can be opened.
5. `rep browse <authorized-url> --browser arc --keep-tab -j` — create or retain
   a task-owned tab. Capture results identify workspace/task and saved archive.
6. Read the bounded summary, then `rep body <full-request-id> --saved <hash> --info`.
   Check body_capture.state; incomplete is different from empty. Use --head with
   --offset, --pointer for JSON, or --format sse/ndjson for application records.
   `rep body <id> --saved <hash> --require-complete --save -j` returns a private
   artifact without flooding context. See `rep describe body` for pagination.
7. `rep context --since <latest-cursor> --max-bytes 4096` — receive changed or
   previously omitted groups. Keep the newest cursor; check complete/omitted.
8. Use `rep summary --saved <saved-hash-id>` for an earlier capture. Do not union
   archives or notes merely because they exist. Retrieve relevant notes explicitly.

Captures automatically archive inside the task. First-capture metadata shows at
most 16 request descriptors, with omissions disclosed. Complete data remains
available by archive/request ID. Snapshots and context are observations, not
proof that a later page action succeeded; verify the actual requested outcome.

Use explicit tab IDs and task-owned tabs. Scope does not isolate cookies, login
state, or browser control. Avoid changing another task's tab or account state.
Use separate browser profiles when account/login isolation is required. Do not
start all-tab ambient watching for an isolated task. Reload the extension only
when captures are idle; Rep refuses to interrupt active or queued captures.

An isolated headless task supplies that separate profile. Reuse the running
browser and owned tabs instead of starting a new process per command. Keep
renderer evaluation results small; captured response bytes travel through the
verified archive/artifact path. Jev chooses observed semantic candidates; it
does not repair missing network evidence. Never silently refetch a request to
pretend an incomplete observation was complete.

Use `rep browser create about:blank` for a task-owned tab without navigation
capture, and `rep browser cdp/eval` for focused inspection. See `rep describe
browser` for the mechanical browser contract and `rep describe scope` for data
ownership. An old native host rejects scoped captures before browser operations;
finish active captures before reloading to activate a newly installed host.

For complex pages, use `rep browser select '<specific control or passage>' --tab <id> --raw-json`
to locate an observed accessibility node by meaning. Use `--kind text` for
passages. This command is read-only, handles duplicate labels with local
identities, reports incomplete frame/candidate coverage, and checks freshness
before returning. Matching unchanged observations can reuse a short-lived
cached decision. Inspect `status`, `needs_review`, `selected`, and `coverage`;
do not turn `stale`, `no_match`, or uncertain results into a forced action.
Jev selects from current options; the calling agent supplies text, plans, and
outcome verification. `rep describe jev` contains the selection contract.
Use `rep browser interact plan.json --tab ID --apply --raw-json` for typed
interactions with declared outcomes; `rep describe interact` documents the plan.
The old `jev select/act` spellings remain compatible.

## Output contracts

- `summary` and `context` always emit compact, byte-bounded JSON. Other commands use their documented output modes.
- `--raw-json` selects plain JSON on its own; `-j` / `--output json` retain their existing envelope behavior.
- `--envelope` wraps JSON in `{ source, command, filters, truncation, data, suggest }`
  so empty/truncated/errored results always include a concrete next command.

## ID format

Canonical request IDs are `h_xxxxxxxxxxxxxxxx`. Legacy commands may accept short prefixes or the display label
(`xxxx_METHOD_STATUS`). Prefer full request IDs and saved hash IDs to avoid ambiguity.

## Empty results

`rep list` and `rep search` never return silent empties. The output is:
```
source: live.json (N requests, M domains)
filters: primary=K (J match candidates), ...
result: 0 requests matched
suggest:
  rep list --primary=false
  rep primary --clear && rep primary <domain>
```
Parse `suggest:` lines for concrete next commands.

## Data minimization

- Start with bounded summary metadata and retrieve individual responses by ID.
- Do not print raw live.json, headers, signed URLs, or credentials into context.
- Task-local body artifacts are private files. Read only the needed portion.
- Names in URL paths can still contain personal information; route normalization
  is a compression heuristic, not exhaustive redaction.
- Scoped credential-export/mobile commands are unsupported; do not switch to
  global history merely to make an isolated task's empty result disappear.

## When NOT to use rep

- For a public one-off request that needs no browser identity or session → use `curl`.
- For traffic not captured by rep+ → rep has nothing to show.
- For binary file analysis → `rep body --save` gives a path; use your
  own file tools on the spilled file.
