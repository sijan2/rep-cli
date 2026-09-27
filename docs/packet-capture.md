# Native packet capture

`rep packets` preserves bytes supplied by one filtered capture interface and
produces a separate metadata file describing the observation. It also provides
bounded offline packet header inspection.

## Build and permissions

Live capture uses macOS system libpcap through cgo. Build with Xcode command
line tools and cgo enabled. Other builds can inspect classic PCAP files but
return `packet_capture_unsupported` for live capture and interface listing.
The packet loop writes directly in C without a per-packet Go/JavaScript callback
or an external capture process.

```sh
go test ./...
CGO_ENABLED=1 go build -o /tmp/rep-native .
/tmp/rep-native packets interfaces
```

Opening a macOS BPF device requires account/device permissions configured by
the machine administrator. Enumeration reports `capture_permission: "not_checked"`;
it can succeed when capture cannot. The CLI leaves system permissions unchanged.
Wireshark's signed ChmodBPF installer is one supported macOS setup route; see
[Wireshark's installation guide](https://www.wireshark.org/docs/wsug_html_chunked/ChBuildInstallOSXInstall.html).

ChmodBPF installs a launch daemon that prepares device access for the
`access_bpf` group when loaded, then exits. This is persistent permission setup;
the helper does not record traffic. Rep can then run as the approved ordinary
user, with capture started explicitly and bounded by each command. An existing
installer receipt or group membership alone does not prove that the device
permissions or launch daemon are present and working.

Packet capture passively reads operating-system packet copies. It requires no
browser extension, injected page script, proxy, replacement certificate or
browser launch flag. Chrome continues using its configured network path. Separate
browser collectors can still add observable instrumentation; see
[capture architecture](native-capture.md#capture-architecture).

## Record an owned local fixture

Capture and inspection require an explicit workspace and task. Use addresses
and ports belonging to the intended observation. The interface and BPF filter
are required, and promiscuous capture is disabled. libpcap enforces the filter;
the platform can execute it in the kernel or user space.

```sh
rep --workspace demo --task local-packets scope -j
rep --workspace demo --task local-packets summary --max-bytes 4096
rep --workspace demo --task local-packets packets capture \
  --interface lo0 --filter 'udp and host 127.0.0.1 and port 8443' \
  --duration 10s --max-packets 100000 --max-bytes 67108864 \
  --snaplen 65535 --buffer-bytes 4194304 --output /tmp/fixture.pcap
```

Duration, packet count and file byte limits apply together. The duration bounds
the observation interval after setup. The byte limit includes the global and
record headers, so a limit of 24 permits only the global header. Whole retained
records are written, and limits have explicit stop reasons. `snaplen` bounds
retained bytes per packet; truncation is counted. The requested kernel buffer
and a fixed native writer buffer are additional memory, so file limits are not
a process memory cap.

SIGINT and SIGTERM request cancellation and finalization. The capture result
keeps `status: "cancelled"`, partial coverage and `stop_reason: "cancelled"`.
The CLI returns a nonzero exit with `command_error.code: "capture_cancelled"`.
An attached evidence operation is failed because the requested collection was
interrupted; its result preserves the distinct cancellation outcome. A forced
kill or machine shutdown can leave a pending sidecar and unfinished packet file.

## Artifacts and evidence

The output `.pcap` and matching `.metadata.json` files use mode 0600. Existing
paths, including symlinks, are rejected. The output directory must exist.
PCAP records are streamed into the exclusively created file. A durable pending
sidecar precedes observation. Final metadata is synced and atomically published
over the owned pending sidecar; the PCAP itself is a streamed artifact.

The sidecar includes the file SHA-256 and byte count, interface/filter, limits,
link type, capture times, stop reason, captured/original byte totals, truncation,
libpcap receive/drop counters, coverage and native library version. Counters
cover the observation; original byte totals cover retained packet records.

If native setup fails after output reservation, both files remain for diagnosis.
A BPF denial records `capture_permission_denied`, `status: "failed"`,
`pcap_valid: false`, an empty packet file and `coverage.state: "none"` when no
observation began. Invalid options, unsupported builds or existing output paths
fail before observation. Empty bytes never prove an empty network response.

Add `--run RUN_ID` to attach the attempt to an existing run in the selected
task. A pending operation is written before capture. Both artifacts, including
failed attempts, are imported when available. Their collector and declared
coverage are retained; the imported PCAP hash is checked against the capture
hash. Output JSON includes operation/completion references and artifact IDs.
The run groups evidence explicitly; it does not prove packet ownership by a
tab, track, browser connection or process.

Capture returns bounded JSON up to 128 KiB. A native finalization error may
leave a successful observation result while `command_error` explains why the
command failed. Evidence errors also appear in `evidence_error`. Such failures
return nonzero and preserve the original artifacts for inspection.

## Inspect bounded packet headers

```sh
rep --workspace demo --task local-packets packets inspect /tmp/fixture.pcap \
  --offset 24 --limit 20 --max-scan-bytes 1048576 --max-bytes 8192
```

Use `next_offset` for the next page and stop when `has_more` is false. The
inspector reads classic PCAP 2.4 in either byte order with microsecond or
nanosecond timestamps. PCAPNG needs a classic PCAP export first. Input must be
a regular file; changes during inspection are rejected. Offsets must be known
record boundaries, normally obtained from the previous page.

`--limit` permits 1–1000 descriptors. `--max-scan-bytes` permits 16–67108864
record bytes including record headers. Only up to 4096 initial bytes per record
are read for header interpretation. `--max-bytes` permits 1024–262144 JSON bytes.
If output budgeting defers descriptors, `output_omitted_packets` reports their
count and `next_offset` stays at the first deferred record. A record or
descriptor that cannot fit alone produces an error requiring a larger budget.

Descriptors include timestamps, captured/original lengths and recognized link,
network and UDP headers. Payload bytes are omitted. QUIC/RTP candidates identify
compatible header shapes with `verified: false`; ports alone never establish
the protocol. These hints do not decode encrypted packets or verify media.

## Coverage limits

Packets reflect the operating system capture point. Offload, link header
transformations, interface/filter choice and buffering affect what is visible.
libpcap receive/drop counters have platform-specific semantics. Zero reported
drops cannot prove all transmitted packets were observed. See
[libpcap's statistics contract](https://www.tcpdump.org/manpages/pcap_stats.3pcap.html).

Coverage describes `filtered_interface_observation`. Normal duration completion
keeps wire completeness `unknown`. Limits, cancellation, captured truncation
and reported drops produce `partial` coverage. Setup failure before observation
is `none`. An inspection page reaching EOF makes no claim about capture coverage.

QUIC, SRTP and TLS retain encrypted bytes. The collector does not recover keys
or produce decoded audio/video. Browser media recording uses native codecs on
track clones with separate re-encoded-media provenance; see
[native packets and WebRTC media](native-capture.md). The exact flag contract is
also embedded in `rep describe packets`.

Granting BPF access resolves device authorization only. Administrator privileges
do not decrypt those protocols or convert browser recordings into the original
RTP codec frames. These remain separate capture scopes even after live packet
verification succeeds.

The [September 27 permission follow-up](validation/native-packets-permission-fixed-2026-09-27.json)
verified live capture as an ordinary user after ChmodBPF restored device access.
The unchanged native binaries captured all four local UDP fixture packets with
exact payload matches and verified artifact hashes. The earlier BPF denial was
an authorization failure; the follow-up confirms the live native capture path
for that fixture.
