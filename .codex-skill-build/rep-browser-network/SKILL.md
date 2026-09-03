---
name: rep-browser-network
description: CLI-first web retrieval and real-session Arc/Chrome network control with rep. Apply to browser traffic, signed-in content, page resources, observed APIs, or downloads; dynamically choose a native CLI fast path for public sources and rep/CDP only when browser state or instrumentation is needed, never browser UI automation.
license: MIT
compatibility: Requires the rep CLI (github.com/sijan2/rep-cli) on PATH plus the rep+ browser extension and its native-messaging host; macOS with Arc or Chrome signed in. Python 3.9+ (standard library) for the download helper. Network access.
metadata:
  version: '1.0'
  skill-author: sijan
---

# Rep Browser Network

Use the real Arc/Chrome profile through `rep`, rep+, Native Messaging, and CDP. Keep cookies, service workers, TLS behavior, extensions, proxy state, and browser identity inside the browser.

## Hard invariant

For matching tasks, do not use Chrome/Arc UI automation: no Chrome-control skill, Computer Use, screenshot-driven interaction, AppleScript clicks, mouse/keyboard input, or opening DevTools panels. Do not fall back to UI when a CLI command fails. Diagnose the bridge or use another CLI/data-plane route. If no CLI surface can perform the required interaction, report the exact missing capability.

Also do not substitute a separate headless browser, export the cookie jar, add `--headless` or `--enable-automation`, patch JavaScript globals, spoof fingerprints, or synthesize mouse/keyboard input. Use Network, Page, Runtime, DOM, Storage, Target, and IO data-plane commands.

## Choose the cheapest correct data plane first

Classify the source before any browser preflight:

- Repository clone/source task: use the provider CLI (`gh repo clone`, `gh api`, or `git`) directly. A GitHub repo spec such as `owner/repo` does not need Arc.
- Clearly public static URL: use `curl`, `aria2c`, or the domain's native CLI with bounded output and an explicit file path.
- Existing captured body: use `rep body <id> --save`.
- Browser-authenticated, anti-bot-sensitive, JavaScript-derived, service-worker-mediated, or traffic-inspection task: use the real-profile `rep` route below.

Escalate from a native CLI to Arc only on evidence that browser semantics are required (authentication redirect, 401/403/429 challenge, missing JavaScript-derived URL, or an explicit traffic-capture requirement). Do not pay browser startup, contract, or navigation cost for a public repository/static download.

## Begin browser work with one cheap preflight

Resolve `rep` from `PATH`; on this machine the fallback is `/Users/sijan/.local/bin/rep`. Query status once after selecting the browser route:

```bash
rep browser status --browser arc -j
```

Use the returned `capabilities` as the router. Run command-specific `--help` or `rep describe browser -j` only when a needed capability/flag is unfamiliar, missing, or rejected; run `rep describe arc -j` only for browser-process work. Do not reload the same contracts on every turn.

If status is unhealthy, use `rep browser doctor --browser arc -j`, then the narrow recovery in [references/rep-recipes.md](references/rep-recipes.md). Do not reload or restart a healthy browser.

## Route by outcome

- For a signed-in JSON/HTML request on one origin, use `rep browser fetch`. It performs credentialed renderer `fetch()` and captures the underlying CDP request and body.
- For a real page load, JavaScript execution, redirects, service workers, subresources, or browser-facing bot checks, use one `rep browse --keep-tab` background navigation. Reuse that owned tab across routes with `rep browse <url> --tab <id>`, adding `--referrer` when the site validates navigation provenance.
- When page JavaScript triggers the request of interest, use `rep browser action <js-or-@file> --tab <id>`. It evaluates and captures bodies in one isolated operation; do not combine an ambient watch, click/eval, and a second capture for the same action. Return only small metadata from the expression and keep the default `--max-result` cap; response bytes belong in the sealed capture.
- To observe normal traffic across materialized tabs and domains, use `rep browser watch start`; stop it in cleanup unless the user asked for continuing monitoring.
- For repeated DOM, Runtime, Network, Page, Storage, Emulation, or IO work, use `rep browser attach`, multiple `rep browser cdp`/`eval` calls, then `detach`.
- For inactive page/tab creation and page CDP, use `rep browser create` and `rep browser cdp`. Reserve `rep arc` for browser-process operations, direct worker CDP, launch/restart, or extension reload.
- For a small or medium captured payload, identify its request and use `rep body <id> --save` rather than copying content through chat.
- For a captured final GET, use `rep browser download <request-id> <output-path> -j`. It streams through the real browser profile to an atomic mode-0600 file without printing the signed URL, cookies, headers, or bytes. Add `--min-bytes` and `--expect-magic` for binary artifacts such as IPA/ZIP files.
- If the server rejects curl because credentials/fingerprint are browser-bound, run `scripts/rep_arc_download.py`, which streams through Arc's credentialed `Network.loadNetworkResource` and `IO.read`. Read the download section of the reference before either path.

Prefer `rep` over plain `curl` when session state, browser headers, origin policy, service workers, client hints, or anti-bot consistency may matter. Plain `curl`/`aria2c` is acceptable only for clearly public static content that needs none of those browser semantics; it is still preferable to UI automation.

## Operating discipline

Create one inactive temporary tab/target for a sequential workflow. Record its ID, reuse it, and close only that owned tab; never repurpose or close the user's existing tabs unless explicitly requested. Use a persistent debugger attachment for multi-command loops and release it in cleanup. Use separate tabs only for truly concurrent captures because a tab can have only one active capture.

Keep normal cache behavior unless the task specifically needs bypass/reload semantics. Preserve browser-managed credentials with `credentials=include`; never print cookie, authorization, or token values merely to prove authentication. Verify authentication through status, redirect behavior, and the presence—not the value—of sensitive headers.

Use structured, bounded output (`-j`, `-o meta`, `jq`, `--head`, `--limit`) and write large bodies to files. Never print raw `live.json`, a signed query, generated `rep curl`, or binary data into the tool transcript. After a capture, inspect `rep summary`, narrow with `rep list`/`rep search`/`rep js`, then request only one needed body or field. Archive with `rep save --note` when the capture has future value.

For an unfamiliar SPA, target roughly one navigation, one captured action, and one download:

1. Capture the initial route once with `--keep-tab`.
2. Use `rep js`, bounded `rep search --in response`, and targeted `rep body --head` to find route bundles, indexes, fetch/action code, and codecs. Do not dump every script or the entire capture.
3. Query a discovered public/index endpoint inside the existing tab and select one small test item.
4. Import or call the site's own client module/codec in `rep browser action`; capture its final file request instead of guessing serialization.
5. Select the final GET from bounded metadata and run `rep browser download` directly to the requested path with semantic validators appropriate to the artifact.

## Verification

Verify observable results through CLI/data state:

- bridge health and zero leaked attachments/captures via `rep browser status -j`;
- target/tab ownership via `rep arc targets -j` and `rep browser tabs -j`;
- HTTP status, final URL, request provenance, error text, body size/hash, and expected domain counts via `rep`/`jq`;
- native identity with `rep browser probe` when bot-visible state matters;
- downloaded file completion, size, type, mode, and checksum from the filesystem.

Treat HTTP success alone as insufficient when the task expects particular content. Validate a semantic marker, schema, file type, or checksum without dumping secrets.

## Detailed recipes

Read [references/rep-recipes.md](references/rep-recipes.md) for low-level CDP, ambient capture, authenticated downloads, recovery, analysis, and cleanup. When exact Arc internals matter and the local repository exists, also read `/Users/sijan/code/projects/rep-cli/docs/arc-browser-control.md`.
