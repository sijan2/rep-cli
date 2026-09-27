# Reliable bodies, bounded parsing, and persistent browser control

The body-capture foundation was locally verified 2026-09-20. The September 26
source update adds WebSocket records, HTTP metadata, collection budgets, native
record spooling and indexed saved lookup. This extends the
[task ownership architecture](agent-context.md) and uses the same capture path
for Arc and [task-owned headless Chromium](headless.md).

## Failure modes that were fixed

Several independent problems could make the correct response disappear:

| Previous behavior | Result | Current behavior |
| --- | --- | --- |
| Compressed bodies skipped | Small gzip transfer hid a large decoded response | Collect decoded CDP bytes; do not compare compressed length to decoded length |
| Unknown/zero encoded size treated as no body | Cached or chunked responses disappeared | Stream available bytes; use browser response-buffer fallback |
| 768 KiB extension ceiling | Larger responses omitted | Default 8 MiB, explicit configurable body budget and partial evidence |
| Capture ended before new/slow body promises settled | Late bodies lost | Freeze the observation boundary, drain body work, seal once, reject late mutation |
| Child target request IDs shared one namespace | Frame/worker requests could collide | Attach related targets and namespace their request IDs |
| One oversized native message | Large record could not cross the bridge | Ordered 192 KiB fragments with total length and SHA-256 verification |
| Queue disconnects and request trimming silently dropped records | Successful-looking incomplete capture | Bind capture to one native connection; verify expected request count; fail explicitly |
| Empty body printed without evidence | Agent confused missing with genuinely empty | Complete/partial/unavailable/pending/not-applicable/legacy-unknown states |
| `body --head` bypassed JSON and could dereference a missing response | Broken agent parsing or request-body crash | One bounded JSON contract, correct byte ranges, separate request/response evidence |
| Canvas returned entire body in evaluation result | Large body exceeded native host's return-message limit | Small evaluation metadata, archive-pinned body artifact, exact byte/hash checks |

## One evidence path

```mermaid
flowchart LR
    B[Owned Arc tab or isolated Chromium profile] --> C[CDP HTTP and WebSocket collection]
    C --> E[Body state and byte-preserving representation]
    E --> T[Sequenced native transport fragments]
    T --> H[Private host spool: count and hash verification]
    H --> S[Sealed session snapshot]
    S --> A[Exact task archive and atomic task live view]
    A --> M[Small summary with body-state counts]
    A --> P[Body metadata / JSON pointer / record page / byte range]
    P --> F[Private raw artifact when needed]
    B --> J[Jev: semantic selection from observed DOM candidates]
```

Jev remains the semantic decision layer. Transport integrity, ownership, body
assembly, parsing, and pagination are deterministic and need no model calls.
Jev cannot establish that missing response bytes were captured. Its existing
freshness checks and cache also apply through `--browser headless`.

The response collector preserves buffered bytes before streamed bytes even when
the stream setup callback arrives after data events. It retains binary data,
UTF-8 BOMs, and split codepoint prefixes exactly. Non-UTF8 or incomplete text is
stored as base64 with its encoding declared. `body` decodes that representation
before byte counts, digest checks, ranges, or raw file export.

Body state describes evidence, independently of HTTP status. A 200 response can
still have a partial body. A capture's network-idle condition does not prove an
open SSE stream ended. Body limits, network failures, capture deadlines, missing
browser buffers, and detached child targets have explicit reasons. Coverage
warnings disclose unavailable related-target support. Requests that occurred
before attachment cannot be reconstructed by this implementation.

The native host acknowledges ordered capture messages and writes completed
records to a private disk spool. Its live capture state retains a compact index
and metadata; sealing copies the serialized records into the snapshot. Oversized
records use fragments with sequence, chunk-count, length and SHA-256 checks.
Transport loss or a persistence failure cannot become a verified snapshot. The
snapshot's expected record total checks transport delivery of the records the
collector retained; collector omissions remain separately disclosed in capture
statistics. Snapshot/archive I/O serialize or decode one request at a time;
staged archive appends use locks, fsync and rollback on write errors. Malformed
archive tails report an error instead of silently resetting history.

Capture actions check acknowledged incremental-host support before acting on
the page. Older hosts fail this preflight. The host still allocates space for
the largest individual record, and startup can rehydrate a previous live file;
spooling does not establish a strict process-memory ceiling.

## Browser protocol visibility

WebSocket captures preserve `ws://` / `wss://` connection identity, creation,
handshake metadata, sent/received events, direction, opcode, timestamps, errors
and close/interruption state. Text payloads retain UTF-8 bytes; binary payloads
retain a declared encoding. Browser-exposed messages do not reconstruct original
network fragmentation, compression or masking bytes. HTTP `body` views do not
stand in for WebSocket payloads; use `rep stream` and inspect the stream's own
capture state and event counters.

A connection first observed after creation has a partial start. An open socket
when capture ends, a detached target, a payload limit or an event limit also
leaves explicit partial evidence. A closed connection is not sufficient to
establish complete capture when events or bytes were omitted. Sequence gaps,
dropped counts and reason fields remain visible.

