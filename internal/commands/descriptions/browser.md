# `rep browser` contract

For repeated site searches, see `rep describe sites`: the Rep workspace's
sitelearn companion can reuse verified document/API packs in one owned tab.
Prefer an observed API or fetched document to repeated navigation and sleeps.

## Purpose

Control and observe Arc/Chrome through rep+, Native Messaging, and
`chrome.debugger`. `rep browser headless start` launches a separate task-owned
Chromium profile using this same bridge; see `rep describe headless`.

`rep browser native-capture URL` launches a separate, extension-free private
Chromium session for explicitly enabled native TLS/QUIC key logging and original
RTP/RTCP diagnostics. It owns and stops that browser. See `rep describe
native-capture` for its filters, artifact boundaries and resource limits.

## Core workflow

```text
rep browser status --raw-json
rep browser create <https-url> --raw-json
rep browser element select "Save button" --tab ID --raw-json
rep browser interact flow.json --tab ID --apply --raw-json
rep browser capture open <https-url> --keep-tab --raw-json
rep browser capture fetch <same-origin-url> --raw-json
rep browser capture action <javascript-or-@file> --tab ID --raw-json
rep body <captured-request-id>
```

Pass a workspace/task or set REP_WORKSPACE/REP_TASK in the calling process.
Dia is the default; use --browser only to choose another profile/transport.
`--raw-json` selects plain JSON by itself. `browser element select` is read-only;
`browser interact` executes explicit typed plans (see `rep describe interact`).
`browser element observe` reads compact relational evidence without model calls.
`browser element select-batch` resolves independent goals together; `browser element validate`
refreshes scoped evidence. See `rep describe decisions` for scope, reuse, and
frame-aware binding contracts. See `rep describe element` for inherited scope
flags and the hidden compatibility paths.
Task captures always archive, so `--save` is unnecessary in scoped workflows.
The old `rep browse` and `rep browser open|fetch|action` paths remain hidden
compatibility commands, with the same flags, defaults, payloads, and scope guards.
Execution prints a deprecation note only on stderr; JSON stdout is unchanged.
Use `rep browser capture open|fetch|action` for new integrations. There are no
capture hard breaks. `rep jev select/act`, `--goal`, and `--plan` remain
compatible. Normal help emphasizes common options; advanced flags below remain
accepted with their existing behavior, only by the commands listed. An unknown
flag fails with `invalid_argument` and names the commands that accept it.

| Advanced flags | Commands / purpose |
|---|---|
| `--await`, `--by-value`, `--user-gesture`, `--repl` | eval/action renderer semantics; defaults true, true, false, false |
| `--target` | Alternative debugger target ID instead of --tab |
| `--keep-attached` | eval/CDP persistent attachment; release with detach |
| `--idle`, `--settle` | `browser capture action` only: capture settling, defaults 300 ms / 1500 ms |
| `--max-result` | action evaluation result cap; default 64 KiB |
| `--protocol-payloads` | capture family: instrument new WebTransport and WebRTC data-channel APIs |
| `--webrtc-media` | capture family: record clones of observed WebRTC audio/video tracks with native browser codecs |
| `--referrer`, `--idle` | `browser capture open`: navigation referrer and network-idle interval |
| `--credentials`, `--cache` | fetch credentials/cache behavior |
| `--save` | Legacy global capture archival; automatic for task captures |

## Capture family

`rep browser capture` groups the three operations that produce an isolated
network capture. `open` navigates a page, `fetch` issues a credentialed request
inside an origin, and `action` evaluates page JavaScript. These remain distinct
operations because their target requirements and completion criteria differ.
`browser create` only creates a tab; `browser eval` only evaluates JavaScript.

The following persistent capture options work before or after the operation:

| Flag | Default | Meaning |
|---|---|---|
| `--timeout` | `30s` | Maximum operation duration; the bridge handoff retains its existing 10-second grace |
| `--max-body` | `8388608` | Maximum captured decoded response-body bytes per request |
| `--protocol-payloads` | `false` | Enable page API payload collection |
| `--webrtc-media` | `false` | Enable recording of observed media-track clones |

