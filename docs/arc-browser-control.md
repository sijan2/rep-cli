# Arc browser control architecture

## Validated build

- Arc `1.161.1` (`85803`)
- Arc release commit `6e7a748657d4376c3b45b7d254f5f79eb7ee456f`
- ArcCore / Chromium `151.0.7922.174`
- DevTools Protocol `1.3`, V8 `15.1.206.23`
- macOS arm64, tested on Apple M1 Max

`rep arc status` reads these values from the installed app and the live
ArcCore DevTools endpoint. Treat every offset below as exact-build-only.

## Why there are two control planes

Arc embeds Chromium but does not model its entire sidebar as Chromium tabs.
Only materialized `WebContents` appear in `chrome.tabs` or CDP target lists;
Arc's Swift/ADK layer owns lazy sidebar items, spaces, and restoration.

The reliable design therefore uses both native Chromium control planes:

1. `rep arc` talks to ArcCore's browser-process DevTools WebSocket. It handles
   `Browser.*` and `Target.*`, creates real page targets, inspects the exact
   runtime, and reloads rep+ without opening `arc://extensions`.
2. `rep browser` talks to rep+ over Native Messaging and a mode-0600 Unix
   socket. rep+ uses `chrome.debugger` against real Arc tabs for Network,
   Runtime, DOM, Input, Page, and Emulation commands. It retains Arc's normal
   profile, cookies, service workers, extensions, proxy, TLS stack, GPU, and
   device fingerprint.

No workflow launches `--headless` or `--enable-automation`. Arc uses a fixed,
nonzero loopback debugging port and an exact allowed WebSocket origin.

```text
rep CLI
  |-- rep arc ------ loopback WebSocket ----> ArcCore browser target
  |                                           |-- Browser.*
  |                                           `-- Target.*
  `-- rep browser -- mode-0600 Unix socket --> rep-host
                                                |
                                                `-- Native Messaging --> rep+
                                                                        |
                                                                        `-- chrome.debugger --> page target
```

## Exact-build reverse-engineering evidence

The arm64 Arc executable contains Swift reflection metadata for:

- `ADKClient.devToolsWebSocketDebugURL`
- `ADKClient._resolveDevToolsProtocolRequest`
- `ADKClient._getDevToolsFor`
- `ADKClient._executeJavaScript`
- `_navigateToURLViaDevToolsAndWaitForPageLoadEvent`
- `_addPreloadScriptViaDevTools`

The user-facing binary also contains the hidden developer action
`Copy WebSocket Debug URL` and the diagnostic text requiring
`--remote-debugging-port=<number>`.

ArcCore's Objective-C metadata exposes these build-specific classes and
method offsets in the arm64 slice:

| Class | Method | Offset |
|---|---|---:|
| `ArcBrowserApplication` | `resolveDevToolsProtocolRequest:withJSONString:` | `0x03f90c3c` |
| `ArcBrowserApplication` | `devToolsWebSocketDebugURL` | metadata entry `0x07a40c14` |
| `ArcBrowserContextExtensions` | `reloadExtension:` | `0x03fa0810` |
| `ArcBrowserContextExtensions` | `runJSInExtensionBackgroundPage:js_code:` | `0x03fa0d60` |
| `ArcBrowserContextExtensions` | `installExtensionFromUnpackedPath:completion:` | `0x03fa0ae4` |

Those methods confirm that browser-level CDP and extension-background
execution are first-class ArcCore surfaces. The implementation uses the
public protocol equivalents instead of process injection, which is blocked by
Arc's hardened runtime even for a root Frida client.

## Launch and inspect without UI automation

```bash
rep arc launch --port 9222 --restart
rep arc status --port 9222 -j
rep arc cdp Browser.getVersion -j
rep arc cdp Target.getTargets -j
rep arc cdp Runtime.evaluate --target <target-id> \
  --params '{"expression":"document.title","returnByValue":true}' -j
rep arc reload-extension -j
```

The launch command uses:

```text
--remote-debugging-address=127.0.0.1
--remote-debugging-port=9222
--remote-allow-origins=http://127.0.0.1:9222
```

Do not use `--remote-allow-origins=*`. With a fixed debugging port,
`DevToolsActivePort` may still contain an older ephemeral endpoint; query
`/json/version` on the configured port instead.

## Real-profile page control

