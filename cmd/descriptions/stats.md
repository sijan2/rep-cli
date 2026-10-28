# rep stats

Aggregate recorded traffic locally. Operations are explicit commands; fields
are one positional value:

```sh
rep stats count method
rep stats count domain --limit 10
rep stats unique header:authorization
rep stats distribution status
rep stats count path:segment:1
```

Valid fields are `method`, `status`, `domain`, `path`, `body-type`,
`path:segment:N` (nonnegative, zero-based), and `header:<name>` (nonempty).
`count`, `unique`, and `distribution` require exactly one field. Unknown fields,
extra arguments, and conflicting operations fail with `invalid_argument` before
reading traffic.

`--limit N` is available on `count` and `distribution` and can appear before
or after the operation. Positive N limits returned entries; zero or negative N
keeps all entries. `unique` does not accept `--limit`: it has never truncated
its values. The hidden legacy `--top` spelling is still accepted with `unique`
and preserves its prior no-effect behavior, with a stderr note to omit it.

## Source and output

The selected task's nonempty live capture is used first. If it is absent, empty,
or unreadable, the existing extraction loader reads that same namespace's saved
store sessions. This legacy source policy is unchanged; there is no fallback to
another task or shared global data. Select a scope with `--workspace`/`--task`
(or their environment variables), or explicitly opt into `--global`.

With JSON output the existing result shapes remain unchanged:

- Count: `field`, `counts: [{value, count}]`, `total` (all input requests).
- Unique: `field`, `unique_count`, `values` in encounter order.
- Distribution: `field`, `total` (nonempty field values),
  `buckets: [{value, count, percent}]`. Status values are bucketed as `2xx`,
  `4xx`, etc.; a missing response has status `0` and bucket `0xx`.

Count/distribution entries sort by descending count; ties have no ordering
guarantee. Empty values are omitted. With no input, the existing informational
`No requests to analyze` message is preserved even when JSON was requested.
These legacy results are bare JSON, not a new envelope. This command reports
observations, not authorization verdicts; HTTP status alone proves no finding.

## Migration

`--count-by FIELD`, `--unique FIELD`, and `--distribution FIELD` still work on
`rep stats`, hidden from help and with a deprecation note on stderr. Their
replacement is `count FIELD`, `unique FIELD`, and `distribution FIELD`.
`--top N` is also a hidden compatibility spelling for `--limit N`, with a
stderr note; combining them fails. Do not combine multiple old operation flags
or combine one with a new operation.
Previously ignored positionals, malformed fields, and ambiguous combinations
now fail rather than silently selecting an operation or returning empty data.
Field validation rejects names outside the documented set, empty or
whitespace-only `header:` suffixes, and `path:segment:` suffixes that are empty,
negative, out of integer range, or not a complete base-10 integer. The old
segment parser could silently use zero or accept a numeric prefix on malformed
input; neither behavior is retained.
