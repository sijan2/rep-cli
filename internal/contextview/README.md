# Bounded capture context

`Build` accepts a capture that the caller has already isolated to a workspace,
task, and source. It returns compact JSON; **the JSON plus one newline fits the
requested byte budget**. The default is 8,192 bytes, with a supported range of
1,024–1,048,576 bytes. A budget that cannot carry provenance or make progress
returns an explicit error. Error messages are outside the successful-view budget.

The projection is deterministic and uses no model calls:

1. Group requests by method, canonical origin, and normalized route. Preserve
   scheme and nondefault ports, so different local servers and HTTP/HTTPS remain
   distinct. Remove URL userinfo, query, fragment, and default ports; replace numeric, UUID, common opaque-ID,
   and recognized credential-value path segments. Bound all displayed strings.
   These are route heuristics, not a guarantee of redacting every secret a site
   might put inside an otherwise ordinary-looking path segment.
2. Count response statuses and resource types. Preserve up to three existing
   request IDs: the latest request, then status diversity, then recent IDs.
   Omitted representative IDs are counted. Invalid IDs are counted and omitted,
   never replaced with invented drill-down targets.
3. Round-robin across origin/method/status-class buckets, with most recent
   groups first inside each bucket. This preserves breadth under a fixed budget
   without giving a repeated endpoint all the available space.
4. Admit only complete JSON groups and removal IDs that fit the budget.
   Provenance and source totals are always present. `omitted.groups` and
   `omitted.requests` refer to pending groups omitted from this response, while
   `omitted.removed` counts omitted removal notices.

## Incremental context

`Options.Since` is an immutable content-addressed checkpoint. It is bound to the
full `ScopeID` and `SourceID`; unknown, expired, malformed, or foreign checkpoints
produce an error without falling back to an unrelated or full dataset. The caller
must include stable source selection/filter identity in `SourceID`. Capture
export timestamps and capture session labels belong in `Provenance`, so updates
to the same logical source remain comparable.

A checkpoint acknowledges **only delivered groups and removals**. If a response
has `complete:false`, follow its returned cursor to obtain more pending changes.
An omitted update keeps its previous hash; an omitted addition remains unseen;
an omitted removal remains in the baseline. Consequently a small budget cannot
silently swallow changes. `no_change:true` means the complete current group state
matches the acknowledged baseline. Metadata such as export time is still
refreshed in that response. Concurrent changes to the capture are compared with
the last acknowledged state, rather than claiming to replay every intermediate
capture event.

Request and response contents are hashed locally to detect a late response or
changed response body under an unchanged request ID and timestamp. Neither the
output nor the checkpoint contains those bodies, headers, queries, or session
notes. Checkpoints store scope/source hashes and group-to-content-hash mappings.
They do not alter the raw captures.

The caller supplies a scope-specific `CacheDir`. The directory is private
(`0700`), checkpoint files are private (`0600`), and retention is limited to 64
checkpoints and 24 hours. Checkpoints are at most 2 MiB each; an oversized baseline
returns an explicit narrowing error. Files are atomically published without
rewriting an existing checkpoint. Independent agents should use independent
task scopes; a cursor is a context baseline, not an authentication capability.
