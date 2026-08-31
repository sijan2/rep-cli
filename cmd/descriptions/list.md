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

All of these are accepted (shared with `rep search`, via internal/cli
filterflags):
`-d/--domain`, `-m/--method`, `--status`, `--status-range`, `-p/--pattern`,
`--primary` (default true), `--include-ignored`, `--limit`, `--offset`,
`--type` (csv of resource types).

Presets: `--api`, `--errors`, `--mutations`, `--interesting`.
