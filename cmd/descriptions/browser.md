# `rep browser` contract

## Purpose

Control and observe a real Arc/Chrome profile through rep+, Native Messaging,
and `chrome.debugger`. No separate headless browser or cookie export is used.

## Core workflow

```text
rep browser status --browser arc -j
rep browser reload-extension --browser arc
rep browser create about:blank --browser arc -j
rep browse <https-url> --browser arc --keep-tab -j
rep browser probe --browser arc --tab <id> -j
rep browser fetch <same-origin-url> --browser arc -j
rep browser action <javascript-or-@file> --browser arc --tab <id> -j \
  | jq '{terminal_outcome,captured_requests}'
rep browser watch start --browser arc -j
rep browser watch stop --browser arc -j
rep body <captured-request-id>
rep browser download <captured-get-id> <output-path> --expect-magic zip -j
```

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
- stale extension code: run `rep arc reload-extension -j`.
