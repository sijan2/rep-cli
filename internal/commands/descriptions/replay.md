# rep replay

Send one selected captured request with Go's `net/http` client and compare the
response. Select the same workspace/task as the capture. Replay performs a real
HTTP request to its captured URL and can repeat the original operation's effects.

## Body evidence

`diff.body_comparison.state` is `equal`, `different`, or `inconclusive`.
The comparison checks every decoded body byte, including whitespace. It validates
captured byte counts and any stored SHA-256 digest before comparing. Both bodies
must have verified `complete` capture state. Missing metadata, missing responses,
partial captures, and corrupt evidence produce `inconclusive`.

`body_distinct` is `false` for verified equality, `true` for verified difference,
and `null` when inconclusive. Read `body_comparison.state` before acting on this
field. Complete zero-byte captures can compare equal; legacy empty strings do
not establish an empty response. Existing matrix results use the same contract.
`size_delta` counts retained bytes and is `null` for missing or invalid originals.
`header_changes` compares added/removed header names only.

## Limits and artifacts

Replay retains at most 16 MiB per response. One sentinel byte detects truncation;
an exactly 16 MiB response can still be complete. An over-limit response reports
`body_capture.state: "partial"`, reason `replay_body_limit`, and
`body_truncated: true`. A read failure preserves received bytes and reports
`body_error` with partial capture state. Failure before a response remains an
execution error. No automatic network retry is added for capture or file errors.

Summaries include `body_capture`, with SHA-256 and captured/observed byte counts.
For partial bodies, observed bytes are a lower bound. Inline previews carry
`inline_body_encoding`; `--include-body` carries `replay_body_encoding`. Invalid
UTF-8 bytes use base64. Previews can be shorter than retained bytes.

Larger bodies have unique mode-0600 artifacts in the task's private
`replay-exports/` directory. Global mode uses a private directory under the
system temporary directory. `REP_SPILL_DIR` does not override these locations.
Artifact failures appear as `artifact_error`, leave the artifact path absent,
and preserve response/comparison metadata. Check for this field before assuming
the retained body is available on disk. Existing artifacts are never overwritten.

## HTTP client fidelity

`fidelity.client` is `go-net-http`; `browser_equivalent` is `false`.
Response metadata reports the final URL, HTTP protocol, and automatic gzip
decompression when it occurred. Explicit captured compression headers can leave
replay bytes compressed while browser capture bytes were decoded; differing
representations can produce an exact-byte difference.

Captured cookies are copied into the request. The client does not use the
browser's current cookie jar or run page scripts/service workers, and does not
reproduce browser TLS behavior, wire framing, connection reuse, or redirects
exactly. Byte equality does not establish browser equivalence or a vulnerability.

## Differential replay: `--baseline` and `--as`

A live endpoint embeds per-response nonces, timestamps and trace IDs. Comparing
one replay against a capture taken at a different moment therefore reports a
difference every time, and `body_distinct: true` carries no information.

`--baseline N` replays the **unmutated** request N times first (N >= 2) and
learns which fields the endpoint changes on its own. The resulting volatility
mask is then used to judge the candidate on the fields that held still.

- JSON responses are compared structurally, by JSON pointer, so key reordering
  and whitespace do not register as changes.
- Other responses fall back to line positions, and only when the line count is
  stable across identical requests. A body whose shape moves by itself yields an
  **unusable** mask and an `inconclusive` verdict rather than a guess.
- A status change counts as a difference only when the endpoint proved its
  status stable across the baseline samples.

`--as alice,bob,anon` re-executes the request as each named principal (see
`rep describe identity`) and compares each response against the baseline.

### Verdicts

Comparisons are tri-state — `differs`, `equivalent`, `inconclusive`. An
`inconclusive` result is a real outcome and is never rounded toward either side;
incomplete body evidence always produces it.

The campaign-level `authz` verdict is one of:

| Verdict | Meaning |
|---|---|
| `enforced` | every other principal received a materially different response |
| `possible_authz_issue` | some principal's response was equivalent on all stable fields |
| `inconclusive` | at least one principal produced evidence that could not be compared |

`possible_authz_issue` is a **lead, not a finding**. An equivalent response is
not proof that the protected resource was returned: the endpoint may return a
generic shell to everyone. Inspect the body, confirm it contains the owner's
resource, and name that resource before recording the finding.

`--as` and `--baseline` cannot be combined with `--matrix`. Run the sweep first,
then re-test the interesting value with a baseline.

## Advanced flags

These flags stay accepted with their existing behavior and are hidden from `--help` to keep the everyday surface small: `--include-body` returns retained response bytes inline (up to 16 MiB per response); prefer `rep body` for archived bytes.
