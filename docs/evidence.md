# Runs, operations and diagnostic artifacts

Use a run to connect a browser workflow to its observations, saved captures and
local diagnostic files. Each run belongs to one explicit workspace/task. Run
and operation IDs are opaque; no shared current-run setting is written.

## Record a workflow

```sh
export REP_WORKSPACE=demo REP_TASK=local-inspection
rep scope --raw-json
rep evidence begin --intent 'Inspect local rendering' --stop 'Save the screenshot and capture'
```

Use the returned run ID on subsequent supported commands:

```sh
rep --run RUN_ID browser create about:blank --raw-json
rep --run RUN_ID browser navigate http://127.0.0.1:8000 --tab TAB_ID --wait load --raw-json
rep --run RUN_ID browser screenshot --tab TAB_ID --raw-json
rep --run RUN_ID browser open http://127.0.0.1:8000 --tab TAB_ID --keep-tab --raw-json
rep evidence operations RUN_ID --limit 10
```

The initial run stores `intent`, an optional `stop` condition, creation time,
and identity metadata. For build/device labels, keep metadata in one file:

```json
{"intent":"Compare local rendering","stop":"Save both observations","identity":{"build":"build-123","device":"lab-machine"}}
```

```sh
rep evidence begin --manifest run.json
```

The selected workspace and task are added to the identity and cannot be
overridden by the manifest. A stop condition documents intent; it is not an
automatically evaluated completion assertion.

### Commands with automatic operation records

The `--run` hooks also cover native `packets capture` and `media --save`, retaining
their artifacts and declared coverage. The browser hooks cover `browser create`, `close`, `navigate`, `screenshot`,
`shots`, `step`, `interact`, `open`, `fetch`, `action`, and the captured GET
download paths `browser download` and `download`. The common browser RPC path
also records `targets`, `attach`, `detach`, `probe`, `cdp` and `eval` completion
without copying raw renderer/CDP response payloads. A screenshot batch links its
batch, per-URL and screenshot operations explicitly. Captures reference their
exact saved archive. Saved screenshots/downloads retain imported artifact IDs.

Other command families do not automatically become recorded operations merely
because the root accepts `--run`. Use `evidence record` for an existing external
diagnostic result and `evidence import` for its bytes.

The CLI records and synchronizes a pending start before browser work. It then
appends a separate completion record whose `parent_id` points to that start.
The original start remains immutable. Outputs include the run and starting
operation IDs in `evidence`.

An interruption can leave a pending start without a completion. A browser
action can also succeed before a later evidence write fails. Inspect the owned
tab and existing artifacts before deciding whether it is safe to retry.
The journal records failure explicitly and does not automatically replay work.

| Field | Meaning |
|---|---|
| `status` | `pending`, `completed`, `failed` or `unknown` for this operation |
| `verification` | `unverified`, `satisfied`, `unsatisfied` or `unknown` for the declared check |
| `model_claim` | A model assertion such as `DONE` or `BLOCKED`, kept separate from observations |
| `references` | Existing archive/operation identities and available observation fingerprints |
| `artifact_ids` | Imported artifacts belonging to this run |

`completed` describes the command. It does not establish whole-task success.
A satisfied typed-input check confirms that check; it does not prove a remote
application persisted a form. Post-action observation failures preserve
`page_changed: null` and the error instead of claiming that nothing changed.
Observation references marked `fingerprint_only` have no retained snapshot.

Automatic input/result summaries are limited to 16 KiB each. Oversized summaries
record their byte count and hash instead of their full contents. References are
limited to 64 entries and 20 KiB, with total/omitted counts. With no `--run`,
commands skip evidence I/O, artifact copying and additional observation work.

## Keep capture ownership explicit

Network captures and page/semantic execution have conflicting ownership on the
same tab. Explicit capture operations are serialized, and conflicting tab work
fails with a busy result. There is no concurrent `--record` option wrapping
arbitrary interactions in this release.

