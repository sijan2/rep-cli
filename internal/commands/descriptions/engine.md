# rep engine — typed model assessments

`engine` supplies bounded proposal and adjudication primitives. It runs no
probes and writes no findings, observed evidence, or proposal ledger entries.
Every returned assessment and every triage proposal carries
`assessment: "model_assessed"`, `engine`, `model`, and a UTC `timestamp` supplied
by the runtime. A model cannot choose or override these tags.

```sh
rep engine list -j
rep engine check -j
rep engine run --job triage --input @families.json --engine codex --model gpt-6.1-sol -j
rep engine run --job triage --input @families.json --engine codex --model gpt-6-astra --effort high -j
rep engine run --job adjudicate --input @executed.json --engine jev -j
```

`list` and `check` inspect local availability only: executable discovery for
Codex and the existing Jev configuration loader. Neither invokes a model,
checks remote authentication, or fails because an engine is unavailable.
Their data includes `model_call: false`, model identifiers, supported tasks,
and `constrained_choice` / `freeform` capability flags.

| Engine | Models | Tasks | Capabilities |
|---|---|---|---|
| `codex` | `gpt-6-astra`, `gpt-6.1-sol` (default), `gpt-6-luna` | triage, adjudicate | freeform plus validated fixed-choice output |
| `jev` | configured `JEV_MODEL`, otherwise `jev-latest`; `--model` can pin a Jev version | adjudicate | constrained choice |

The default engine is `codex` for triage and `jev` for adjudicate. Repeating
triage with different models permits independent proposal sets for comparison;
the command does not run an agent loop or automatically combine assessments.

## Triage input and output

`--input` requires an `@file` containing one JSON object. There is no free-prompt
flag, inline prompt, or implicit capture loading. A family input looks like:

```json
{
  "families": [
    {
      "family_id": "orders-family",
      "method": "GET",
      "route": "/api/orders/{id}",
      "content_type": "application/json",
      "resource_type": "fetch",
      "status_codes": [200, 403],
      "sample_count": 4,
      "principals": ["alice", "bob"],
      "signals": ["object_reference", "auth_present"]
    }
  ]
}
```

Supply 1–32 unique families. Each requires `family_id`, a recognized HTTP
`method`, exactly one of `route` or an HTTP(S) `url`, 1–16 HTTP `status_codes`,
and positive `sample_count`. Content/resource types and up to 16 principal
references are optional. `signals` is an optional array of at most 16 entries
from this fixed set:

`object_reference`, `state_changing`, `auth_present`,
`unauthenticated_capture`, `user_controlled_input`, `rate_limit_observed`,
`sensitive_resource`, `missing_csrf`, `input_reflected`.

A successful result contains up to 10 ranked `proposals`, ordered from rank 1.
Each has the original `family_id`, `oracle`, a `hypothesis`, a `next_test`, and
its own assessment provenance. Hypothesis and next-test text are at most 1024
bytes each. These are suggestions for a discriminating experiment.

The oracle enum matches `rep probe`:

`authz_cross_principal`, `authz_unauthenticated`, `object_reference`,
`input_reflection`, `mutation_side_effect`, `rate_or_lockout`,
`information_disclosure`.

## Adjudication input and output

Copy the selected triage proposal into `proposal` and supply executed summaries:

```json
{
  "proposal": {
    "assessment": "model_assessed",
    "engine": "codex",
    "model": "gpt-6.1-sol",
    "family_id": "orders-family",
    "oracle": "authz_cross_principal"
  },
  "evidence": [
    {
      "id": "run_example/op_comparison",
      "assessment": "executed_and_observed",
      "subject": "bob",
      "resource": "seeded-order-1",
      "status_deltas": [{"baseline": 403, "observed": 200}],
      "stable_field_changes": [{"field": "/owner_id", "change": "different"}],
      "volatility_mask": ["/timestamp", "/request_id"],
      "principal_comparisons": [
        {
          "baseline_principal": "alice",
          "candidate_principal": "bob",
          "same_resource": true,
          "candidate_authorized": false,
          "access": "allowed"
        }
      ]
    }
  ]
}
```

