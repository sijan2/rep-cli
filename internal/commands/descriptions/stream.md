# rep stream

Read one task's WebSocket, WebTransport, WebRTC data-channel or media evidence. The ID is
the record ID returned by a capture or summary. `--saved FULL_HASH` pins
the source archive, including when a later capture reuses the same browser ID.

```sh
rep --workspace PROJECT --task AGENT stream ID --saved HASH --info
rep --workspace PROJECT --task AGENT stream ID --saved HASH --events 20 --max-bytes 4096
rep --workspace PROJECT --task AGENT stream ID --saved HASH --after SEQUENCE
rep --workspace PROJECT --task AGENT stream ID --saved HASH --event SEQUENCE --head 4096
rep --workspace PROJECT --task AGENT stream ID --saved HASH --event SEQUENCE --head 0 --save
```

Default output is a bounded page of event descriptors. Message/chunk payloads are
explicitly omitted and remain retrievable by sequence. `next_after` acknowledges
only descriptors actually returned; `remaining_events` and `page_complete`
describe the stored event list. They do not establish connection completeness.

`--event N` returns exact decoded message or chunk bytes in `body`. Text uses UTF-8;
binary byte ranges use base64. `--offset` and `--head` select decoded byte ranges.
Follow `next_offset` until the whole payload is read. `--head 0` selects all bytes,
but inline JSON remains bounded by `--max-bytes` (default 8192, minimum 1024).
`--save` writes the selected range to a private artifact with a SHA-256 digest.
Use `--head 0 --save` for an entire captured message. An empty payload remains
explicit. Invalid base64 and byte-count mismatches fail.
Lifecycle, statistics and gap events have no payload; inspect their descriptors
instead of treating them as empty messages.

`capture.state`, `scope`, `limitations`, `reason`, byte/event counts, and
dropped-event counts describe coverage. `--require-complete` rejects partial,
unavailable, or unknown coverage, gaps, truncation, and inconsistent counters.
Completeness applies to the declared observer scope. Static limitations can
remain on a complete scoped capture; this does not establish complete wire traffic.
A view or saved range can be complete while the connection capture is partial.
`payload_validation` distinguishes an ordinary descriptor read from verified
encoding and decoded lengths. `--require-complete` checks all retained payloads
with bounded decoding memory; this does not establish cryptographic provenance.
An open connection at capture end, a connection begun before attachment, a
detached target, event loss, or a payload budget hit remains partial.

Records include source, payload semantics, direction, channel identity, event
metadata, source ordering and lifecycle. WebSocket events also carry opcode.
CDP timestamps use CDP monotonic seconds. Page API timestamps use
`performance.now()/1000` within one realm; metadata can include `time_origin_ms`.
Clocks from different contexts are not directly comparable. Missing timestamps
are unknown.

`source: "cdp_lifecycle"` WebTransport records expose lifecycle evidence with
`payload_semantics: "unavailable"` and partial coverage. Independent page API
records expose instrumented application calls: WebTransport stream chunks and
datagrams, or WebRTC data-channel messages. Each WebRTC record represents one
data channel; metadata can identify its parent peer. `channel_id` distinguishes
channels within a WebTransport record. Original chunk boundaries follow the API
observer and do not reconstruct transport frames.

Page API metadata declares `observer_trust: "page_controlled"`, its scope and
limitations. It cannot establish observations of uninstrumented workers,
pre-existing objects or bypassed APIs. Lifecycle-only and page API records are
independent observations; a shared URL does not establish a matching connection.
The archive can retain handshake/HTTP metadata; this command does not print
headers or reconstruct encrypted packets, QUIC frames, RTP media, or wire timing.

`webrtc_media` records expose binary MediaRecorder container segments. Their
source is `browser_media_recorder`, semantics `reencoded_media`, and scope
`recorded_media_interval`. Use `rep media ID --saved HASH --require-complete
--save recording.webm` to join ordered segments. Track recordings and raw
`packets capture` artifacts retain separate provenance; neither implies that
particular wire packets correspond to a given media chunk.
