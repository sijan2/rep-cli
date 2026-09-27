# rep packets

Passive native packet collection and bounded classic-PCAP inspection. Live
capture needs a macOS cgo/libpcap build and permission to open the selected BPF
device. Interface enumeration needs no task. Capture and inspection require an
explicit workspace and task.

```sh
rep packets interfaces
rep --workspace PROJECT --task TRIAL packets capture \
  --interface lo0 --filter 'udp and src port 32123 and dst port 32124' \
  --output /private/path/trial.pcap --duration 2s --max-packets 100
rep --workspace PROJECT --task TRIAL packets inspect /private/path/trial.pcap \
  --offset 24 --limit 20 --max-scan-bytes 1048576 --max-bytes 8192
```

Use a filter for the intended traffic. The example ports must belong to your
local fixture. Capture is nonpromiscuous and does not change interface settings.
No permission elevation is attempted. `capture --output` names the artifact;
this command always emits bounded JSON.

Defaults: duration 10s, 100,000 packets, 64 MiB of pcap bytes, 65,535 captured
bytes per packet, and a requested 4 MiB kernel capture buffer. Maximums: 1h,
10 million packets, 1 GiB, 262,144 bytes per packet, and 64 MiB respectively.
The byte limit includes the 24-byte file header and each 16-byte packet header.
Packets that do not fit are not partially written. A fixed 1 MiB C writer
buffer is additional memory; these limits are not a strict process RSS cap.

Capture creates a new mode-0600 `.pcap` and matching `.metadata.json` without
replacing either file or a symlink. The directory must already exist. Metadata
records pending state before capture, final status, limits, host timing,
caplen/wirelen totals, truncation, raw libpcap counters, SHA-256, and coverage.
SIGINT/SIGTERM stops and finalizes a partial observation. A hard process kill
can leave unfinalized files; a pending sidecar is not a final manifest.
Cancellation retains `status:cancelled` and returns a nonzero exit with
`command_error.code:capture_cancelled`. Final metadata is atomically published
over the owned pending sidecar; PCAP records are streamed as they arrive.

`--run RUN_ID` records durable pending/completion operations and imports both
artifacts into the selected task's run. Failed setup is retained too. A BPF
permission failure has `error_code:capture_permission_denied`, no observation
start, `coverage.state:none`, and a zero-byte artifact with `pcap_valid:false`.
Capture JSON is bounded to 128 KiB. Unsupported builds fail before creating
capture artifacts; offline inspection remains available.
`command_error` identifies a failed command even when native observation
completed before a finalization error. Evidence failures additionally appear
in `evidence_error`. Native capture/evidence failures return a nonzero exit.

Coverage scope is `filtered_interface_observation`. No output claims complete
wire coverage. Original byte counts cover written records only. `ps_recv` may
include unfiltered or unread packets depending on platform; zero `ps_drop` may
be unsupported, and zero `ps_ifdrop` does not establish no interface drops.
Offloads and the OS capture point can change packet presentation. Hashes check
byte identity, not collection completeness.

Inspection supports classic pcap 2.4 in either byte order and microsecond or
nanosecond timestamps. PCAPNG is explicitly unsupported. Common loopback,
Ethernet, raw-IP and Linux cooked headers can expose IPv4/IPv6 and UDP fields.
Only header shapes can produce `quic_long_header_candidate` or
`rtp_v2_header_candidate`, always with `verified:false`. No protocol is inferred
from a port alone. Encrypted QUIC/SRTP bytes remain encrypted. No decryption,
reassembly, payload output, or complete-protocol decoding is performed.

Inspection returns at most 1,000 packet descriptors, traverses at most 64 MiB
of record bytes per page, and reads at most 4 KiB of headers per packet. The
JSON budget is 1,024–262,144 bytes. Use the returned `next_offset` to continue;
budget truncation never skips a descriptor. `output_omitted_packets` counts
descriptors deferred to the next page by the JSON budget. Supplied offsets are
presumed record boundaries. `has_more:false` means this file page ended, not that the
original capture was complete. See `docs/packet-capture.md` for details.