```bash
rep browser status --browser arc -j
rep browser tabs --browser arc -j
rep browser targets --browser arc -j
rep browser reload-extension --browser arc

# Create a task-owned inactive tab without navigation capture or ArcCore CDP.
rep browser create about:blank --browser arc -j

# Full sessioned navigation and network/body capture.
rep browse https://github.com --browser arc --keep-tab -j

# Reuse an owned tab and preserve navigation provenance without another tab.
rep browse https://github.com/settings/profile --browser arc --tab <tab-id> \
  --referrer https://github.com/ -j

# Run a page-native action while CDP captures every resulting request and body.
rep browser action @action.js --browser arc --tab <tab-id> \
  --user-gesture --settle 1500ms --max-body 786432 --max-result 65536 -j

# Bot-visible identity from the real renderer.
rep browser probe --browser arc --tab <tab-id> -j

# Persistent attachment removes attach/detach overhead across action loops.
rep browser attach --browser arc --tab <tab-id> -j
rep browser cdp DOM.getDocument --browser arc --tab <tab-id> -j
rep browser cdp Input.dispatchMouseEvent --browser arc --tab <tab-id> \
  --params '{"type":"mousePressed","x":300,"y":240,"button":"left","clickCount":1}' -j
rep browser eval 'document.title' --browser arc --tab <tab-id> -j
rep browser detach --browser arc --tab <tab-id> -j

# Capture ordinary browsing from every materialized Arc tab/domain.
rep browser watch start --browser arc -j
rep browser watch status --browser arc -j
rep browser watch stop --browser arc -j

# Credentialed same-origin request in the browser renderer.
rep browser fetch https://github.com/settings/profile --browser arc \
  --cache default --max-body 65536 -j

# Read a signed URL from a mode-0600 file instead of placing it in argv.
rep browser fetch @/private/tmp/signed-url --browser arc --headers-only -j

# Select a stable request handle directly from the capture-producing command.
rep browser action @action.js --browser arc --tab <tab-id> -j \
  | jq '{terminal_outcome,captured_requests}'

# Stream a captured final GET to disk without exposing its signed URL or cookies.
rep download <request-id> /absolute/path/artifact.ipa --timeout 2m \
  --min-bytes 1000000 --expect-content-type application/zip \
  --expect-content-type application/octet-stream --expect-magic zip -j

# If curl is rejected, stream the same captured GET through the real profile.
rep browser download <request-id> /absolute/path/artifact.ipa --timeout 5m \
  --min-bytes 1000000 --expect-magic ipa -j
```

For an attached page, raw CDP round trips measured about 20 ms through the
CLI/native bridge. Attachments are released by `rep browser detach`, tab close,
extension reload, or browser exit. A page target ID is canonicalized to its
Arc tab ID so the same attachment is reused by subsequent capture sessions.

Ambient watch is intentionally dormant when disabled, avoiding request-body
parsing overhead during normal browsing. When enabled, it records completed,
failed, and redirect-hop requests through `webRequest`; its state and unfinished
session survive extension/native-host restarts and unexpected host reconnects.
Start/stop operations are idempotent, and reconnect backlogs retain lifecycle
control messages ahead of bounded request data. Explicit `browse`, `fetch`, and
`action` sessions switch to CDP so response bodies and protocol diagnostics are
available. `action` evaluates in the real page and seals one isolated capture,
which avoids separate evaluation, watch, and request-list round trips. Its
serialized evaluation result has an independent byte cap so scripts can return
small metadata without accidentally sending response bodies over Native
Messaging. A bounded minimum observation window (`--settle`, 1500 ms by
default) keeps the session open for delayed browser callbacks before the normal
network-idle interval is allowed to seal it.

Navigation, fetch, and action results also carry a chronological
`captured_requests` array. Those descriptors expose only `sequence`, `id`,
`method`, `status`, captured `body_bytes`, an optional truncation flag, and an
optional intentional-cancellation label; URL, query, header, and body data
remain in the sealed capture until explicitly requested by ID.

When an action response contains a framework redirect that leads through HTTP
redirects to a terminal 2xx response, the result also includes a safe
`terminal_outcome`. It identifies the source, chain, and terminal request by
capture ID, distinguishes `download_chain` from `redirect_chain`, and counts
later form failures separately. This keeps a duplicate single-use challenge
submission from hiding a download that already completed. Redirect locations,
signed queries, response bodies, and form-error contents are never copied into
the outcome.

