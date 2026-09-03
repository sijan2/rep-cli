# rep CLI browser/network recipes

Use this reference only for the mode needed by the task. The live command contract from `rep describe browser -j`, `rep describe arc -j`, and command-specific `--help` remains authoritative.

## Contents

- Health and recovery
- Fast authenticated retrieval
- Full navigation capture
- Ambient cross-tab traffic
- Low-latency page and worker control
- Large and complex downloads
- Capture analysis and replay
- Cleanup and verification

## Health and recovery

Inspect without mutation:

```bash
rep browser status --browser arc -j
rep browser doctor --browser arc -j
rep arc status -j
```

Healthy state has one responsive bridge, a valid native-host manifest, `connected=true`, `active_captures=0` before work, and ArcCore CDP on loopback. Recover narrowly:

1. If only extension source is stale, run `rep arc reload-extension -j`, then poll `rep browser status -j` until a new responsive host PID appears.
2. If the native manifest is missing/invalid, inspect `rep browser install --help`, install only the requested browser manifest, and recheck doctor.
3. If Arc is not running, use `rep arc launch -j`.
4. If Arc is running without the required fixed loopback flags and the task truly needs browser-process CDP, use `rep arc launch --restart -j`; this is a graceful application restart, so do not do it merely as a generic retry.

Never weaken Arc's hardened runtime or edit Secure Preferences as routine recovery. The supported loopback CDP and extension bridge are the primary surfaces.

## Fast authenticated retrieval

Let `browser fetch` choose or create a same-origin inactive tab. This avoids mutating an unrelated user tab:

```bash
rep browser fetch 'https://github.com/settings/profile' \
  --browser arc --cache default --max-body 393216 -j
```

For POST/JSON:

```bash
rep browser fetch 'https://example.com/api/action' \
  -X POST -H 'content-type: application/json' \
  --data '{"enabled":true}' --browser arc -j
```

Use `--tab` only after confirming the tab is already on the same origin. If `origin_mismatch` occurs, omit `--tab`; do not navigate the user's tab just to satisfy the fetch. HttpOnly cookies remain browser-managed.

Avoid emitting the response body into the tool transcript when it may be large or private:

```bash
rep browser fetch "$URL" -j | jq '{status:.response.status, ok:.response.ok, redirected:.response.redirected, requests, response_bodies, duration_ms}'
rep list --primary=false --pattern 'expected-path' --limit 5 -o meta
rep body <request-id> --save
```

Validate signed-in behavior without printing secrets or reading raw `live.json`:

```bash
rep list --primary=false --pattern 'expected-path' --limit 5 -o meta
rep detail <request-id> -o meta -j | jq '{status,has_auth,auth_type,capture_source,tab_id,response_body_truncated,response_body_error}'
```

## Full navigation capture

Use a background tab when navigation, subresources, JavaScript, service workers, or redirect chains matter:

```bash
rep browse 'https://www.google.com/search?q=example' \
  --browser arc --keep-tab --timeout 30s -j
```

Record the returned tab ID. Inspect before reading bodies:

```bash
rep summary -j
rep list --primary=false --api --limit 30 -o meta
rep list --primary=false --errors --limit 30 -o meta
rep chain <request-id> -j
```

Reuse the same owned tab for sequential routes instead of creating a tab per
route. Supply a referrer only when the site's flow requires one:

```bash
rep browse 'https://example.com/next' --browser arc --tab <owned-tab-id> \
  --referrer 'https://example.com/start' --timeout 30s -j
```

Close the returned tab when finished:

```bash
rep browser close <owned-tab-id> --browser arc -j
```

If the command created and automatically closed its temporary tab, do not issue another close.

## Ambient cross-tab traffic

Ambient watch captures normal `webRequest` traffic across materialized tabs, including completed requests, redirect hops, and failures. It intentionally omits response bodies; use an explicit CDP capture for bodies.

