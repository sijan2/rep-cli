# gc — task-local body retention

Select an explicit workspace and task on every invocation (or set
`REP_WORKSPACE` and `REP_TASK`). Shared legacy/global stores are refused.

```sh
rep --workspace research --task authz gc plan --max-bytes 268435456 --keep-days 30
rep --workspace research --task authz gc apply --max-bytes 268435456 --keep-days 30 --yes
rep --workspace research --task authz gc status -j
```

`rep gc` defaults to `plan`. Plan and status do not write files. Apply always
displays its snapshot before mutation; without `--yes` it returns the plan and
the `confirmation_required` error, with a nonzero exit status. With `--yes`,
JSON output consists of two newline-delimited envelopes: `phase: plan`, then
`phase: result`. Refused apply emits one plan envelope containing its error.

| Class | Policy |
| --- | --- |
| evidence | Never evict. Requests referenced by notes, findings, probe records, identities, or evidence runs, and entire archives referenced by archive ID, are protected. Session notes protect their whole occurrence. Evidence journals and artifacts stay immutable. Deleted/cleared note history still pins its references. |
| primary | Dynamic requests on primary domains and their subdomains; age limit defaults to 30 days. Without configured primary domains, unknown dynamic traffic gets this longer retention. |
| ambient | Static assets identified by extension, content type, or resource type, and hosts outside configured primary domains; age limit is the smaller of one day and `--keep-days`. |

The default task disk budget is 256 MiB. `--max-bytes N` accepts nonnegative
integer bytes; `--keep-days D` accepts 0–365000 days. Age-expired bodies are
selected first. Additional byte pressure selects oldest ambient bodies, then
oldest primary bodies, including recent bodies if necessary. Evidence protection
always takes precedence. Missing timestamps do not expire by age. Request
metadata, evidence, live capture files, browser profiles, and unmanaged artifacts
can prevent reaching the budget; `budget_unmet_bytes` reports the remainder.
Under budget pressure, bodies whose eviction markers would cost more than their
serialized bytes are retained. Age-based eviction can increase disk usage for
very small bodies; signed reclaimed byte counts accurately reflect that change.

GC rewrites body fields in this task's `sessions.jsonl` and legacy `store.json`,
without removing request records, event records, archive records, headers,
timing, status, or provenance. Stream event payloads are body bytes too. Untouched
and protected request objects remain byte-for-byte unchanged, including duplicate
historical occurrences. Unknown metadata fields survive rewriting.

An evicted body has `*_body_capture.state: unavailable`,
`reason: "retention_evicted: retention_age"` or `retention_budget`, and
`captured_bytes: 0`. Its request also carries `retention.state: evicted`, the
class, time, reason, stored byte counts, and original capture descriptors. Old
body digests stay in those original descriptors rather than describing the new
empty value. Genuine complete empty bodies remain complete. Inspect the same
archive with `rep body ID --saved HASH --info`; `--require-complete` refuses
evicted bodies.

Reports include current storage/body usage per class and selected task, planned
and actual reclaimed bytes per class, protected references and their sources,
and skipped files and reasons. Eviction/skip details are sampled to 100 entries
each; omission counts and envelope truncation describe the samples. Aggregate
counts cover the entire selected snapshot. Body byte counters measure stored
strings (including base64), while reclaimed bytes measure actual serialized file
size differences. Metadata and unmanaged file bytes are reported separately.

Live exports and capture staging files are never rewritten. GC uses nonblocking
file locks, detects writable descriptors with `lsof`, skips staged or active
session appends, and checks source identity and reference snapshots before
atomic 0600 replacements. A changed source is skipped. Changed or unreadable
references abort applying the plan so new evidence is not knowingly evicted.
The existing archive index invalidates itself when the source stamp changes.
Symlinked captures are skipped, and symlinked evidence/reference trees are
refused. Writers that bypass the store locks are detected through descriptor
and stamp checks; there is no shared transaction with arbitrary external file
writers. If writer inspection is unavailable, affected captures are skipped.

Sessions are scanned one request at a time, including gigabyte JSONL records.
An individual request or metadata value larger than 64 MiB is skipped with its
whole source file; unavailable or malformed reference data fails closed.
Status/remaining byte totals describe the scan snapshot, including retained
nonbody bytes, rather than a live meter of concurrently growing captures.