An `@file` navigation/fetch URL is sensitive by default. JSON and text output
omit request/final URLs, response headers, response bodies, and URL-bearing
errors while retaining status, byte counts, capture IDs, and explicit
redaction markers. The full request remains only in the sealed local capture.
Direct URL operands retain their existing verbose output for backward
compatibility. With `--headers-only`, the deliberate body cancellation is
reported via `ignored_cancellations` rather than `failed_requests`; genuine
transport failures are still counted.

`rep download` replays only a captured final `GET`. It passes signed URLs,
cookies, and headers to curl over stdin rather than argv or stdout, strips HTTP/2
pseudoheaders, stale range and content-encoding headers, ignores ambient curl
configuration, disables redirects, retries bounded transient failures, verifies
the byte count, hashes the result, and atomically publishes a mode-0600 file
without overwriting by default. Before publishing, it can enforce a minimum byte
count, one of several declared content types, and a named or hexadecimal file
magic. It also sniffs and refuses HTTP 200 HTML error pages when the destination
has a known binary extension. JSON and compact output both report declared and
detected content types without displaying response bytes.

`rep browser download` is the browser-bound counterpart. It creates one
inactive `about:blank` tab through rep+, attaches with `chrome.debugger`, calls
`Network.loadNetworkResource` with browser-managed credentials, drains the IO
stream in bounded chunks, and closes the stream/attachment/tab before atomic
publication. It uses only the Native Messaging bridge, so it still works when
Arc is already running without a loopback browser-process debugging port.
Changed unpacked-extension source can likewise be loaded through
`rep browser reload-extension`; the old worker posts the acknowledgement,
schedules `chrome.runtime.reload()`, and the CLI verifies that a replacement
Native Messaging bridge reconnects.

## Browser identity and lifecycle

Before navigation or raw tab control, rep+ applies supported CDP state only:

- `Page.setWebLifecycleState(state=active)`
- `Emulation.setFocusEmulationEnabled(enabled=true)`
- `Emulation.setIdleOverride(isUserActive=true, isScreenUnlocked=true)`
- `Emulation.setAutomationOverride(enabled=false)`

It does not rewrite JavaScript globals, spoof a user agent, patch canvas, or
replace native property getters. On the validated Arc build, a background
Google page reported:

- `navigator.webdriver === false`
- native `Navigator.webdriver` getter
- `document.visibilityState === "visible"`
- `document.hidden === false`
- `document.hasFocus() === true`
- native Arc/Chromium UA Client Hints, WebGL renderer, screen, timezone,
  plugins, language list, CPU count, and device memory

This keeps network and renderer identity internally consistent, unlike a
separate headless browser or copied-cookie HTTP client.

## Sessioned network validation

Validated against the live profile:

- Google navigation: 47 CDP requests, 27 response bodies, eight domains,
  completed in about 3.1 seconds without timeout.
- GitHub `/settings/profile` browser fetch: HTTP 200, no redirect, a real
  browser-managed Cookie header, 115 captured requests, and 106 response
  bodies. Cookie values never crossed the CLI result.
- A same-origin captured action returned network-idle with one request, one
  response body, and no attachment or capture left behind after its owned tab
  closed.
- A captured decrypt.day final GET streamed 67,101,933 bytes to a mode-0600 IPA;
  its SHA-256 was
  `45f4b6a8f84fd1943fe8753fa8f4bb619188ff86cabb7feef7ed2e3a026759c5`,
  matching the original artifact without printing the signed query or cookie.
- Explicit sessions contained only `capture_source=cdp`; ambient webRequest
  events were sealed out of the capture.
- Ambient validation preserved both hops of an HTTP 302 chain and recorded a
  refused connection as `net::ERR_CONNECTION_REFUSED` with status zero.

`live.json` is written atomically with mode 0600. The native host batches disk
writes and keeps the last finished session after Arc or the extension exits.

## Extension metadata

Arc stores unpacked-extension registration in:

```text
~/Library/Application Support/Arc/User Data/Default/Secure Preferences
```

For the validated installation, location `4` points to the source directory
and grants `debugger`, `nativeMessaging`, `storage`, `tabs`, `webRequest`, and
`<all_urls>`. The native-host allowlist remains the authority for which
extension ID may start `rep-host`.

Use `rep browser install` to update the native-host manifest. Use
`rep arc reload-extension` to load changed source; direct editing of Secure
Preferences while Arc is running is neither needed nor reliable.
