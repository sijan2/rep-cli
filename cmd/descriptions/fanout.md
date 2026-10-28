# `rep browser capture fanout` contract

`fanout` replays one captured request many times in parallel, from inside a tab
on the request's own origin, and archives every response body through the
ordinary action capture. It generalizes the Marketplace tool's measured
approach (20 queries in 3.8 s versus 162 s of page scraping): acquire one real
request, then vary it in-page with the page's own session instead of navigating
and scraping once per value.

```sh
rep --workspace PROJECT --task TASK browser capture fetch 'https://api.example.com/search?q=chair' --keep-tab --raw-json
rep --workspace PROJECT --task TASK browser capture fanout h_REQUEST_ID --matrix 'query.q=[chair,lamp,desk]' --concurrency 8 --raw-json
rep --workspace PROJECT --task TASK body h_VARIANT_ID --saved SAVED_HASH --pointer /items/0/price
```

## Arguments and flags

| Flag | Meaning |
|---|---|
| `--matrix 'query.NAME=[v1,v2]'` | Required. One variant per value. `body.KEY` mutates JSON or form bodies; `header.NAME` sets a header. Same grammar as `rep replay --matrix`. At most 256 values. |
| `--set path=value` | Constant mutation applied to every variant (repeatable). |
| `--concurrency N` | Parallel in-page requests, 1 to 32 (default 8). |
| `--tab ID` | Existing tab on the request's origin. Without it an owned inactive tab opens on the origin root and closes afterwards. |
| `--keep-tab` | Keep that owned tab warm for the next fanout. |

Capture-family flags apply: `--timeout` (default 30s) bounds the whole
operation, `--max-body` bounds each archived body.

## Behavior

- Every variant must stay on the source request's origin. The page session is
  the credential; a cross-origin fetch would be governed by CORS and is refused
  with `origin_mismatch` before anything runs.
- Captured headers are forwarded except forbidden and identity headers
  (cookie, user-agent, origin, referer, sec-*, content-length and similar).
  Authorization and custom headers are kept because they are part of the
  observed request. Cookies come from the browser profile.
- Each response body is read to completion in the page so the capture holds
  the full bytes; the page evaluation returns only per-variant metadata.
- The result pairs variants with captured request IDs by method and URL. Equal
  URLs (body mutations) pair in start order and are marked
  `method_url_order`; anything that cannot be paired is `unmatched`, never guessed.
- `fanout_summary` reports totals; `captured_requests`, `saved_hash_id` and
  `body_capture_states` come from the sealed capture like other captures.
- The capture lane is serialized with other explicit captures on the same
  bridge. Every fetch is awaited to completion in the page, so the capture ends
  250 ms after the evaluation returns (`complete_after_settle`) instead of
  waiting for network idle, which never arrives on pages that keep chat long
  polls or streams open. Measured on reddit.com: 30 s at the deadline before,
  about 1.5 s after. Background requests still in flight are reported as pending.
- Find the request to replay by observing the page itself: `browser page` and
  `browser state` list every fetch/XHR endpoint the page issued in
  `data_requests`, and `browser capture open URL` archives the bodies. Replay
  what the page does; do not guess legacy endpoints (Reddit's `.json` API
  returns 403 to a plain fetch while its own `/svc/shreddit/graphql` and
  `/search/?q=` requests replay cleanly from the page).
- The replay runs from the document that issued the request (`page_url`), so a
  request the page makes to a sibling API host replays under the same CORS
  grant the page has.

Rate limits, challenges and login walls appear as per-variant HTTP statuses
(for example 429 or 403) with archived bodies; stop and inspect rather than
retrying blindly. Read-only use is the intended scope; a POST matrix still
sends real requests with the signed-in session.
