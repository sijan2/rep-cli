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
most three attempts for rate limits or server errors. Retry-After seconds and
HTTP dates are honored; a delay beyond the deadline fails with a retry-later
message. Errors never include API response bodies. Typed decisions must match
the supplied options and have finite valid probabilities that sum to one.
Conservative byte budgets cap state plus each question at 24 KiB and total
requests at 48 KiB. Responses are limited to 1 MiB.

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

The locator reads `Page.getFrameTree` and `Accessibility.getFullAXTree` through
the existing real-profile bridge. It preserves browser-computed names, roles,
ancestor context, and whitelisted control states. Duplicate labels keep distinct
IDs. Input values and editable text descendants are excluded, along with URL
attributes, credentials, and raw DOM. Named controls in shadow roots are included
when Chromium exposes them in the accessibility tree. Up to sixteen frames are
considered, with unavailable frames reported explicitly. `--origin ORIGIN`
requires that top-level origin and excludes frames outside it.

Code caps the candidate set (`--limit 240`, maximum 480), shortlists by lexical
relevance when necessary, and reports truncation. Each Jev Choice includes at
most forty observed candidates and `none`; larger sets use group decisions then
a final comparison of shortlisted candidates. Group uncertainty remains visible
in the final review status. Independent group probabilities are not treated as
comparable scores. No selectors, scripts, or actions are generated.

Every model or cached decision is checked against a fresh snapshot. An empty
candidate set returns after the initial coherent snapshot. Returned backend node IDs and
frame IDs describe that observation; they are not permanent locators. Results
can be `selected`, `needs_review`, `no_match`, or `stale`. Missing frames,
truncated labels or candidate sets, unresolved ancestry, and confidence below
`--confidence 0.8` require review. A successful
selection performs no click, typing, navigation, or submission.

The five-minute decision cache lives under `~/.cache/rep-cli/jev-dom` (or
`XDG_CACHE_HOME`), is bounded to 128 entries, and uses private files. It stores
decision IDs, probabilities, model/usage, and timestamps, not page text or goals.
Reuse requires the same goal, model, candidate options, and fresh observation
fingerprint. `--no-cache` bypasses it. `usage` is zero on cache hits;
`original_usage` records the earlier request. Provider aliases are only cached
within this short expiry; pin `JEV_MODEL` for reproducible runs.

Limitations: this is lookup over named, loaded accessibility nodes. It does not
prove visibility/occlusion or hit-test click geometry, render virtualized content,
interpret canvas pixels, or support every frame/process boundary. The subsequent
browser action must check its own current state and task authorization.

## Verified interactions

`rep browser interact FILE --tab ID [--apply]` integrates semantic selection with
typed, locally verified browser actions. The older `rep jev act --plan FILE` form remains compatible.
Exact targets skip the model; goal targets use the existing selector and cache.
The runtime verifies input, scopes duplicate labels, and waits for explicit
outcomes. It never retries a mutation after an uncertain response.
See `rep describe interact` and [the interaction guide](interactions.md).