```bash
rep browser watch start --browser arc -j
rep browser watch status --browser arc -j
# Trigger work through rep/Arc CDP, another CLI integration, or wait for the requested external action.
rep browser watch stop --browser arc -j
```

Start/stop is idempotent. An unfinished ambient session survives extension reload and native-host reconnect. Always stop it in a `finally`-style cleanup unless continuous monitoring was requested. Do not mix ambient and explicit capture expectations: `browse`/`fetch` switches the live session to isolated CDP capture.

Use ambient data efficiently:

```bash
rep summary -j
rep domains -j
rep list --primary=false --since 5m --limit 50 -o meta
rep list --primary=false --status-range 3xx -o meta
```

## Low-latency page and worker control

List both browser-process and extension-visible targets:

```bash
rep arc targets -j
rep browser targets --browser arc -j
rep browser tabs --browser arc -j
```

For a multi-command page loop, attach once. A page target ID canonicalizes to its tab ID, so capture sessions can reuse the attachment:

```bash
rep browser attach --target <page-target-id> --browser arc -j
rep browser cdp Runtime.evaluate --target <page-target-id> \
  --params '{"expression":"document.title","returnByValue":true}' -j
rep browser cdp DOM.getDocument --target <page-target-id> -j
rep browser detach --target <page-target-id> --browser arc -j
```

Use `rep browser eval` for concise renderer expressions. Put long JavaScript or CDP parameter objects in a file and pass `@absolute-path` to avoid quoting damage.

When the expression triggers network work, combine evaluation and capture:

```bash
rep browser action @/absolute/path/action.js --browser arc --tab <owned-tab-id> \
  --user-gesture --max-body 393216 --max-result 65536 --timeout 30s -j
```

The resulting action capture replaces `live.json` atomically and includes
bounded response bodies. Return only identifiers/status from the expression;
`--max-result` independently prevents an accidental body or binary value from
flooding Native Messaging. Use ordinary `eval` only when no network capture is
needed.

Create an inactive real-profile tab without UI or browser-process CDP:

```bash
rep browser create about:blank --browser arc -j
```

Use the returned tab ID with bridge-native tab CDP:

```bash
rep browser cdp Runtime.evaluate --browser arc --tab <owned-tab-id> \
  --params '{"expression":"({title:document.title,webdriver:navigator.webdriver})","returnByValue":true}' -j
```

Close only the tab created by the task:

```bash
rep browser close <owned-tab-id> --browser arc -j
```

When bot-visible state matters, use `rep browser probe --tab <tab-id> -j`. Expected native-profile properties include `navigator.webdriver=false`, a native getter, normal UA client hints, and consistent WebGL/timezone/screen data. Do not overwrite these values.

## Large and complex downloads

### Small/medium response already captured

Find the request without dumping all bodies, then save it:

```bash
rep list --primary=false --pattern 'download|export|artifact' --limit 10 -o meta
rep body <request-id> --save
```

Verify with `stat`, `file`, and a checksum appropriate to the task.

### Captured final GET (preferred)

After a captured action or navigation reveals the final file GET, stream it
without printing the URL, signed query, cookies, headers, or body:

```bash
rep list --primary=false --pattern 'download|export|artifact|\\.zip|\\.ipa' --limit 10 -o meta
rep browser download <request-id> '/absolute/output/artifact.bin' -j
```

`rep browser download` keeps the signed URL and credentials inside rep's local
data plane, creates one inactive bridge-owned tab, streams with browser-managed
credentials, writes a mode-0600 part file, validates it, and publishes
atomically without overwrite. For an IPA, require both a plausible size and ZIP
magic so a quota/login/error document cannot be published as the artifact:

```bash
rep browser download <request-id> '/absolute/output/app.ipa' \
  --min-bytes 1048576 --expect-magic zip -j
```

Use `--overwrite`, `--allow-empty`, or `--allow-html` only when the task
explicitly requires them.

### Browser-bound GET through Arc

