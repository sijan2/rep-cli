# rep browser native-capture

Launch a new private Chromium session with explicitly enabled native diagnostics.
The session has no Rep extension or page API instrumentation. The browser connects
directly using its existing network configuration; Rep adds no proxy or replacement
certificate. Diagnostic flags and resource overhead can change browser behavior.

```sh
rep --workspace demo --task native scope -j
rep --workspace demo --task native summary --max-bytes 4096
rep --workspace demo --task native browser native-capture http://127.0.0.1:8080 \
  --webrtc-rtp --tls-keys --interface lo0 \
  --filter 'udp and host 127.0.0.1 and port 8443' \
  --duration 30s --output /tmp/rep-native-session
```

An explicit workspace/task and a new output directory are required. Choose at
least one of `--tls-keys` and `--webrtc-rtp`. `--tls-keys` also requires
`--interface` and `--filter`; both packet options may accompany RTP capture.
The filter applies to wire packets. Native browser diagnostics cover connections
inside this newly launched browser process, including background TLS traffic,
independently of that filter or the target URL. A URL does not identify which
interface packets belong to a browser tab.

| Flag | Meaning |
|---|---|
| `--binary PATH` | Installed Chromium executable; otherwise use `REP_HEADLESS_BINARY` or discovery |
| `--output DIR` | Exclusive new bundle, directory mode 0700 and artifact files mode 0600 |
| `--tls-keys` | Enable Chromium's native TLS/QUIC key log for this new session |
| `--webrtc-rtp` | Enable native plaintext RTP/RTCP diagnostics at the SRTP boundary |
| `--interface NAME`, `--filter BPF` | Start filtered native libpcap before navigation |
| `--headless` | Run without a visible window; the default is a visible private browser |
| `--duration` | Default 15 seconds after navigation, maximum 10 minutes; startup and shutdown add time |
| `--max-log-bytes` | Default 32 MiB each for retained native browser log and derived RTP JSONL |
| `--max-key-log-bytes` | Default 1 MiB key-log stop threshold |
| `--max-packet-bytes` | Default 64 MiB wire PCAP limit |

The private profile is created for this invocation and removed after confirmed
browser exit. If exit cannot be confirmed, capture fails and preserves the profile
and unfinalized artifacts; `browser.profile_removed` remains false. Existing
browser windows, profiles and logins are independent. The loopback
debug endpoint is used for session ownership and navigation. `ready.json` appears
only after the owned endpoint is verified and requested packet capture is ready.
It is removed during cleanup. Capture closes only the browser it launched.
The debugging launch and optional headless mode can expose automation-related
browser state, including `navigator.webdriver`; this state is left unchanged.

## Artifacts and provenance

The bounded JSON result and `manifest.json` contain paths, SHA-256 hashes, byte
counts, stop reasons, resource limits and capability observations. They contain
no inline key material or media payloads. Raw diagnostics stay in the private
bundle. With `--run RUN_ID`, the evidence store imports only the manifest and
records artifact references; it does not duplicate keys or media automatically.

TLS/QUIC keys come from Chromium's supported key-log switch. They enable a
compatible analyzer to decrypt matching captured sessions when enough handshake
and packet data were retained. Empty output is reported as unobserved; flags
alone do not prove support. This does not obtain DTLS/SRTP keys or remove an
application's own additional encryption.

RTP JSONL contains exact base64 datagrams emitted by native libWebRTC:
outbound immediately before SRTP protection, inbound after successful SRTP
authentication/decryption. No track clones or additional media encoders are used.
RTCP remains distinct from RTP. Each record has an ordinal, direction and
available native timing provenance. IP/UDP headers are not invented. Dynamic RTP
payload types require the session's negotiated codec mapping for decoding.

The raw native log is retained alongside parsed records. The parser also limits
output to 100,000 datagrams and each source line to 256 KiB. Parser omissions,
malformed records and resource limits remain explicit. Log order is an observation
order, not proof of wire arrival order; native dump clock strings can be inaccurate.
An outgoing plaintext record does not prove successful encryption or delivery.
Packets failing incoming SRTP authentication are absent from the plaintext log.

The browser log writer stops retaining bytes exactly at its budget, keeps draining
the pipe, and requests shutdown. Chromium writes its key file independently; its
size threshold is monitored and can overshoot during detection and shutdown.
After shutdown, only complete key-log lines within the retained-byte budget are
kept. The result discloses observed, retained and discarded sizes. PCAP writes stop at whole records
within their budget. A duration or successful exit does not prove zero packet
loss. Read the capability and omission fields before drawing payload conclusions.

This diagnostic mode is explicit opt-in for a newly owned session. It does not
attach to unrelated browser processes or guarantee that diagnostics are
unobservable. Use `rep packets capture` for passive interface observation without
browser diagnostics. See `docs/native-capture.md` for source references and
local validation results.
