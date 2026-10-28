# rep group

Group recorded traffic locally by one field. The field is positional data, not
a subcommand:

```sh
rep group method
rep group path --pattern '\d+' --replace ':id'
rep group status --show methods,ids
rep group domain --min-count 5 --limit 10
```

Valid fields are `method`, `status`, `domain`, `path`, and `header:<name>`
(nonempty). Exactly one field is required. Unknown fields, extra arguments, and
mixing a positional field with `--by` fail with `invalid_argument` before reads.

## Options

| Option | Meaning |
| --- | --- |
| `--pattern REGEX` | Go regular expression for group-key normalization. |
| `--replace TEXT` | Replacement for matches; normalization runs only when both pattern and replacement are nonempty. |
| `--min-count N` | Keep groups with at least N requests; values <= 0 keep all. |
| `--limit N` | Keep the N largest groups; values <= 0 keep all. |
| `--show count,statuses,methods,ids` | Text annotations; count is always displayed. JSON retains all existing fields. |

## Source and output

The selected task's nonempty live capture is used first. If it is absent, empty,
or unreadable, the existing extraction loader reads that same namespace's saved
store sessions. This legacy source policy is unchanged; it never falls back to
another task or shared global data. Choose `--workspace`/`--task` (or their
environment variables), or explicitly opt into `--global`.

JSON remains a bare array of objects with `key`, `count`, `request_ids`,
`statuses`, and `methods`. Groups sort by descending count; ties have no ordering
guarantee. IDs remain the existing semantic request IDs. Empty keys use
`(empty)`. A missing response groups under status `0`. Long header values whose
names contain `auth`, `cookie`, or `key` retain the existing masking behavior.
No input preserves the informational `No requests to group` message, including
when JSON was requested. No raw occurrence is changed or removed.

## Migration

`rep group --by FIELD` remains a hidden compatibility spelling with a
deprecation note on stderr. Use `rep group FIELD` instead. Unknown fields used
to fall back silently to path grouping; they now fail. Extra positionals and
mixing `FIELD` with `--by` also fail instead of being ignored.
An empty or whitespace-only `header:` suffix also fails. The field allowlist is
specific to grouping: stats-only fields such as `body-type` and
`path:segment:N` were never supported grouping fields and now report an error
instead of falling back to exact-path grouping.
