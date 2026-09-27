# WebTransport and WebRTC capture

Rep captures WebTransport application datagrams and stream chunks, and WebRTC
data-channel application messages, during an explicit browser capture. The
collector is deterministic and uses no model calls. Use an owned tab and a
separate workspace/task for each agent.

## Use it

```sh
rep --workspace demo --task transports scope -j
rep --workspace demo --task transports summary --max-bytes 4096

# Install the observer before application scripts construct transports.
rep --workspace demo --task transports browser open https://localhost:8443 \
  --protocol-payloads --keep-tab --raw-json

# Or construct and exercise a transport from an already owned tab.
rep --workspace demo --task transports browser action @fixture.js \
  --tab TAB_ID --protocol-payloads --settle 500ms --raw-json

# Read the exact archive returned by that operation.
rep --workspace demo --task transports summary --saved HASH --max-bytes 4096
rep --workspace demo --task transports stream RECORD_ID --saved HASH --info
rep --workspace demo --task transports stream RECORD_ID --saved HASH --events 20
rep --workspace demo --task transports stream RECORD_ID --saved HASH \
  --event SEQUENCE --head 0 --save
```

Use the returned context cursor for later deltas. Add `--require-complete` when
the declared observation scope must be complete. An open connection at capture
end, a destroyed context, missed batch, bypassed read, failed payload conversion,
or budget hit remains partial. The selected payload can also be truncated even
when its retained prefix was saved successfully. Always inspect both levels.

The flag also exists on `browse` and `browser fetch`. Fetch can observe new
transports created after its collector attaches; it cannot recover existing
objects or earlier messages. For page startup traffic, use `browser open`.

## Evidence contract

| Record | Source and contents |
|---|---|
| WebSocket | CDP application messages and connection lifecycle |
| WebTransport lifecycle | `cdp_lifecycle`; creation, establishment and close; payload unavailable |
| WebTransport application | `page_api`; datagrams and direct reader/writer calls, stream direction and local channel IDs |
| WebRTC data channel | `page_api`; one record per channel, text/binary/Blob payloads, parent peer, channel configuration and peer state changes |

WebTransport API and CDP observations have separate identities. A URL match is
insufficient to establish that two records represent the same connection. Byte
stream chunks follow application reads/writes, not QUIC framing. Accepted sends
show local API acceptance; received messages or independent server evidence are
needed to establish delivery.

Archives retain `source`, `clock`, `payload_semantics`, `capture.scope`,
`capture.limitations`, effective observer limits, and execution-context identity.
CDP uses monotonic seconds. API timestamps are `performance.now()/1000` in that
realm, with `time_origin_ms` metadata; do not compare different realms as one
clock. Local IDs are observation identities, not QUIC or SCTP wire IDs.

The page observer wraps native constructors, reader/writer methods and channel
methods. It copies observed bytes before application mutation and preserves
native objects, return values, promises, and application stream backpressure.
It neither tees nor drains a stream. Attaching promise handlers can affect
unhandled-rejection diagnostics, and copying/serialization adds work.
Cleanup restores hooks only when they still point to Rep's wrapper, so it does
not overwrite later application changes. It removes its own globals after the
final flush and leaves transports open.

## Limits and gaps

Default capture bounds remain 64 MiB aggregate archived payloads, 10,000
request/connection records, 10,000 retained stream events, and 16 MiB native
export backlog. The observer also bounds each realm to at most 8 MiB per payload
(or a smaller `--max-body`), 16 MiB pending copied payloads and 256 pending events.
It batches up to 32 events toward a 256 KiB target; a single large event may use
up to a 16 MiB binding envelope. Native binary base64 conversion is used when
available. The background enforces shared capture budgets across all realms.

At most 128 tracked execution contexts receive API observation within a capture.
Excess contexts disable the target's startup observer and report a coverage gap.
These limits bound retained capture data, not Chromium's memory, application
buffers, all metadata allocation, or total process RSS. High-rate capture can
reach the limits; counts and partial reasons remain part of the evidence.

Pre-existing objects, cached native methods, transferred streams, unattached
shared/service workers and some stream piping paths can bypass observation.
Known pipe/tee/iterator uses produce gaps. This observer is page-controlled:
application code can alter or forge it. A random binding token separates capture
sessions but does not authenticate page-supplied data. The saved archive hash
verifies the stored bytes, not the truth of a page's claims.

Raw interface packets have a separate native `rep packets capture` path.
`--webrtc-media` records existing audio/video track clones through Chromium's
native MediaRecorder. Those artifacts preserve distinct sources and scopes:
encrypted interface bytes and re-encoded media recordings. See
[native packets and media](native-capture.md). Neither of those paths extracts TLS keys
or promises exact original encoded RTP frames. Other diagnostic artifacts can
be retained with `rep evidence import`, preserving declared coverage.

`browser native-capture` adds an independent new private Chromium session with
explicit native TLS/QUIC key logging and original plaintext RTP/RTCP logging.
It does not use this page observer; see the native capture guide for its
artifact formats, supported-build observations and loss boundaries.

## Verification

`npm test` covers native object identity, no read-ahead, failed sends/writes,
Blob ordering and timeout, byte/event budgets, partial coverage, context reuse,
setup/cleanup races, and conditional restoration. `go test ./...` covers exact
stream retrieval, bounded views, source preservation and completeness checks.

`scripts/verify_transport_capture.py --webtransport --quic-python PYTHON`
uses a private Chromium profile, two local WebRTC peers, and a loopback aioquic
HTTP/3 echo server. Its temporary certificate is pinned by the fixture without
changing browser trust settings. The script compares saved bytes with independent
page/server hashes and checks incomplete cases. `--stress` adds a bounded local
RTC burst; timings include the fixture and capture overhead named in the report.

Primary API contracts: [Chrome DevTools Network](https://chromedevtools.github.io/devtools-protocol/tot/Network/),
[W3C WebTransport](https://www.w3.org/TR/webtransport/),
[W3C WebRTC](https://www.w3.org/TR/webrtc/).
