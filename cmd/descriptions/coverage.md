# rep coverage

Read one task-local capture and report retained evidence and capture gaps as
aggregate JSON. This command performs no browser or network operations.

```sh
rep --workspace demo --task review coverage
rep --workspace demo --task review coverage --saved FULL_HASH
```

`--saved` accepts an exact hash or ID, a unique prefix, or `latest`. Ambiguous
selectors fail. Without it, only the selected task's live capture is read.
Missing live data returns `provenance.source_status: no_capture`; an unreadable
capture or missing archive returns an error. There is no implicit fallback.
`--envelope` adds the usual output wrapper; `--raw-json` selects the bare result.

## Evidence fields

- `provenance` identifies the workspace, task, selected source, capture session,
  and saved ID/hash when applicable.
- `observed` counts retained requests, responses, stream records, page URL
  references and scripts. Counts concern recorded observations.
- `response_bodies` and `request_bodies` aggregate declared states and decoded
  byte counts separately from stored representation bytes. Missing metadata is
  unknown. `payload_validation: not_checked` means this metadata report has not
  decoded payloads or verified their hashes. `declared_empty` is a claim in the
  recorded metadata; verify the selected body before relying on it.
- `streams` reports declared counts, missing/invalid metadata, open connections,
  dropped events, retained gap events and truncated payloads.
- `capture` includes collector limits, known warning codes, drops and unknowns.
  Its state is `no_capture`, `unknown`, `gaps_reported`, or `no_reported_gaps`.
  The last state means no known gap was reported within the observed interval;
  it does not establish that every feature, request, or byte was captured.
- `protocol_evidence` distinguishes browser-reported content, connection
  lifecycle, page-controlled API observations and re-encoded media. A page can
  bypass or forge `page_api` observations, regardless of declared completeness.
- `projection.complete` concerns the report's metadata projection. Omitted
  warning details and protocol groups have explicit counts. All retained
  records are aggregated even when supplemental metadata is omitted.

The report uses fixed metadata categories and bounded group lists. It omits
payloads, headers, URLs and arbitrary warning text. Archive identity strings are
retained verbatim, so the entire response has no fixed byte-budget guarantee.
Inspect individual payloads with `body` or `stream`, pinned to the same archive,
and use `--require-complete` when complete evidence is required.

Unvisited features, traffic before attachment, detached targets and unfinished
streams can remain unseen. Ordinary browser records do not establish original
wire framing. Native diagnostic artifacts require their own evidence review.
Task namespaces isolate data; independent accounts need different browser
profiles. `rep scope -j` reports the available profile choices.
