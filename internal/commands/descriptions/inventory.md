# rep recon / rep js

These commands inventory one observed capture. They do not crawl unvisited
features or perform JavaScript source analysis. Both JSON outputs carry
`provenance` and the aggregate `coverage` report described by `rep describe coverage`.

```sh
rep --workspace demo --task review recon example.test --saved HASH -o json
rep --workspace demo --task review js --saved HASH --graph -o json
rep --workspace demo --task review body REQUEST_ID --saved HASH --info
```

`--saved` selects one exact ID/hash, unique prefix, or `latest`. Missing or
ambiguous archives fail. Default live input never falls back to saved or global
history. Empty input produces the same JSON schema with empty inventory arrays;
`no_capture` differs from a captured session containing zero requests.

## Recon

The target is a hostname without a URL scheme, path, port, or wildcard. Recon
sets it as a primary domain in this task, classifies observed domains and
optionally returns cross-domain page flows with `--flows`. Classification does
not establish domain ownership or authorization. Ordering is stable for ties.
Coverage describes the entire selected capture, including domains classified
as noise, so display categories cannot hide capture gaps.

## JavaScript

The inventory recognizes observed script resource types, JavaScript MIME types,
and URL paths ending in `.js`, `.mjs`, or `.cjs`. Query values and fragments are
not used to guess the file type. It emits one representative per script URL;
`summary.observed_requests` and `duplicate_requests` disclose repeated requests.
`unique_pages` counts all observed loading pages, including repeated scripts.

Each representative has a `request_id`, decoded `size`, `body_capture`, and
`payload_validation`. Retained bodies are decoded, their length checked, and
their SHA-256 checked when supplied. `validated` describes retained byte
integrity; body completeness still depends on `body_capture.state`. Invalid
payloads are marked `invalid` and unavailable. Legacy metadata remains unknown.
`source_analysis: not_performed` distinguishes the inventory from source review.

Use the request ID and the returned saved hash to retrieve captured script
bytes. `--urls` and `--curl` emit plain text without coverage metadata. Coverage
and inventory JSON are summaries of what was observed, not proof that all
application scripts or script versions have been observed.