`proposal.engine` and a fixed-enum `proposal.oracle` are required. A missing
proposer engine or an adjudicator equal to the proposer engine is refused with
`engine_independence_required`, before any model call. Engine names are compared
after trimming and lowercasing. A different Codex model is still the **same
engine** and does not satisfy independence. Proposal provenance is supplied by
the caller; the command does not authenticate or invent missing provenance.

Supply 1–16 evidence summaries with unique IDs, explicit
`assessment: "executed_and_observed"`, named subjects, and named resources.
Each needs at least one observation, with bounds of 16 status deltas, 32 stable
field changes, 64 volatility-mask pointers, and 16 principal comparisons.

- Status codes are integers in 100–599.
- Field names and masks are JSON pointers of at most 256 bytes. Changes are
  `equal`, `different`, `added`, or `removed`; raw before/after values are forbidden.
- A field overlapping a volatility-mask pointer, including an ancestor or
  descendant, is excluded from stable changes. The empty pointer masks the root.
- Both comparison booleans must be explicit. Access is `allowed`, `denied`, or
  `unknown`. Principal and resource references preserve equality after aliasing.
- Reference strings are at most 160 bytes: ASCII alphanumerics followed by
  alphanumerics or `_ . / : # -`. References are identifiers, not inline data.

Output `verdict` is exactly `real`, `not_real`, or `needs_more_evidence`, with
`citations` selected from the submitted evidence IDs. Every valid model answer
must cite at least one supplied summary. Jev selects the verdict and each
citation with fixed choices through the existing client; its confidence and
probabilities are retained. Verdict confidence below 0.8 requires more
evidence; citations below that threshold are excluded. Codex output is checked
against the same enum and citation constraints.

Status deltas alone cannot produce a conclusive verdict: cited evidence must
also contain a stable-field observation or known access to the same resource
in a principal comparison. The judge never receives the proposer's narrative,
engine, model, or confidence. `needs_review` remains true for all model
assessments. No verdict verifies the underlying submitted evidence or changes
its storage. A `not_real` assessment addresses this experiment only.

## Sanitization and budgets

Route metadata uses `internal/jev.SanitizeTraffic`: allowlisted method/type and
path words, with unknown path values replaced. Hostnames, URL credentials,
query values, and fragments are omitted entirely. Local family, evidence,
principal, resource, and field references are replaced with aliases before
inference; family IDs and citations are mapped back in the result.

All supplied headers, cookies, credential fields, and raw bodies are dropped
before inference. The result's `sanitization` includes counts and explicit
warnings when these are supplied, without printing their values. Other unknown
fields and unsupported typed values are rejected; input prose is not a prompt.

| Bound | Default | Maximum |
|---|---:|---:|
| `--timeout` | 60s | 2m |
| `--max-input-bytes` | 65536 | 65536 |
| `--max-output-bytes` | 32768 | 65536 |
| `--effort` (Codex) | medium | low, medium, high, xhigh, max |

The input cap covers the file, sanitized state, and full typed provider prompt
or Jev request (including instructions/questions). The output cap applies to
the model answer, before adding runtime metadata. Codex stderr has a separate
64 KiB cap and is never printed. Jev retains the existing client's 1 MiB HTTP
response cap and at-most-30s provider deadline; the chosen engine deadline and
answer cap can tighten these. Existing Jev retries share that deadline.

Codex runs non-interactively via `codex-unm exec`, with a read-only sandbox,
priority tier, explicit model/effort, no approvals, a private temporary working
directory, and a JSON output schema. User config, execution rules, project
instructions, shell tools, web search, apps, and multi-agent features are
disabled. The prompt is sent on stdin. Temporary files are private and removed
after the call; Unix process groups are terminated on completion or timeout.

Timeouts, provider failures, oversized answers, malformed JSON, unknown enum
values, invalid ranks, unknown family IDs, or missing/invented citations return
a tagged `needs_more_evidence` result with a machine-readable `reason`.
Malformed answers produce no proposals or invented citations. Invalid input,
unsupported task/model, unavailable engines, and independence violations are
structured command errors. `--raw-json` selects the result directly; normal
non-TTY JSON uses the standard envelope with the selected model and limits.

## Advanced flags

These flags stay accepted with their existing behavior and are hidden from `--help` to keep the everyday surface small: `--effort` (Codex reasoning effort), `--max-input-bytes`, `--max-output-bytes` (provider request and answer bounds, at most 65536).
