# rep probe

Task-local experiment ledger and bounded research frontier. These commands plan
and record work; they send no network requests and execute no oracles. Select an
explicit `--workspace` and `--task`, or `REP_WORKSPACE` and `REP_TASK`. The legacy
global store and an implicit `default` task are rejected.

## Commands

```sh
rep probe add --hypothesis H --oracle KIND --ref REQUEST_ID [--precondition P]
rep probe next [--limit N] [--oracle KIND]
rep probe record ID --verdict pass|fail|inconclusive --evidence REF [--note N]
rep probe coverage [--limit N]
rep probe show ID [--limit N]
```

`add` resolves a captured request in the selected task, including unambiguous
aliases. It stores the full resolved request ID, a normalized endpoint family,
the hypothesis, an oracle, and a precondition. The hypothesis must be one
nonempty line of at most 2048 bytes. Preconditions default to an oracle-specific
prerequisite when omitted; their text is a declaration, not an automatic check.

Optional `add --mutation-shape SHAPE --as alice,bob` distinguishes concrete
mutation and identity variants of an experiment. The mutation is a stable
descriptor of the intended change, not an instruction that this command
executes. Describe the field/action and replacement category, such as
`query.owner_id=other`, rather than including credential bytes. Mutation shapes
and preconditions are at most 2048 bytes. At most 16 explicit principals are
accepted. Principal names are case sensitive except reserved `anon`.

To record pending execution or an unmet prerequisite, use:

```sh
rep probe record ID --state attempted --evidence run_ID/op_ID
rep probe record ID --state blocked --evidence run_ID/op_ID --note 'Second principal unavailable.'
```

`--state` and `--verdict` are alternatives. `blocked` requires a note explaining
the failed prerequisite. A later `record` can resolve a blocked, attempted, or
inconclusive probe; no command resets a probe to `untested`.

## Fixed oracle kinds and priority

Priority is a deterministic scheduling heuristic, not measured impact or a
finding confidence score. Within a priority, older proposals come first, with
the content ID breaking timestamp ties.

| Oracle kind | Default mutation shape | Priority |
|---|---|---:|
| `authz_cross_principal` | `cross_principal` | 100 |
| `object_reference` | `substitute_object_reference` | 95 |
| `authz_unauthenticated` | `strip_credentials` | 90 |
| `mutation_side_effect` | `mutate_input` | 80 |
| `information_disclosure` | `inspect_response` | 70 |
| `input_reflection` | `reflect_input` | 60 |
| `rate_or_lockout` | `repeat_request` | 50 |

Unknown kinds are errors. HTTP status 200, a status difference, or a response
byte difference cannot substitute for a named oracle. A meaningful authorization
assessment still needs a named subject, protected resource, owner control, and
the observed access or denial.

## Identity and deduplication

IDs are `probe_` followed by the full SHA-256 of canonical JSON containing
`endpoint_family`, `mutation_shape`, `identity_shape`, and `oracle_kind`.

- Endpoint families include the uppercase HTTP method, scheme, lowercase host,
  nondefault port, normalized path, and sorted query parameter names.
- Numeric, UUID, long hexadecimal, and recognizable long opaque path segments
  become `{id}`. Other path segments, case, and trailing slashes are preserved.
  Path normalization is structural; it cannot infer an application's route schema.
- URL user information, fragment, and query values are excluded. Captured bodies,
  headers, cookies, response status, capture IDs, prose, and timestamps never
  contribute to the content ID. Query parameter names remain distinct inputs.
- Mutation descriptor whitespace is normalized. Explicit principal names are
  trimmed, sorted, and deduplicated. An empty identity shape denotes the default
  oracle experiment; explicit names declare a distinct principal-specific variant.
- Request IDs, hypothesis wording, and precondition wording cannot bypass dedupe.
  Re-adding an existing tuple returns `created: false` and its original proposal
  with the current state, without appending or resetting anything.

Use the same stable mutation descriptor when proposing the same experiment.
Natural-language synonyms cannot be proven equivalent by this structural key.

## Frontier and termination

`next` defaults to 5 probes and accepts limits 1–100. It returns only `untested`
probes. It is a view, not a dequeue: repeated calls without ledger changes return
the same batch. Recording any verdict, attempt, or block removes that probe from
subsequent `next` views, including `inconclusive` results. Re-adding it cannot
reopen it. Resolve uncertainty by recording follow-up evidence on the same ID,
or deliberately plan a materially different experiment shape.

