# Thread records

`rep records TARGET` turns the responses a page's own client loaded into one thread: records with id, parent, derived depth, author, time, text and metrics, plus the continuations the page still offers. It decodes; it never clicks, scrolls, or authors a request, and it makes zero model requests.

```sh
rep records https://www.reddit.com/r/programming/comments/ID/title/
rep records t3
rep records s_CAPTURE
rep records h_ID h_ID2
rep records saved.html --url https://www.reddit.com/r/programming/comments/ID/title/
```

| TARGET | Source |
| --- | --- |
| URL | Live read on a tab rep creates: the per-tab network ledger opens before navigation and keeps only policy-declared responses |
| `tN` | The same read on an owned tab after you advanced it yourself with `rep do` or `rep page` |
| `s_` capture | The capture's primary thread response plus every fragment whose request names a pending continuation |
| `h_` requests | Exact archived responses, merged in the order given |
| files | Saved response bytes; `--url` supplies the response URL for detection and the X focal status |

Supported decoders: Reddit server-rendered threads and more-comments fragments, X TweetDetail pages. `--site` pins a decoder when detection is ambiguous. `--max` bounds the inline record preview (default 50, 200 bytes of text each); every record is in the artifact.

## Result

One JSON object. `status` is `complete` only when every modeled continuation is resolved, no resolved continuation fell short of its count hint, the field contracts hold (null-rate ceilings: id 0%, parent 1%, author and time 5%, text 10%), unknown item types stay at 10% or less, and records reach 90% of the expected count. Anything less is `partial` with `meta.coverage.code` naming the first failed check: `frontier_pending`, `resolved_short`, `drift_fields`, `drift_unknown`, `server_gap`, `gated`, `challenge` or `auth`.

`meta.frontier` lists continuation kinds, count hints, states and reasons; their parent ids and labels are page data and sit under `untrusted.frontier`, aligned by index. Record text, authors and ids are under `untrusted`. `meta.artifact` names the private directory `<task>/records/rd_ID/` holding `records.ndjson` and `manifest.json` (0600). Records contain third-party usernames and text, so the directory is pruned after 7 days at the next run.

A fragment resolves a continuation only when its own request names it: Reddit by decoded path plus posted cursor, X by `variables.cursor`. Files carry no request, so merging them adds records without resolving anything. A response whose URL looks like a thread but whose bytes do not is never selected.

## Live reads

A live read checks the site's cooldown before anything else, creates an inactive tab, opens the network ledger, navigates, and decodes the primary response once it arrives, together with any fragments the page's client had already loaded. The tab and its ledger close afterwards unless `--keep-tab` keeps them; a kept tab's result names it in `next`, and `rep records tN` later re-reads the same ledger without navigating again. A thread that needs the user's sign-in returns `auth`, a challenge page `challenge`, a 429 `rate_limited` with a recorded cooldown, and a page that never delivers its thread response `primary_timeout`. The ledger is a per-tab observer on tabs rep created: Network is enabled before `Page.navigate`, bodies are kept only for operations the site policy declares (Reddit: the thread document and `/svc/shreddit/{more-comments,comments}/`; X: `TweetDetail` and `TweetResultByRestId`), private paths such as chat, inbox and drawer partials stay metadata-only, static assets are metadata-only, and credential headers and token-like fields become `{redacted, bytes, sha8}` inside the extension before anything crosses native messaging. The ledger reads no cookie values at all. Pending continuations are reported, not followed; advance the page yourself and read the tab again.

Site policies are data files in `internal/sitepolicy/policies`. They are matched by URL pattern and response shape, never by query id or token, and a malformed policy fails closed.

## Controls that belong to records

`rep records controls tN` takes one reduced geometry snapshot of an owned tab and returns one line per visible record with the controls inside it:

```text
r_t1_abc d1 @alice 4d s41 'SMTP relays charge per 1k because...' [reply n_9c1e0a.18801 | up n_9c1e0a.18802 | 3 more replies n_9c1e0a.18850]
```

Records are keyed by the same ids the network returns (Reddit `thingid`, X status id), a quoted post nests under its tweet, and a "more replies" button directly below a record is attached to it. Controls that belong to no record are counted as orphans. When the tab has a ledger from a records read, the network thread supplies reading order and the records the page has not rendered. Nothing here calls a model.

`rep do n_HANDLE tN` clicks such a control; `--press KEY` sends keys afterwards. Before any input the handle is checked against its own node (same document, same label, same owning record); a changed page returns `stale_document`, `control_changed` or `page_changed` with nothing dispatched, and a covered control returns `occluded`. Input is native and never retried. Node handles support click and keys only; fill values keep using observed `tN.eN` refs.

## Rate governance

A shared governor under `<data>/governor/` records a cooldown per site when any rep transport sees a 429 (from `Retry-After`, the `RateLimit` draft headers, Reddit `x-ratelimit-*` or X `x-rate-limit-*`, else 60 s) or a challenge page (120 s). `rep fetch`, `rep replay` and `rep records` refuse with `cooldown` until it ends, and the Python tools read the same file. The in-page transport stops at the first 429 or 403 and never retries around it. Floors are fixed; nothing adds jitter.
