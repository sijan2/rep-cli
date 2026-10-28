# Replay evidence and HTTP client fidelity

`rep replay ID` sends the selected captured request with Go's `net/http`
client. Review the response's capture state before interpreting a comparison.
The same body evidence rules apply to existing matrix results.

## Exact body comparison

`diff.body_comparison.state` is one of:

| State | Meaning | `body_distinct` |
| --- | --- | --- |
| `equal` | Both bodies are verified complete and every byte matches. | `false` |
| `different` | Both bodies are verified complete and their bytes differ. | `true` |
| `inconclusive` | At least one body is incomplete, unavailable, unknown, or invalid. | `null` |

Comparison covers the entire retained body, including whitespace and bytes
beyond the inline preview. Captured base64 bodies are decoded first. Captured
byte counts and any stored SHA-256 digest must match the decoded bytes.
Corrupt evidence produces `body_error` and an inconclusive comparison.
Legacy records without capture metadata remain `unknown`, including records
with an empty body. A verified complete zero-byte body can compare equal.

`body_comparison` also reports `method: "exact_bytes"`, `original_state`,
`replay_state`, and a reason when inconclusive. The original and replay
summaries include `body_capture`; existing matrix results carry their own
`body_capture` and `body_comparison`.

`size` counts decoded retained bytes. `size_delta` compares retained byte counts
and can therefore reflect capture limits; it does not prove a difference in
full response lengths. It is `null` when the original response is missing or
its body evidence is invalid. `header_changes` reports added and removed header names;
it does not compare header values.

## Bounded response reads

Replay retains at most **16 MiB** per response. It reads one extra byte to
distinguish a response exactly at the limit from a larger response:

- A body that reaches EOF without a read error has state `complete`.
- A body that exceeds the limit has state `partial`, reason
  `replay_body_limit`, and `body_truncated: true`.
- A body read failure retains received bytes and response headers, reports
  state `partial`, reason `response_body_read_failed`, and `body_error`.
- Failures before a response is available remain command errors.

The replay's `body_capture.sha256` describes retained bytes. `observed_bytes`
counts bytes read by this invocation, including the discarded sentinel when
truncated. For partial responses it is a lower bound on the response size.

`--include-body` returns retained bytes with `replay_body_encoding` (`utf8` or
`base64`). Inline previews also have an explicit encoding; invalid UTF-8 bytes
are base64 encoded. Large previews link to a private spill file containing raw
retained bytes. A preview or spill file can still represent a partial capture.

Spill files have unique names and mode 0600 in the task's private
`replay-exports/` directory. Global mode uses a private `rep-artifacts/replay-exports/`
directory under the system temporary directory. Repeated requests and different
tasks cannot overwrite previous artifacts. `REP_SPILL_DIR` no longer changes
replay artifact locations.

A failed artifact write reports `artifact_error` in the replay summary or
matrix result, and leaves its artifact path absent. Response metadata and the
comparison remain available. This error does not retry the network request.
Check for `artifact_error` before assuming a complete response body can be read
back from disk.

## Transport fidelity

The output includes `fidelity.client: "go-net-http"`,
`fidelity.browser_equivalent: false`, the response read limit, and
`response_body_representation: "go_http_response_body"`.

Replay reports the final URL after redirects, HTTP protocol, and whether Go
automatically decompressed the response. Go may automatically decode gzip when
it selected the request's compression header; a captured explicit
`Accept-Encoding` header can leave the replay body compressed. An exact byte
comparison can therefore differ from a browser-decoded capture because the
representations differ.

This client copies captured request headers, including captured cookies. It
does not run page JavaScript or a service worker, use the browser's current
cookie jar, or reproduce browser TLS behavior, HTTP wire framing, connection
reuse, or redirect behavior exactly. Body equality establishes equality of the
compared payload bytes only; it does not establish equivalent browser behavior
or establish whether a vulnerability exists.