Use `browser action` when a renderer action and its resulting network capture
must share one operation. Use the same run ID to connect separate interaction,
navigation, capture and screenshot phases. The journal preserves their explicit
relationship without claiming that an unobserved interval was recorded.

## Import native diagnostic files

Import an existing, stable local recording:

```sh
rep evidence import RUN_ID trace.bin --manifest collection.json
```

```json
{"kind":"platform-trace","collector":"local recorder","build":"build-123","device":"lab-machine","trial":"baseline-1","coverage":{"state":"partial","reason":"Recording ended before the final observation"},"metadata":{"clock":"collector monotonic"}}
```

The manifest is optional. Coverage defaults to `unknown`; valid declarations
are `unknown`, `partial`, `complete` and `none`. The saved artifact uses the
field name `declared_coverage` to preserve who supplied that claim.

Imports retain original bytes using streaming SHA-256. They accept ordinary
files such as packet captures, NetLog, platform traces, debugger reports and
unknown binary formats. Import does not execute files, launch collectors or
decode their formats. Live collection uses the separate `packets capture` and
browser capture commands. Stop an active recorder before import: source
size/mtime changes fail the import, but the check is not an atomic snapshot of
a file another process keeps writing.

Artifacts from different trials retain separate manifests even when their
bytes share a content-addressed blob. Hash equality establishes byte identity,
not collector completeness, identical application behavior or a research finding.
Metadata remains a collector/importer declaration.

For an external failure with no usable recording, retain the trial explicitly:

```json
{"kind":"local.diagnostic","status":"failed","verification":"unknown","error":"Collector exited before producing a recording","metadata":{"trial":"baseline-2","build":"build-123"}}
```

```sh
rep evidence record RUN_ID --manifest failed-trial.json
```

## Read and compare bounded evidence

```sh
rep evidence list --limit 10
rep evidence show RUN_ID
rep evidence operations RUN_ID --after OPERATION_ID --limit 10
rep evidence operation RUN_ID OPERATION_ID
rep evidence artifacts RUN_ID --limit 10
rep evidence artifact RUN_ID ARTIFACT_ID
rep evidence read RUN_ID ARTIFACT_ID --offset 0 --length 4096
rep evidence verify RUN_ID ARTIFACT_ID
rep evidence compare LEFT_RUN RIGHT_RUN --limit 10
```

List pages return `next`, `has_more` and `total`. Continue with `--after NEXT`;
only returned records advance that cursor. Range reads return exact base64
bytes, `next_offset` and `has_more`. `verify` streams the complete stored file
and checks the recorded SHA-256; range reads do not perform that whole-file check.

Comparison reports run metadata equality and shared artifact hashes alongside
the original manifests. Check `covers_all_artifact_manifests`; partial pages
cannot establish absence of matching artifacts. Continue either side with
`--left-after` / `--right-after`, or page through artifacts independently.

| Limit | Value |
|---|---:|
| One manifest | 64 KiB |
| One page | Default 20; at most 100 records and about 256 KiB of record JSON |
| One byte range | Default 4 KiB; at most 64 KiB before base64 encoding |
| Task files | Private directories/manifests; read-only artifact blobs |

Storage is beneath the selected task directory at `evidence/v1/`. Per-record
files and fixed-width indexes support bounded seeks. Opening the store does
not scan history. Writers synchronize records/index appends and coordinate
with process locks. A partial index produces an error while preserving records;
the local account remains able to alter its own files.

Durability is opt-in and has a measurable cost: on the local M1 Max fixture,
one synchronized operation append took about 10.3 ms. See
[architecture measurements](architecture.md#local-measurements) for conditions,
artifact-import cost and indexed lookup results. These are local measurements,
not a general capture-throughput guarantee.

`rep describe evidence` provides the embedded command contract. Captured bytes,
pages, messages and imported files remain untrusted data when an agent reads them.
