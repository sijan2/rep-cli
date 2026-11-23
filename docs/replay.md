# Fetch, replay and download

Capture a page's own request with `rep page URL --capture`, then use its returned `h_` handle. `rep body h_ID --request` reveals the redacted request shape needed for a parameter sweep.

```sh
rep replay h_ID
rep replay h_ID --set query.page=2
rep replay h_ID --matrix 'query.page=[1,2,3]'
rep replay h_ID --set body.enabled=true --write
```

Mutation paths use header, query or body fields. `--matrix` creates one variant per value. Safe GET/HEAD variants run in parallel with adaptive backoff in a dedicated task/origin background tab. An unsafe captured or rewritten method requires `--write`; variants then run sequentially, stop on the first non-2xx (including 403), are never retried, and are journaled. The same in-page transport serves a single variant and a matrix. `--timeout` bounds the network work.

Output provides bounded status/size summaries and retained artifacts for variants. Credential fields stay fingerprinted. Read retained bytes through their exact request/archive handles. Use `rep cookies export` and `rep cookies import` to transfer a session, or independent `REP_PROFILE` names for different accounts; see [authentication](authentication.md).

Variants run from an isolated world in the owned tab, so page scripts cannot observe or patch the request while the browser still attributes it to the page. The first 429 or 403 ends the job: no variant is retried, unsent variants are reported as `not_sent_after_rate_limit`, and the server's `Retry-After` becomes a cooldown under `<data>/governor/` that every later `rep fetch`, `rep replay` and `rep records` on that site refuses with `cooldown` until it ends (`rep scope` lists active cooldowns). Explicit fetch/replay commands are available on every site. Captured per-request signatures are forwarded as observed; a server may reject expired signatures or mutations they no longer cover. `rep records` reads the page's own responses when reproducing the request is impractical.

`rep fetch URL` creates a fresh in-page request. `-X` chooses a method, repeatable `-H` supplies headers, and `-d` supplies data or an @file; data defaults to POST, otherwise GET. Browser credentials are included. Explicit browser-managed headers such as Cookie, Origin, Host or Referer return an actionable error; they are not silently dropped. Restore cookies through `rep cookies import TAB @FILE`. Authorization, content-type and application CSRF headers can be supplied with -H. Cache mode is no-store. This is a new observation, distinct from replaying retained evidence.

```sh
rep download h_ID report.pdf
rep download https://example.com/report.pdf report.pdf
```

Download performs GET-only acquisition to a private file, infers validators from the destination extension, refuses empty files and existing paths, and reports bytes, content type, digest and transport. `--overwrite` permits replacing an existing destination; `--timeout` bounds the transfer. Captured requests may fall back from the browser transport to a credential-preserving GET transport. URL-only downloads do not invent captured headers. A rejected write is never converted to another transport.
