# Fetch, replay and download

Capture a page's own request with `rep page URL --capture`, then use its returned `h_` handle. `rep body h_ID --request` reveals the redacted request shape needed for a parameter sweep.

```sh
rep replay h_ID
rep replay h_ID --set query.page=2
rep replay h_ID --matrix 'query.page=[1,2,3]'
rep replay h_ID --set body.enabled=true --write
```

Mutation paths use header, query or body fields. `--matrix` creates one variant per value. Safe GET/HEAD variants run in parallel with adaptive backoff in a dedicated task/origin background tab. An unsafe captured or rewritten method requires `--write`; variants then run sequentially, stop on the first non-2xx (including 403), are never retried, and are journaled. The same in-page transport serves a single variant and a matrix. `--timeout` bounds the network work.

Output provides bounded status/size summaries and retained artifacts for variants. Credential fields stay fingerprinted. Read retained bytes through their exact request/archive handles. Stored identities and principal swapping are outside core.

`rep fetch URL` creates a fresh in-page request. `-X` chooses a method, repeatable `-H` supplies headers, and `-d` supplies data or an @file; data defaults to POST, otherwise GET. Forbidden headers are dropped and browser credentials are included. Cache mode is no-store. This is a new observation, distinct from replaying retained evidence.

```sh
rep download h_ID report.pdf
rep download https://example.com/report.pdf report.pdf
```

Download performs GET-only acquisition to a private file, infers validators from the destination extension, refuses empty files and existing paths, and reports bytes, content type, digest and transport. `--overwrite` permits replacing an existing destination; `--timeout` bounds the transfer. Captured requests may fall back from the browser transport to a credential-preserving GET transport. URL-only downloads do not invent captured headers. A rejected write is never converted to another transport.
