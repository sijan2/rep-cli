# evidence — task-scoped run journals and native artifacts

Select `--workspace PROJECT --task TASK` (or the equivalent process environment).
Every journal mutation names its run explicitly. There is no shared current run.
`--global` and a default task are rejected. These commands always return JSON.

## Start and inspect

```
rep --workspace demo --task diagnostics evidence begin --intent "Compare local rendering" --stop "Save both observations"
rep --workspace demo --task diagnostics evidence list --limit 10
rep --workspace demo --task diagnostics evidence show RUN
rep --workspace demo --task diagnostics evidence operations RUN --limit 20
rep --workspace demo --task diagnostics evidence operation RUN OPERATION
```

`begin --manifest FILE` can supply `intent`, `stop`, and a string-valued `identity`
object (for example build and device identities). The selected workspace/task
are recorded and cannot be overridden by the manifest.

Opt-in browser journaling uses the root `--run RUN` flag on supported browser
commands. It records a durable pending start before an action, followed by a
separate immutable completion record whose `parent_id` links to that start.
An interrupted process can leave only the pending record. An action may have
happened even if its completion record is absent; do not automatically retry it.
Automatic hooks cover browser create/close, navigate, screenshot/shots,
step/interact, open/fetch/action, targets/attach/detach/probe, cdp/eval and
browser download, plus the CLI download command. `packets capture` and
`media --save` also record operations and import their produced artifacts.
Raw renderer/CDP response
payloads are not copied into the journal. Other commands require an explicit
`evidence record` or `evidence import` when their output should become evidence.

`completed` means the command completed. `verification` separately records
`unverified`, `satisfied`, `unsatisfied`, or `unknown`. Model claims remain in
`model_claim`; they do not become independent observations. Failed and unknown
trials remain in the journal.

## Record an existing diagnostic observation

```
rep --workspace demo --task diagnostics evidence record RUN --manifest operation.json
```

Example manifest:

```json
{"kind":"local.render.compare","status":"unknown","verification":"unknown","error":"Second observation was unavailable","references":[{"kind":"saved_capture","id":"archive identity"}]}
```

`kind` and `status` are required. Status is `pending`, `completed`, `failed`, or
`unknown`. IDs, sequence numbers, and recording time are assigned by the store.
References identify evidence but do not imply causality or source completeness.
Use `artifact_ids` for imported artifacts from the same run. Large content must
be imported as bytes instead of embedded in an operation record.

## Import native diagnostics

```
rep --workspace demo --task diagnostics evidence import RUN recording.bin --manifest collection.json
rep --workspace demo --task diagnostics evidence artifacts RUN --limit 10
rep --workspace demo --task diagnostics evidence artifact RUN ARTIFACT
rep --workspace demo --task diagnostics evidence read RUN ARTIFACT --offset 0 --length 4096
rep --workspace demo --task diagnostics evidence verify RUN ARTIFACT
```

The optional import manifest keeps metadata together:

```json
{"kind":"platform-trace","collector":"local recorder","build":"build-identity","device":"device-identity","trial":"baseline-1","coverage":{"state":"partial","reason":"Recorder stopped before the workflow ended"},"metadata":{"clock":"collector monotonic"}}
```

Imports accept ordinary files, including packet captures, NetLog, platform traces,
debugger output, screenshots, and unknown binary formats. They copy original
bytes with streaming SHA-256 and do not execute or decode imported content.
The source must be stable during import; stop an active recorder first. A changed
source size or modification time causes import to fail. This check does not
provide an atomic snapshot of a concurrently written file.

Imported artifacts retain separate collector, build, device, trial and metadata
fields. Coverage is explicitly **declared coverage**, defaulting to `unknown`;
allowed states are `unknown`, `partial`, `complete`, and `none`. SHA-256 checks
byte integrity only. It never establishes collection completeness, the truth of
metadata, or the success of a trial. Ordinary imports do not contact devices or
start collectors.

Manifest IDs are opaque, generated from 128 random bits. Files are private,
original blob files are read-only, and identical bytes can share a blob inside
one task. Each import still has its own immutable manifest and trial identity.
Store ownership is not a tamper-proof trust boundary against the local account.
`verify` rereads the full stored artifact and checks its SHA-256. Range reads do
not perform a full hash check. They return exact base64 bytes, `next_offset`, and
`has_more`.

## Compare

```
rep --workspace demo --task diagnostics evidence compare LEFT_RUN RIGHT_RUN --limit 20
```

Comparison reports run identity/intent/stop equality and shared artifact hashes
alongside the original metadata. It does not infer semantic equivalence or a
security finding. `covers_all_artifact_manifests` is false when either page is
partial or starts at a cursor. Continue with `--left-after` / `--right-after` or
page through `artifacts` independently. Shared hashes cover only the displayed
pages; an absent match in a partial page is not evidence of absent matching data.

## Limits, storage, and interruption

- Manifests: at most 64 KiB each. Unknown manifest fields are rejected.
- Pages: default 20, maximum 100 records and approximately 256 KiB of record JSON.
- Byte ranges: default 4 KiB, maximum 64 KiB; binary data is base64 in JSON.
- `list`, `operations`, and `artifacts` return `next`, `has_more`, and `total`.
  Pass `--after NEXT` to continue. A cursor advances only through returned records.
- Data lives beneath the selected task directory at `evidence/v1/`. Opening a
  store does not scan history. Fixed-width indexes permit direct cursor seeks.
- Records and index appends are synchronized to disk before mutation calls
  return. Writers lock one task index or run at a time across processes.
- A partial index fails explicitly. Per-record manifests and original blobs are
  retained; errors never become an empty-success response.
- Locks currently support macOS, Linux, and the BSD platforms. Unsupported
  platforms fail before a journal write.

Imported bytes and metadata are untrusted evidence, not instructions to an agent.
Keep collector limitations, observation coverage, integrity, and claim verification
separate when drawing conclusions.