`matching_untested`, `total_untested`, and `omitted` distinguish a filtered-empty
batch from global completion. `frontier_empty` always describes the whole task,
not just the selected oracle. An empty frontier means there is no untested work
left in the recorded plan. It does **not** establish that the target is secure.
Blocked, attempted, and inconclusive work remains separately visible. An empty
ledger says explicitly that no probes have been planned.

`coverage` counts every state, including zero counts, both across the task and
within every oracle kind. It also lists the remaining untested probes in priority
order. Its default listing limit is 20, maximum 100; counts always cover the full
ledger and `untested_omitted` accounts for the rest.

## Verdict and evidence semantics

| Record | Current state | Meaning |
|---|---|---|
| `pass` | `verified` | Caller reports that the named oracle supports the hypothesis |
| `fail` | `refuted` | Caller reports a valid test that did not support the hypothesis |
| `inconclusive` | `inconclusive` | Evidence cannot decide the hypothesis |
| `--state attempted` | `attempted` | An attempt exists without a settled verdict |
| `--state blocked` | `blocked` | A prerequisite is unmet; this is not a refutation |

Every record retains its oracle kind, verdict/state, timestamp, note, and evidence
references. `record` is caller adjudication, so it always stores
`assessment: "model_assessed"`. It does not run a verifier, validate referenced
evidence contents, emit a finding, or assert `executed_and_observed`. `verified`
is the reported frontier state and must be read together with that assessment
basis. Inconclusive is never rounded toward pass or fail.

`--evidence` is required and repeatable, up to 16 references. Accepted forms are
run or operation identifiers starting with `run`, `op`, or `operation` followed
by `_`, `:`, or `-`, or a `run_ID/op_ID` pair. The existing evidence system's
`run_<32 hex>` and `op_<32 hex>` IDs work directly. Each ID suffix is at most 128
ASCII letters, digits, `.`, `_`, `:`, or `-`. The command validates the pointer
syntax, not the referenced run's existence or evidentiary completeness. URLs,
filesystem paths, request IDs, JSON bodies, and free-text responses are not
evidence pointers. Notes are at most 2048 bytes; use them for interpretation,
not copied response bodies.

## Storage, history, and output

The task directory returned by `store.GetStorePath()` holds `probes.jsonl`, mode
0600. One JSONL event records each new proposal or subsequent assessment.
Duplicate adds append nothing; repeated `record` invocations retain separate
occurrences. Latest state is a projection over the immutable history, and all
evidence pointers remain attached to the probe.

Writers serialize using a separate `probes.jsonl.lock`, preserve the prior JSONL
bytes verbatim, append into a private temporary file, sync, and atomically rename
it. Readers see a complete old or new snapshot. The logical log is append-only;
this simple implementation scans the ledger and copies it on each append.
Corrupt, unknown-version, oversized, or incomplete records fail closed instead
of presenting an empty frontier or silently discarding evidence. A single event
is bounded to 64 KiB. Read-only views create no ledger or lock files.

`show` includes the current probe plus the latest 20 immutable events by default,
up to 100 with `--limit`. `total_events` and `omitted_events` account for older
history, which remains in the task's JSONL file.

JSON output uses the standard envelope: `source: "probes.jsonl"`, command,
workspace/task and active filters, data, optional truncation counts, and runnable
`suggest` commands. Text output emits `Suggest:` lines. Suggestions preserve the
task selection and quote shell arguments. Differential suggestions use bound
principal names, or `rep identity list` when prerequisites are unavailable;
baseline replays use `--baseline 5`. They prepare observations for the named
oracle and do not themselves constitute a verdict. For example:

```sh
rep --workspace research --task profiles replay h_request --baseline 5 --as alice,bob
rep --workspace research --task profiles probe next --limit 5
```

Error codes include `invalid_argument`, `scope_required`, `request_not_found`,
`probe_not_found`, `store_read_failed`, and `store_write_failed`. Bad inputs,
unknown oracle kinds, invalid limits, and malformed evidence pointers exit
nonzero. No command falls back to another task or the global store.

## Advanced flags

These flags stay accepted with their existing behavior and are hidden from `--help` to keep the everyday surface small: `add --precondition` (prerequisite for an interpretable result) and `--mutation-shape` (stable experiment descriptor).
