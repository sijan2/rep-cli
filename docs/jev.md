# Jev decisions

Jev is TypeSafe's typed decision model, not a text/chat generator. Its official
API is `POST https://api.typesafe.ai/v1/systemone`, using Bearer authentication,
`model`, `state`, and named typed `questions`. Choice answers contain the winning
option, a complete probability distribution and a separate confidence value.
Confidence is not the same as the winning option's probability. The default
model is `jev-latest`; set `JEV_MODEL` to pin a published Jev model identifier.

Configure a local file once so Chrome/Arc's native host can use it regardless of
the directory from which it starts:

```sh
rep jev config --env-file /absolute/path/to/.env
rep jev status -j
rep jev doctor -j
rep --workspace NAME --task TASK jev classify <captured-id> -j
```

The env file can contain `JEV="apikey"`. Credential precedence is `JEV`,
`JEV_API_KEY`, `TYPESAFE_API_KEY`, then `JEV_ENV_FILE`, the configured env-file
path, and finally the working directory's `.env`. Configuration stores only the
absolute file path in `~/.config/rep-cli/jev.json` (or under `XDG_CONFIG_HOME`)
with mode 0600. The parser reads literal dotenv assignments; it never executes
shell commands or variable interpolation. The browser never receives the key.

`status` is local and does not call TypeSafe. `doctor` makes one synthetic
classification evaluation. `classify` reads one existing capture from local
storage and sends only scrubbed method, hostname, route shape, resource type,
status and content type. It excludes bodies, headers, URL credentials, queries
and fragments. Unknown path segments and filenames are replaced; categories
are `api`, `document`, `static`, `analytics`, and `other`. Results preserve
probabilities/confidence and flag confidence below 0.8 for review. No request is
replayed and no browser action executes as a result of a decision.

The extension's **Classify with Jev** action uses the same classifier through
the native host. Rebuild/install the CLI and host with
`scripts/build_install.sh --host --install-dir ~/.local/bin`, then reload the
extension so it reconnects to the new host. Native requests are correlated by
ID, evaluated outside the capture loop and limited to four concurrent calls.

HTTP requests have a 30-second overall deadline, forbid redirects and make at
most three attempts for connection failures, rate limits, overload, or server
errors. Retry-After seconds and HTTP dates are honored; a delay beyond the
deadline fails with a retry-later message.