HTTP records preserve frame/loader/CDP-session identity and available event
timestamps. Responses retain browser-reported protocol, remote IP/port,
connection ID/reuse, timing, TLS/security details, cache/service-worker flags and
encoded length. Availability depends on what the browser supplies. HTTP/2 and
HTTP/3 content remains at the browser HTTP layer; Rep does not record transport
frames or QUIC packets through this collector.

WebTransport records distinguish CDP lifecycle observation from optional page
API observation. Lifecycle-only records declare unavailable payload capture and
partial coverage. Page API records preserve instrumented WebTransport stream
chunks/datagrams and WebRTC data-channel messages. WebTransport event
`channel_id` values identify logical streams or datagram channels; a WebRTC
record represents one data channel and can name its parent peer in metadata.
The reader preserves collector source, declared scope, payload semantics,
channel metadata and explicit observation gaps.

Page API records declare `scope: "instrumented_api_calls"` and
`observer_trust: "page_controlled"`. Their timestamps use performance-clock
seconds within a realm, with the time origin recorded when supplied. They do
not establish observations outside that scope, and clocks from different realms
are not directly comparable. CDP lifecycle records and page API payload records
remain independent; matching URLs do not prove a common connection. Neither
collector supplies interface packets or media recordings. Separate
`rep packets capture` and `--webrtc-media` collectors provide those artifacts;
see [native capture](native-capture.md). Packet bytes retain wire encryption,
and media recordings contain re-encoded track content.

`browser native-capture` can separately launch a new private browser for native
TLS/QUIC key logs and original RTP/RTCP datagrams. Its artifacts are a diagnostic
bundle with explicit provenance, not HTTP bodies or re-encoded track records.

