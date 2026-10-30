# Captured network evidence

Record the request that supplies the desired data with `rep page URL --capture`, `rep do REF --capture`, or `rep eval @script.js t3 --capture`. Capture is optional because browsers serialize capture work. An addressed page with capture reloads to observe a fresh load. Each readiness continuation can record another bounded window; the capture identifies its predecessor with extends and marks observation_gap and incomplete network evidence across the idle gap. It does not claim to have recorded while the CLI was absent.

`rep net` returns a bounded overview. Query terms are positional and ANDed:

```sh
rep net s_CAPTURE GET type:api
rep net s_CAPTURE 4xx host:example.com
rep net s_CAPTURE has:result in:body
rep net s_CAPTURE '~item-[0-9]+' in:body
rep net s_CAPTURE since:10m
```

Methods, write, exact status, status class, err, host, type, literal search and regex projection select traffic. Use a capture handle to pin evidence. An implicit capture is permitted only when exactly one recent capture qualifies; ambiguity does not union archives. Rows return full canonical request IDs, first-party marking, body state and bounded metadata. Follow the returned cursor to continue matching rows.

```sh
rep body h_ID
rep body h_ID /results/0/title
rep body h_ID '#2'
rep body h_ID --request
rep body h_ID --find needle
rep body h_ID --out response.json
```

The request ID resolves its exact archive and fails closed on a miss. Body output includes completeness, capture state, content type, byte size, source archive, hashes and redacted request/response metadata. JSON pointers select values. Quote `#N` so a shell does not treat the event selector as a comment. SSE/NDJSON projection follows the content type. A find returns offsets and bounded context. Use the returned `next` for the remainder; partial bytes never prove a complete response.

Inline headers and credential-bearing fields are fingerprinted before filtering or truncation. Automatic large artifacts are redacted too. Only explicit `--out` writes raw decoded bytes to the chosen path, using exclusive creation and mode 0600; an existing file is refused. Bodies over the memory/inline ceiling spill to private artifacts. Preserve hashes, archive provenance and completeness when reporting evidence. A missing body is not an empty response, and re-fetching is a new observation rather than a repair of old evidence.

`rep fetch URL` makes a fresh credentialed request in a rep-owned background tab for its task and origin, using cache no-store. It accepts `-X METHOD`, repeatable `-H 'Name: value'`, and `-d DATA` or `-d @file`; a body defaults the method to POST. Credentials remain in the browser. See [replay](replay.md) to vary an observed request or download a file.
