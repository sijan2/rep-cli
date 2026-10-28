# rep list

List captured HTTP requests, filtered for agent consumption.

## Source

- Default: `live.json` (same as rep+ extension).
- `--saved <id>`: a persisted session (prefix or `latest`).

## Output modes (`-o`)

- `compact` (default): truncated response bodies ≤ {{.MaxBodySize}} bytes.
- `meta`: headers only. No request or response body.
- `full`: complete bodies.
- `json` / `-j`: structured data; combine with `--envelope` for agent metadata.

## ID format

The `[xxxx_METHOD_STATUS]` label shown in list output is a display alias.
Downstream commands (`rep body`, `rep detail`, `rep curl`) accept any
≥4-character prefix of the full `h_xxx...` ID.

## Empty result

Text mode returns a block with `source:`, `filters:`, `result:`, and `suggest:`
keys. JSON mode returns one JSON envelope with an empty `data` array, active
`filters`, and `suggest`; missing-primary and no-match paths never fall back to
plain text. Exit code is 0 for an empty result. A missing saved session is a
structured `session_not_found` error.

## Truncation

When `--limit` is set and more results exist, a footer prints:
`[Showing A-B of N requests. Next: --offset=X --limit=Y]`.
In `--envelope` mode, the JSON object has a `truncation` field with
`reason`, `returned`, `total`, and a `suggest` array.

## Filter grammar

List accepts these filter flags. Common categories share registration with
`rep search`; command-specific defaults and URL-pattern spellings are retained:
`-d/--domain`, `-m/--method`, `--status`, `--status-range`, `-p/--pattern`,
`--primary` (default true), `--include-ignored`, `--limit`, `--offset`,
`--type` (csv of resource types).

Use `--select PRESET[,PRESET...]` (also repeatable). Presets are intersected;
all other explicit filters still apply:

- `api`: xmlhttprequest/fetch resource types (overrides --type as the old --api did).
- `errors`: 4xx/5xx responses.
- `mutations`: POST/PUT/DELETE/PATCH, unless an explicit --method supplies methods.
- `interesting`: errors with mutation methods, unless --method supplies methods.
  This preserves the old implementation's intersection; it is not an OR filter.

`--view line|detail` selects a text layout for compact output. Line is already
the default; no flag is needed. The existing `--output meta/full` projections
use multiline rendering even with --view line; JSON ignores the text layout.
`--budget` retains minimal/compact/standard/full and defaults
to compact. `--since` retains its duration filter.

Hidden legacy flags `--api`, `--errors`, `--mutations`, `--interesting`, `--line`,
and `--detail` remain accepted, including explicit false values, and emit a
deprecation note on stderr. Legacy combinations preserve their old precedence;
`--line=false` selects the multiline path and `--detail` wins over --line.
Do not mix --select with legacy presets, or --view with legacy layout flags.
Unknown/empty presets, unknown views, and mixed syntaxes return invalid_argument.

## Advanced flags

These flags stay accepted with their existing behavior and are hidden from `--help` to keep the everyday surface small: `--budget` (minimal, compact, standard, full token budget per request) and `--include-ignored` (show requests to ignored domains).