Existing native diagnostic recordings can be imported through
[`evidence import`](evidence.md#import-native-diagnostic-files), retaining their
original bytes and declared provenance. Import does not start a platform tracer
or imply that its recording is complete.

## Read only the useful part

Use a stable task identity and the capture's full archive hash:

```sh
rep --workspace research --task site-a body REQUEST_ID --saved SAVED_HASH --info
rep --workspace research --task site-a body REQUEST_ID --saved SAVED_HASH --pointer /data/items/0
rep --workspace research --task site-a body REQUEST_ID --saved SAVED_HASH --format ndjson --records 20
rep --workspace research --task site-a body REQUEST_ID --saved SAVED_HASH --format sse --record-offset 20 --records 20
rep --workspace research --task site-a body REQUEST_ID --saved SAVED_HASH --find 'desired text'
rep --workspace research --task site-a body REQUEST_ID --saved SAVED_HASH --offset 8192 --head 4096
rep --workspace research --task site-a body REQUEST_ID --saved SAVED_HASH --require-complete --save -j
rep --workspace research --task site-a stream CONNECTION_ID --saved SAVED_HASH --info
rep --workspace research --task site-a stream CONNECTION_ID --saved SAVED_HASH --events 20 --max-bytes 4096
rep --workspace research --task site-a stream CONNECTION_ID --saved SAVED_HASH --event SEQUENCE --head 0 --save
```

HTTP transfer chunks, CDP chunks, and native messaging fragments are different
from application records. SSE parsing dispatches only blank-line-terminated
events, joins data lines, and preserves event IDs. NDJSON parses complete JSON
records, preserves large integers, and discloses unfinished trailing bytes.
JSON pointers skip unrelated subtrees and reject invalid/incomplete documents.
Other application wire formats remain available through raw ranges and literal
search; the tool does not claim to decode every framework-specific protocol.

Inline body output defaults to an exact 8,192-byte JSON budget. Budgeting first
bounds the candidate prefix, avoiding repeated serialization of an entire large
body. `view_complete` concerns only the selected projection; `body_capture.state`
concerns captured evidence. A partially emitted record page does not advance its
record cursor. Continue the same page with byte offsets or read its private
artifact before moving to later records. `rep describe body` is the full contract.

Browser stream event pages return descriptors without payloads. Use `next_after` as
the next `--after` cursor; it acknowledges only returned descriptors. Retrieve
one event with `--event SEQUENCE`, then follow byte offsets or save the entire
captured message with `--head 0 --save`. Inline stream JSON defaults to 8 KiB.
Capture completeness applies to the declared observer scope and is separate
from page/range completeness. Static limitations can remain on a complete scoped
capture; explicit gaps and truncation cannot pass `--require-complete`.
`--require-complete` checks the connection's evidence state and validates every
retained payload's encoding and decoded length. Ordinary metadata reads report
`payload_validation: "not_checked"`; selecting an event validates its bytes. See
`rep describe stream` for the full contract.

Canvas's read adapter now drains the authorized response while returning only
small renderer metadata. It resolves candidate request IDs by exact URL, method,
and status in the verified archive, allowing OPTIONS/unrelated reads while
rejecting ambiguous matches. It requires complete body evidence, validates the
private raw file's identity, size, hash, and UTF-8, and removes only that consumed
artifact. The complete response remains in its archive. Existing read-only and
manual-redirect rules remain part of the Canvas adapter's contract.

## Budgets and storage costs

The system cannot make a finite browser, disk, or Native Messaging channel
unlimited. Budgets are now visible and failures are explicit.

| Resource | Default | Configuration |
| --- | ---: | --- |
| Decoded body per captured request | 8 MiB | `--max-body`, 0 through 256 MiB; invalid values fail |
| Aggregate archived request/response and WebSocket payload bytes | 64 MiB | Collector limit negotiated by CLI; lower host snapshot limits reduce it |
| Request/connection records per capture | 10,000 | Collector cap; lower `REP_CAPTURE_MAX_REQUESTS` also applies |
| WebSocket events per capture | 10,000 | Collector event budget; omitted events are counted |
| Native-send backlog | 16 MiB | Collector queue budget; overflow fails explicitly |
| Serialized request accepted by host | 384 MiB | `REP_CAPTURE_MAX_REQUEST_BYTES` in host environment |
| One sealed snapshot | 512 MiB | `REP_CAPTURE_MAX_SNAPSHOT_BYTES` |
| Aggregate temporary snapshots | 1 GiB | `REP_CAPTURE_TOTAL_SNAPSHOT_BYTES` |
| Records accepted per host capture | 10,000 | `REP_CAPTURE_MAX_REQUESTS`; exceeding it fails publication |
| Temporary snapshot retention | 32 entries / one hour | Retained host policy |
| Inline body output | 8 KiB | `--max-bytes` |

Environment changes apply when the native host starts; merely setting them on an
already connected CLI does not reconfigure its running host. The CLI negotiates
the actual host snapshot limit. The serialized size can exceed decoded size due
to base64 or JSON escaping. Raising one budget does not raise the others.

The aggregate payload budget is a retained/archived byte limit, not a strict
process RSS cap. Browser/CDP delivery, UTF-8/base64 conversion and JSON encoding
can create transient copies. The native spool also counts obsolete record
revisions against its disk budget, so repeated updates cannot grow it without
limit. Native transport, collector storage and snapshot limits remain separate.

Durable network archives retain inline JSONL bodies. `body --saved` now resolves
an archive through an offset index and decodes only that selected session's
records. The index checks the source inode/device, size and modification time
under a shared log lock; normal appends update it under the exclusive append
lock. Missing/stale indexes rebuild lazily. Metadata is capped at 8 MiB; above
that bound the existing lookup path is used. A selected large archive can still
require substantial memory, and unpinned history queries can load more data.

Imported run artifacts have a separate content-addressed byte store. Existing
HTTP/stream bodies have not been migrated to it. See
[architecture measurements](architecture.md#local-measurements) for a controlled
saved-lookup comparison and the cost of opt-in durable operation journaling.

## Historical body verification — September 20

The Go suite, host/store/headless race tests, 237 extension tests, and 119 Canvas
tests passed. Regression coverage includes compressed/cached bodies, late setup
callbacks, empty and partial streams, child request ID collisions, detached
frames, Unicode/BOM/binary preservation, missing/corrupt fragments, archive write
failure, immutable retrieval, JSON pointers, record pagination, and output budgets.

An actual JavaScript fragment sender transferred 2,250,000 decoded Unicode bytes
through 13 frames to the compiled Go native host. A corrupt frame failed while
the earlier valid snapshot remained available. A Canvas adapter test read a
3 MiB body with a 4 KiB stdout budget.

`scripts/verify_body_capture.py` runs a real isolated headless browser against
loopback fixtures: over-2-MiB gzip JSON, chunked NDJSON, finite/open SSE, binary,
exact artifacts, and archive reuse. One optional Jev call selected the intended
test button. Measurements and repeatable commands are in [headless.md](headless.md).
These are local fixture results, not a guarantee about every remote website.

The September 20 CLI and native host were installed in `~/.local/bin`, and the
Arc extension was reloaded while it had zero active/queued captures. An actual Arc
metadata-only action then captured all 2,109,569 decoded bytes of the gzip fixture
through `cdp-stream`, verified the archive and exact raw artifact, and closed its
owned test tab. The installed binaries also passed the headless fixture and the
two-agent isolation check. Temporary test profiles and task data were removed.

These are historical results for the body-capture foundation. Current source
validation uses `go test ./...` in rep-cli and `npm test` in rep; the new stream
and evidence tests cover their own contracts. The earlier installed-build check
does not establish that the current capture protocol is already active.

Primary protocol references: [CDP Network](https://chromedevtools.github.io/devtools-protocol/tot/Network/),
[Chrome related-target debugging](https://developer.chrome.com/docs/extensions/reference/api/debugger#attach-to-related-targets),
[Native Messaging framing](https://developer.chrome.com/docs/extensions/develop/concepts/native-messaging),
[SSE parsing](https://html.spec.whatwg.org/multipage/server-sent-events.html#parsing-an-event-stream),
and [JSON Pointer](https://www.rfc-editor.org/rfc/rfc6901).
