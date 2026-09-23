# rep body

Inspect one request's captured bytes. Use the same workspace/task as its capture.
Prefer a full request ID and `--saved SAVED_HASH_ID` to pin an immutable archive.
Unique ID prefixes and semantic labels work; ambiguity fails without a guess.

## Evidence first

`rep body ID --info` returns metadata without content. `body_capture.state` is
`complete`, `partial`, `unavailable`, `pending`, `not_applicable`, or `unknown`
(legacy data). Empty content is not proof of an empty HTTP response. Captured
byte counts and SHA-256 describe decoded payload bytes, not compressed wire size.
`--require-complete` rejects partial, unavailable, pending, and unknown evidence.
It does not reissue a request or reconstruct bytes that were never observed.

## Bounded retrieval

Success output is compact JSON in every mode, including `--head`. The default
`--max-bytes 8192` includes JSON escaping, optional explicit `--envelope`, and
newline. `--raw-json` disables the envelope. `--save` without `-j` is the one
exception: it prints only the private artifact path.

- `--head N --offset M`: select decoded byte ranges. `next_offset` advances only
  by delivered bytes. A range that splits UTF-8 is losslessly returned as base64.
- `--save -j`: save the selected bytes privately and return path, byte count,
  and capture evidence. Binary data is decoded into the actual file bytes.
- `--request`: inspect request-body evidence instead of response-body evidence.
- `--pointer /data/items/0`: select a JSON value using RFC 6901 escaping. The
  parser skips unrelated subtrees and validates the whole document. Invalid or
  unfinished JSON fails explicitly. Numeric IDs retain their original precision.
- `--format ndjson` or `--format sse`: parse application records, with
  `--record-offset N --records 20`. SSE joins multiple data lines, retains event
  IDs, and waits for a blank-line event boundary. Unfinished tails are disclosed
  through `pending_bytes`, never presented as completed records.
- `--format auto`: choose from Content-Type; unknown formats remain raw.
- `--find TEXT`: literal search with byte offsets and bounded context; use
  `--record-offset` and `--records` to page matches.

`body` contains the selected text or base64 bytes; `encoding` says which.
`body_capture` describes the original captured body. `view_complete` describes
only whether the full selected projection was returned. They are independent.
`artifact_sha256` verifies the saved selection; `body_capture.sha256` verifies
the original captured body, which can differ after a pointer/range projection.

Large selections have a private `artifact_path` for complete local retrieval.
`--save` returns `path`. Task artifacts are unique mode-0600 files in a private
`body-exports/` directory. Budgeted output never prints an entire huge body just
to determine whether it fits.

For record pages, advance `selection.next_record` only when
`record_page_complete` is true. If the byte budget cuts a page, next_record is
omitted: read its artifact or continue byte ranges with the same format and
record offset. Do not discard the rest of a partially returned record page.
`selection.source_validated` is not a claim that a still-open network stream
has ended; capture completeness always comes from `body_capture`.

HTTP chunked transfer, CDP data events, and native transport fragments are byte
transport mechanisms. They are not SSE/NDJSON record boundaries. Parsing runs
only after ordered captured bytes have been assembled.
