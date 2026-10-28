# Reusable site search packs

The Rep workspace includes `tools/sitelearn/sitelearn`. This is a companion
tool, not a built-in `rep site` command. It stores versioned data packs for two
transports: fetched documents and observed JSON APIs. No extension plugin or
agent customization is required to invoke it. From another directory, use its
absolute path or an existing `sitelearn` executable on PATH.

```sh
tools/sitelearn/sitelearn --help
tools/sitelearn/sitelearn learn-api observations.json --out pack.json
tools/sitelearn/sitelearn run pack.json "office chair" "desk lamp" --pages 2 --json
```

An observation bundle supplies an entry URL, three distinct captured searches
including a held-out query, independently observed visible records, and a next
page/terminal-page example for cursor binding. `learn-api` is offline apart from
optional Jev choices among matching fields (`--no-jev` disables those). It
infers record paths, fields, query value flow and response-to-request cursors.
It does not generate or execute page actions to collect those examples.

Export one original capture at a time, using its workspace/task:

```sh
tools/sitelearn/sitelearn --workspace PROJECT --task ORIGINAL_TASK capture REQUEST_ID \
  --saved FULL_ARCHIVE_HASH --query "office chair" --page 1 --has-next \
  --visible visible-records.json --out observation.json
```

`rep detail ID --saved HASH --output meta --json` and archive-pinned body
artifacts keep request metadata and complete response bytes from the same
capture. The full bundle format and live token/operation metadata sources are
documented in `tools/sitelearn/README.md`.

Execution checks same origin, shape, identities, price/sort contracts where
supported, pagination progress, response/output sizes, timeouts and budgets.
Challenges, login requirements and 429s stop new work. In-flight requests are
aborted where possible. No automatic challenge solving or request retries are
performed. Access-blocked tabs remain open for inspection.

API packs support GET, JSON/form GraphQL queries, and persisted GraphQL queries
with a live operation metadata source. Session values come from declared page
sources; credentials are not copied into packs or model state. Unknown POSTs,
cross-origin APIs and ambiguous bindings need an explicit adapter or more
evidence. Model probabilities are not proof that a binding is correct.

`run` performs no model calls. It fails without publishing partial records when
a required contract fails. Sitelearn serializes its own processes per origin;
this does not coordinate arbitrary rep calls or other tools. Fresh sitelearn
invocations use unique task identities unless the caller supplies one. Headless
use requires the explicit task whose private browser is already running.

For Marketplace's ranking/exploration/photo workflow use `tools/fbmp/fbmp`.
Use `rep browser shots --parallel N` for bulk screenshots. Historical shopping
measurements are not performance guarantees for a newly learned site.