Typed decisions must answer every question with one of the offered options, a
confidence in 0..1, and a finite probability for every offered option. The
provider reports probabilities rounded to hundredths and documents totals of
approximately one, so a total is valid when rounding can explain it: every
option can lose up to 0.005 and every nonzero value can gain up to 0.005
(totals within 0.01 of one always pass). For example, 0.99 across a few
options or 0.98 across 41 options is valid; 1.02 with two nonzero values, 0.97
across three options, or a distribution with no mass is rejected. A fixed
tolerance fails as probability spreads over look-alike candidates, which is the
common case for text on busy pages. Reported values and confidence are never
renormalized. See the [response contract](https://docs.typesafe.ai/sdk/python/api/types/responses#probabilities).

Request budgets follow the documented jev-1.13 limits of 64k tokens per request
and 32k tokens for state plus the longest question ([models](https://docs.typesafe.ai/models)).
Rep estimates tokens conservatively (two ASCII bytes or one non-ASCII byte per
token; measured candidate JSON used 2.3-3.1 bytes per token) and allows 30k per
question and 60k per request, with an absolute 128 KiB request cap. If the
provider still reports `max_tokens_exceeded`, selection repeats with the same
candidates split into smaller questions (down to a quarter of the budget).
Responses are limited to 1 MiB.

Failures carry a stable code and a message naming the actual cause; messages
never contain API response bodies or credentials, only the provider's
machine-readable `error_type` when present. `select`, `select-batch`, `observe`,
`validate`, `interact`, and the `jev` commands print them as structured errors
(JSON on stdout with `-j`, text on stderr otherwise):

| Code | Meaning |
|---|---|
| `jev_not_configured` | No key found; run `rep jev config --env-file PATH` |
| `jev_unauthorized` | HTTP 401/403 or `authentication_error`; check the key |
| `jev_rate_limited`, `jev_overloaded`, `jev_server_error` | HTTP 429, 529/503, or 5xx after three attempts |
| `jev_timeout`, `jev_connection_failed` | Deadline or transport failure (DNS, reset, TLS) after retries |
| `jev_context_exceeded` | Provider `max_tokens_exceeded` even after smaller splits; narrow the scope |
| `jev_request_rejected` | Another 4xx response from the provider |
| `jev_invalid_request` | The request would exceed a local budget or question limit |
| `jev_invalid_response` | A response failed validation; the message names the failed check |
| `host_outdated` | The browser's rep-host predates the CLI; install both and reload rep+ |

Official references: [API](https://docs.typesafe.ai/api),
[Choice](https://docs.typesafe.ai/primitives/choice),
[models](https://docs.typesafe.ai/models).

## Semantic lookup in complex browser pages

```sh
rep browser tabs --browser arc -j
rep browser select --tab TAB_ID 'Find the course materials link' -j
rep browser select 'Instructions about the report format' --tab TAB_ID --kind text --raw-json
rep describe jev
```

The native host owns selection and a persistent HTTP client. The extension reads
Chromium accessibility data and sends a compact projection: browser-computed
names, roles, ancestor and sibling entity context, the nearest preceding section
heading in document order, control states, and frame-aware handles. The top
frame's document title is not used as a candidate's context, because every
candidate shares it; child frames keep their document title. Duplicate labels
retain separate identities. Input values and editable
text descendants are excluded, along with URL attributes, credentials, and raw
DOM. Shadow controls exposed by Chromium are included. Up to sixteen frames are
considered; unavailable or omitted evidence is reported explicitly.

The default candidate limit is 240 (maximum 480). A question with up to 254
candidates plus `none` uses one Choice when it fits the token budget; typical
pages fit. One flat Choice compares every candidate at once. Larger inputs use
budget-sized groups and a final comparison of each group's leaders, which is less
reliable: a finalist round no longer sees the full candidate set. Independent
questions can share a request. Confidence and coverage still gate every result.
`--strategy legacy` provides the sequential forty-candidate comparison mode.

A fresh initial observation validates cached decisions. New model decisions are
rechecked after inference. Results can be `selected`, `needs_review`, `no_match`,
or `stale`. Truncation, missing frames, unresolved context, and low confidence
require review. A selection executes no click, typing, navigation, or submission.
Frame/backend IDs are temporary handles, not permanent locators.

The private five-minute cache is bounded to 128 entries and partitioned by task,
credentials, model, options, and current evidence. It stores IDs, probabilities,
usage, and timestamps, not page text or goals. `--no-cache` bypasses it. Usage is
zero on cache hits; `original_usage` records the earlier request. Pin `JEV_MODEL`
for reproducible comparisons.

Use `browser observe` for model-free evidence, `browser select-batch` for
independent goals, and `browser validate` to refresh a scoped binding. Frame and
subtree scopes keep candidate eligibility explicit; contextual siblings remain
part of freshness evidence. See [the decision runtime](decision-runtime.md) for
contracts, batching, reuse, lifecycle limits, and measurement instructions.

Lookup covers named, loaded accessibility nodes. It cannot infer virtualized or
canvas-only content. Verified actions separately check current geometry,
occlusion, frame identity, and declared outcomes.

## Verified interactions

`rep browser interact FILE --tab ID [--apply]` integrates semantic selection with
typed, locally verified browser actions. The older `rep jev act --plan FILE` form remains compatible.
Exact targets skip the model; goal targets use the existing selector and cache.
The runtime verifies input, scopes duplicate labels, and waits for explicit
outcomes. It never retries a mutation after an uncertain response.
See `rep describe interact` and [the interaction guide](interactions.md).