Use the bundled downloader. It creates an inactive temporary tab with `rep browser create`, keeps one `chrome.debugger` attachment, loads the resource with browser credentials, drains its CDP IO stream in bounded chunks, validates the completed part file, writes a mode-0600 output atomically, and closes the stream/attachment/tab.

```bash
/Users/sijan/.codex/skills/rep-browser-network/scripts/rep_arc_download.py \
  'https://example.com/protected/export.zip' \
  '/absolute/output/export.zip' --min-bytes 65536
```

The parent directory must already exist. Existing output is refused unless `--overwrite` is explicitly supplied. Binary-looking output names (including `.ipa` and `.zip`) reject HTML by both response type and body sniffing unless `--allow-html` is explicit. Options include `--min-bytes`, `--timeout`, `--no-cache`, `--omit-credentials`, `--allow-empty`, and a bounded `--chunk-size`. The script emits only status, path, byte count, content type, and SHA-256—not cookie values or body content.

This is the fallback when `rep browser download` is unavailable or cannot consume the needed URL.
It was validated against ArcCore with persistent
`Network.loadNetworkResource` + `IO.read`; do not replace it with
`Browser.setDownloadBehavior`, because Arc's custom download layer may accept
that CDP command without publishing a file.

### POST that yields a file

Use `rep browser action` to execute the site's native POST/client codec and
capture the resulting final GET. Download that GET with `rep browser download`.

Only when the response body itself is the file and no GET exists, capture the
successful POST and keep credentials out of generated text:

```bash
rep auth --save -d <download-domain>
source "$(rep auth --env -d <download-domain>)"
rep curl <request-id> --use-vars
```

Execute an adapted CLI transfer with an explicit output path, redirects enabled
only as required, retries bounded, and status/size/checksum verification. Do
not print the expanded command, signed URL, or environment. `rep curl` is not a
secret-safe display surface for signed queries.

## Dynamic SPA route mining

Use captured metadata to narrow before reading code:

```bash
rep js -j
rep search 'fetch\\(|/api/|download|action|serialize|encode|decode' \
  --in response --type script --context 240 --limit 20 -j
rep body <route-script-id> --head 12000
```

Prefer the page's own module over a hand-reimplemented protocol. In one
`browser action`, dynamically import the discovered chunk, call its exported
codec/action, return only a bounded summary, and let the action capture record
the request. This avoids trial POSTs based on guessed JSON/form encoding and
avoids many `Runtime.getProperties` closure-walk round trips.

## Capture analysis and replay

Start broad, then narrow:

```bash
rep summary -j
rep domains -j
rep list --primary=false --budget minimal --limit 100
rep list --primary=false --api --limit 50 -o meta
rep list --primary=false --interesting --limit 50 -o meta
rep detail <request-id> -o meta
rep body <request-id> --head 5000
```

Do not inspect a complete `live.json` object to learn its schema: it may contain
cookies and signed URLs, and a single request can consume the entire tool-output
budget. `rep detail`, `rep search`, `rep extract`, and `rep get` are the bounded
data-plane surfaces.

Use `rep get` for one field. Sensitive fields should be consumed by a command or compared as a hash/presence check, not echoed. Use `rep replay` only when replay/mutation is part of the requested task; it is a real external request. Archive valuable captures:

```bash
rep save --note 'concise-purpose' -j
```

## Cleanup and verification

Before finishing:

1. Stop ambient watch unless continuous monitoring was requested.
2. Detach every persistent target attached by the task.
3. Close every temporary tab/target created by the task and no others.
4. Verify `active_captures=0` and `attached_targets=0`.
5. Confirm expected files and capture data exist and have restrictive permissions when they may contain session material.

```bash
rep browser status --browser arc -j | jq '{connected,active_captures,attached_targets,ambient_watching,native_host_pid}'
rep browser doctor --browser arc -j | jq '{healthy,bridge_count:(.bridges|length)}'
```

Do not delete or overwrite unrelated captures, tabs, browser data, profiles, downloads, or user files during cleanup.
