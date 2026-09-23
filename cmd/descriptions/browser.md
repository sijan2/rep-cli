# `rep browser` contract

## Purpose

Control and observe Arc/Chrome through rep+, Native Messaging, and
`chrome.debugger`. `rep browser headless start` launches a separate task-owned
Chromium profile using this same bridge; see `rep describe headless`.

## Core workflow

```text
rep browser status --raw-json
rep browser create <https-url> --raw-json
rep browser select "Save button" --tab ID --raw-json
rep browser interact flow.json --tab ID --apply --raw-json
rep browser open <https-url> --keep-tab --raw-json
rep browser fetch <same-origin-url> --raw-json
rep browser action <javascript-or-@file> --tab ID --raw-json
rep body <captured-request-id>
```

Pass a workspace/task or set REP_WORKSPACE/REP_TASK in the calling process.
Arc is the default; use --browser only to choose another profile/transport.
`--raw-json` selects plain JSON by itself. `browser select` is read-only;
`browser interact` executes explicit typed plans (see `rep describe interact`).
Task captures always archive, so `--save` is unnecessary in scoped workflows.
The old `rep browse`, `rep jev select/act`, `--goal`, and `--plan` forms remain
compatible. Normal help emphasizes common options; advanced flags below remain
accepted with their existing behavior.

| Advanced flags | Commands / purpose |
|---|---|
| `--await`, `--by-value`, `--user-gesture`, `--repl` | eval/action renderer semantics; defaults true, true, false, false |
| `--target` | Alternative debugger target ID instead of --tab |
| `--keep-attached` | eval/CDP persistent attachment; release with detach |
| `--idle`, `--settle` | Capture settling; action defaults 300 ms / 1500 ms |
| `--max-result` | action evaluation result cap; default 64 KiB |
| `--referrer`, `--idle` | open navigation referrer and network-idle interval |
| `--credentials`, `--cache` | fetch credentials/cache behavior |
| `--save` | Legacy global capture archival; automatic for task captures |

## Low-level workflow

```text
rep browser targets --browser arc -j
rep browser attach --browser arc --tab <id> -j
rep browser cdp <Domain.command> --browser arc --tab <id> --params <json-or-@file> -j
rep browser eval <javascript-or-@file> --browser arc --tab <id> -j
rep browser detach --browser arc --tab <id> -j
```

Persistent attachment is optional. Without it, each low-level command attaches
and detaches automatically. Capture sessions reuse a persistent attachment;
page target IDs canonicalize to tab IDs for consistent reuse.

Use `browser action` when page JavaScript triggers the request of interest. It
combines renderer evaluation and an isolated body-capturing network session,
avoiding a separate ambient watch and follow-up capture. Reuse one owned tab
across route changes with `rep browse <url> --tab <id> --referrer <url>`.
Action evaluation output is capped separately with `--max-result`; return only
small metadata and let the capture/download commands handle response bytes.
`browser action` observes for at least `--settle` (default 1500 ms) after the
evaluation resolves, then requires the normal `--idle` interval. This captures
browser-managed callbacks such as Turnstile, `postMessage`, timers, and
framework effects without making the page script sleep artificially.

Every successful `browse`, `browser open`, `browser fetch`, and `browser
action` result includes `captured_requests`. Each descriptor has only the
stable capture `id`, HTTP `method`, response `status`, captured response
`body_bytes`, explicit chronological `sequence`, and optional
`body_truncated`/`intentional_cancellation` markers. It intentionally omits
URLs, queries, headers, and body content, so a caller can select an ID directly
without dumping `rep list` or exposing signed links and credentials.

Descriptors also include `body_state` and `network_state`; `body_capture_states`
and `incomplete_bodies` summarize capture quality. Body capture uses passive CDP
streaming with a buffered fallback. The default decoded-body budget is 8 MiB per
request, configurable by `--max-body` through 256 MiB; larger or unfinished
responses retain a prefix with explicit partial evidence. Compressed wire sizes
are not used to decide that decoded bodies are empty. Use `rep body ID --info`
and `--saved SAVED_HASH_ID` to diagnose and retrieve the exact response.

Large request records cross Native Messaging as sequenced, hashed fragments.
New captures require the updated host protocol. Transport loss, missing records,
or snapshot persistence failures fail explicitly instead of reporting success.
Fetch RPC output is only a bounded preview; `full_body_in_capture` and the request
ID point to the captured bytes. Keep action evaluation metadata small too.

Completed framework-redirect and HTTP-redirect graphs are summarized in a safe
`terminal_outcome` containing only request IDs, hop count, terminal status, and
separate counts/IDs for later form failures. A later duplicate challenge error
therefore does not conceal an already completed terminal download.

`browser create` is the target-creation primitive for workflows that need a
real-profile tab without navigation capture. It works through the rep+ bridge
and therefore does not require Arc's browser-process remote-debugging port.
`browser download` uses that primitive internally to stream a captured final
GET with browser-managed credentials through `Network.loadNetworkResource` and
bounded `IO.read` calls. The temporary tab is detached and closed before the
validated part file is published.

`browser reload-extension` schedules `chrome.runtime.reload()` only after the
RPC response has been posted, then waits for a replacement native bridge. It is
the bridge-native update path for an unpacked extension and does not require
ArcCore CDP or the extension-manager UI.

The `browse`, `browser open`, and `browser fetch` URL operand accepts
`@/absolute/path`. Use a mode-0600 file for a signed URL so its query does not
appear in process arguments. `@file` URL operands automatically redact
request/final URLs, response headers, response bodies, and URL-bearing errors
from command output; the complete request remains in `live.json`. Direct URL
operands keep the existing output contract.
For a large signed GET whose request only needs to be captured, add
`--headers-only`; rep cancels the renderer response body after headers and
returns the stable request handle for `rep browser download`. The resulting
intentional abort is reported via `ignored_cancellations`, not
`failed_requests`.

## Data

Completed network captures replace `live.json` atomically. Request provenance
is `capture_source=cdp` with a stable tab ID. HttpOnly cookies stay managed by
the browser; captured headers are available to explicit full-data commands but
agent-facing meta output fingerprints sensitive values.

Ambient watch records completed requests, redirect hops, and failures across
ordinary Arc tabs. It performs no request parsing while disabled, and an active
ambient session survives extension/native-host restart or host reconnect. Watch
start/stop operations are idempotent. Explicit navigation, fetch, and action
sessions use CDP and remain isolated from ambient traffic. Browser fetch defaults to the
renderer's normal cache mode; use `--cache` only when needed.

## Failure recovery

- `browser_unavailable`: run `rep browser doctor --browser arc -j`.
- `debugger_attach_failed`: close visible DevTools for the target or choose
  another tab.
- `tab_busy`: wait for the active capture or use another tab.
- `origin_mismatch`: omit `--tab` so `browser fetch` creates a matching-origin
  temporary tab.
- stale extension code: run `rep browser reload-extension -j`.