For example, `rep browser capture --timeout 10s open URL` and
`rep browser capture open URL --timeout 10s` are equivalent. `--browser`
remains inherited from `browser`; `--workspace`, `--task`, `--global`, `--run`,
and output options retain their root contracts. Captures still require explicit
scope, and `--run` still records executed operation evidence.

Operation-specific flags stay local: `open` has `--tab`, `--referrer`,
`--active`, `--keep-tab`, `--idle`, `--save`, and `--note`; `fetch` has `--tab`,
`--method`/`-X`, `--header`/`-H`, `--data`/`-d`, `--credentials`, `--cache`,
`--headers-only`, `--keep-tab`, `--save`, and `--note`; `action` has `--tab`,
`--target`, `--await`, `--by-value`, `--user-gesture`, `--repl`, `--idle`,
`--settle`, and `--max-result`. `--idle` remains 800 ms for navigation and
300 ms for actions; those differing defaults are intentionally not promoted.

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

Use `browser capture action` when page JavaScript triggers the request of interest. It
combines renderer evaluation and an isolated body-capturing network session,
avoiding a separate ambient watch and follow-up capture. Reuse one owned tab
across route changes with `rep browser capture open <url> --tab <id> --referrer <url>`.
Action evaluation output is capped separately with `--max-result`; return only
small metadata and let the capture/download commands handle response bytes.
`browser capture action` observes for at least `--settle` (default 1500 ms) after the
evaluation resolves, then requires the normal `--idle` interval. This captures
browser-managed callbacks such as Turnstile, `postMessage`, timers, and
framework effects without making the page script sleep artificially.

### WebTransport and WebRTC

```sh
rep --workspace PROJECT --task AGENT browser capture open https://localhost:8443 --protocol-payloads --keep-tab --raw-json
rep --workspace PROJECT --task AGENT browser capture action @fixture.js --tab ID --protocol-payloads --raw-json
rep --workspace PROJECT --task AGENT summary --saved HASH --max-bytes 4096
rep --workspace PROJECT --task AGENT stream RECORD_ID --saved HASH --info
rep --workspace PROJECT --task AGENT stream RECORD_ID --saved HASH --event SEQUENCE --head 0 --save
```

WebTransport CDP lifecycle records are captured without an extra flag. Payload
capture is opt-in because it wraps JavaScript APIs in the selected page, its
frames, and attached dedicated workers. `open` installs before navigation;
`action` installs before evaluation. Connections created before installation,
cached native methods, and uninstrumented shared/service workers remain gaps.
An older extension is rejected before navigation or evaluation when the flag is
requested. See [transport capture](../../docs/transport-capture.md).

The observer records WebRTC data-channel messages and WebTransport datagrams
and readable/writable chunks already used by the application. It keeps native
stream identities and promises and never reads ahead. `pipeTo`, `pipeThrough`,
teeing and async iteration have explicit bypass limitations. Capture end restores
hooks and removes the observer; it does not close application transports.
Inspect capture warnings and `stream.capture` before using the evidence.

`stream.source: page_api` is page-controlled evidence. Complete means complete
within `instrumented_api_calls`; it does not mean complete wire traffic. Native
WebTransport lifecycle records remain independent from API records. Raw
interface packets use `rep packets capture`. Audio/video can be recorded with
the separate `--webrtc-media` flag, using native MediaRecorder on existing track
clones. Its `webrtc_media` records declare `browser_media_recorder` source,
`reencoded_media` semantics and `recorded_media_interval` scope. Media capture
does not request devices and leaves original tracks running. Use `rep media ID
--saved HASH --require-complete --save recording.webm` to assemble a recording.
See [native capture](../../docs/native-capture.md) for bounds and provenance.

Every successful `browser capture open|fetch|action` result (including the
legacy spellings) includes `captured_requests`. Each descriptor has only the
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

The `browser capture open` and `browser capture fetch` URL operand accepts
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
- `origin_mismatch`: omit `--tab` so `browser capture fetch` creates a matching-origin
  temporary tab.
- stale extension code: run `rep browser reload-extension -j`.

## Advanced flags

These flags stay accepted with their existing behavior and are hidden from `--help` to keep the everyday surface small: `browser shots`: `--delay` (extra wait before each image), `--quality` (jpeg/webp quality), `--report` (write an HTML contact sheet). `browser screenshot`: `--quality`.
