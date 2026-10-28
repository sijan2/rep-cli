# rep browser element

Observe, select, and validate page elements through one shared observation scope.
These commands do not execute a selected element. `select` and `select-batch`
return model-assessed choices bound to browser evidence; `observe` and `validate`
read fresh browser evidence without model inference. Use `browser interact` for
explicit, guarded actions. A selected handle is not proof that an action ran or
that an application outcome succeeded.

```sh
rep --workspace research --task form-review browser element observe --tab 12
rep browser --browser headless element --tab 12 select "Save button"
rep browser element select-batch goals.json --tab 12 --frame frame-id
rep browser element validate --tab 12 --fingerprint HASH --generation ID
```

Task identity still comes from root `--workspace`/`--task` flags or their process
environment. `--global` remains an explicit legacy-data choice; no command here
changes the selected task or establishes account isolation. The selected owner
is passed unchanged to the host. Use a task-owned tab; extension profiles share
cookies and tabs. `--browser headless` selects the task's private persistent
profile and does not start it automatically.

## Shared observation scope

These flags are registered once on `browser element` and inherited by all four
verbs. They may appear before or after the verb. `--browser` is inherited from
`browser` and may also appear before `element`.

| Flag | Default | Contract |
| --- | --- | --- |
| `--tab ID` | required | Nonnegative existing browser tab ID; explicit selection is required. |
| `--browser NAME` | `arc` | `arc`, `chrome`, `any`, or task-owned `headless`. |
| `--kind KIND` | `controls` | Candidate roles: `controls`, `text`, or `all`. |
| `--origin ORIGIN` | unset | Require the top-frame origin and skip frames outside it. |
| `--frame ID` | unset | Restrict the observation to a frame ID. |
| `--frame-url URL` | unset | Restrict to the unique frame at this exact URL. |
| `--root-node ID` | `0` | Restrict candidates to a backend DOM node subtree. A positive ID requires `--frame` or `--frame-url`; negative IDs are rejected. |

Frame ID and URL can be supplied together to retain the existing host scope
checks. Element scope and data scope are distinct: choosing a frame does not
change workspace/task ownership.

## Selection

`select [goal] --tab ID` accepts one positional goal or `--goal TEXT`, never both.
A goal contains 1–2000 bytes. `select-batch goals.json --tab ID` reads one version
1 JSON object, at most 128 KiB, with 1–32 independent goals:

```json
{"version":1,"goals":[{"id":"save","goal":"Save button"},{"id":"email","goal":"Email field"}]}
```

Each goal ID is unique, nonempty, and at most 128 bytes. Unknown JSON fields and
trailing documents are rejected. Results retain goal order and IDs. Selection
rechecks browser evidence and returns a decision or explicit no-match/uncertain
result; it never clicks, navigates, or fills fields. Only bounded semantic text
is sent to Jev, while URLs, node handles, and credentials remain local.

Both selection verbs retain these local flags:

| Flag | Default | Contract |
| --- | --- | --- |
| `--limit N` | `240` | Candidate budget per goal, 1–480; the existing zero-as-default interpretation is preserved. |
| `--confidence N` | `0.8` | Minimum decision confidence, 0–1. |
| `--no-cache` | false | Bypass decision reuse. |
| `--strategy MODE` | `auto` | `auto` or the `legacy` packing comparison baseline. |
| `--observation MODE` | `auto` | `auto` or the `legacy` observation comparison baseline. |

`--goal` applies only to `select`. Existing advanced-flag help visibility is
retained; this contract lists all flags. Browser/host capability negotiation
still precedes sending private credentials to the local host.

## Observation and validation

`observe --tab ID` reads a compact relational observation. Optional
`--since FINGERPRINT` asks for changes from a retained observation fingerprint.
`validate --tab ID --fingerprint HASH --generation ID` refreshes evidence and
checks the earlier semantic observation; both identity flags are required.
Validation does not call the model or execute an action.

Both verbs use `--limit N` (default `64`, range 1–4096) for the returned candidate
projection. This differs from the selection candidate budget, so it deliberately
remains local. The full observation fingerprint is retained even when output is
bounded. Coverage reports returned counts and `output_truncated`. A truncated
delta retains its base/new fingerprint but reports `truncated: true` and
`can_advance: false`; do not advance a cursor past omitted changes.

## Compatibility

`browser select`, `browser select-batch`, `browser observe`, and
`browser validate` remain callable with all previous flags and defaults as
hidden compatibility commands. `jev select` also remains callable. Each emits a
deprecation note on stderr with the corresponding `browser element` spelling;
stdout remains one JSON document. Legacy output command identities remain
unchanged. Canonical commands report their new command path, with the existing
envelope, data shape, and stable error codes. There are no removed capabilities
or hard breaks in this family.
