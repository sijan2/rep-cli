# rep — HTTP traffic analyzer (agent prompt)

rep drives a real Arc/Chrome profile and analyzes captured HTTP traffic from
rep+. Use it instead of a separate headless browser, copied cookies, or the
extension UI.

## Workflow

1. `rep browser status --browser arc -j` — confirm the real-profile bridge.
   After changing unpacked extension source, use `rep browser reload-extension`.
2. `rep browse <url> --browser arc --keep-tab -j` — navigate once and retain the owned tab.
3. `rep summary` — confirm the new live session.
4. `rep primary <domain>` — scope your target (enables `--primary` filter).
5. `rep list --primary --interesting` — triage (errors + mutations).
6. `rep list --primary --api` — all API calls.
7. `rep body <id>` — inspect a response. Large bodies spill to
   `/tmp/rep-body-*.{ json,html,js,... }`; read the spill file directly.
8. `rep browser action <js> --tab <id> --settle 1500ms` — execute page logic and capture delayed browser callbacks plus their traffic.
9. `rep download <request-id> <path>` — stream a captured final GET to an atomic file without printing secrets or bytes.
10. `rep browser download <request-id> <path>` — retry a captured GET through the real browser when curl is rejected; no ArcCore port is needed.
11. `rep browser fetch <url>` — replay inside the browser with HttpOnly cookies.
12. `rep auth --save -d <domain>` — extract tokens to a 0600 env file
   (the agent never sees raw values).

Use `rep browser attach/cdp/eval/detach` for low-latency DOM, Input, Runtime,
and Page control. Use `rep arc cdp` for browser-level Browser/Target commands.
Use `rep browser create about:blank` when a task-owned real-profile tab is
needed without navigation capture or browser-process CDP.
Use `rep browser fetch @<mode-0600-url-file> --headers-only` to capture a large
signed GET without buffering its body in the renderer, then pass the returned
request ID to `rep browser download`.
Reuse the owned tab with `rep browse <url> --tab <id> --referrer <url>` instead
of creating one tab per route.

## Output contracts

- Plain text by default; all commands respect `NO_COLOR` and TTY.
- `-j` / `--output json` is parseable.
- `--envelope` wraps JSON in `{ source, command, filters, truncation, data, suggest }`
  so empty/truncated/errored results always include a concrete next command.

## ID format

Canonical request IDs are `h_xxxxxxxxxxxxxxxx`. Every command that takes
an ID accepts **any ≥4-character prefix** or the display label
(`xxxx_METHOD_STATUS`). Copy from any `rep list` line.

## Empty results

`rep list` and `rep search` never return silent empties. The output is:
```
source: live.json (N requests, M domains)
filters: primary=K (J match candidates), ...
result: 0 requests matched
suggest:
  rep list --primary=false
  rep primary --clear && rep primary <domain>
```
Parse `suggest:` lines for concrete next commands.

## Secrets

- `rep setup --json` emits previews + an env_file path. Source the file;
  reference values by `env_name`.
- `rep auth --vars` prints shell-export glue (variable names only, no values).
- Avoid `rep auth --export` and `rep setup --include-secrets` in agent
  contexts — both surface raw values.
- Do not print raw `live.json` or `rep curl` for signed URLs. Use bounded
  metadata plus `rep download`, which keeps the URL and headers off stdout.

## When NOT to use rep

- For a public one-off request that needs no browser identity or session → use `curl`.
- For traffic not captured by rep+ → rep has nothing to show.
- For binary file analysis → `rep body --save` gives a path; use your
  own file tools on the spilled file.
