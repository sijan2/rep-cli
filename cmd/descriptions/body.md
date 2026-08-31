# rep body

Retrieve the response body for a single request.

## Input

`<request-id>`: full `h_xxx...`, the `[xxxx_METHOD_STATUS]` label,
or any ≥4-character prefix. Also accepts semantic IDs.

## Output

Plain text by default; `-j` or `--output json` wraps in JSON.

## Overflow to disk

Bodies larger than {{.OverflowThreshold}} bytes are spilled to a temp
file to keep agent context small. The command prints:
  - The first {{.OverflowPreviewSize}} bytes inline.
  - A pointer: `full body spilled to /tmp/rep-body-h_xxx.json`.
  - A recovery hint: `rep body h_xxx --head N --offset M`.

In JSON mode, the payload includes a `truncation` object with
`reason="overflow-to-disk"`, `returned`, `total`, and `overflow_path`.
The agent should prefer reading the spill file directly (it already
exists on disk) over re-invoking `rep body`.

## Flags

- `--head N`: keep only the first N bytes inline.
- `--save`: write full body to temp and print only the path.
- `--request` / `-r`: return request body instead of response body.
