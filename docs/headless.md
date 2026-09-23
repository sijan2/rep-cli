# A persistent headless browser for each task

Rep can launch full Chrome for Testing or Chromium in new headless mode with its
existing extension and native bridge. A running browser is reused between
commands; each workspace/task has its own cookies, storage, tabs, capture staging,
and private profile. This does not import accounts or cookies from Arc or Chrome.

```sh
rep --workspace research --task site-a browser headless start \
  --extension /absolute/path/to/rep

rep --workspace research --task site-a browser open https://example.com \
  --browser headless --keep-tab
rep --workspace research --task site-a browser tabs --browser headless
rep --workspace research --task site-a jev select --browser headless \
  --tab TAB_ID --goal 'Find the next page control'
rep --workspace research --task site-a summary --max-bytes 2048

rep --workspace research --task site-a browser headless status
rep --workspace research --task site-a browser headless stop
```

`start` is idempotent for a running task browser. `stop` closes only the process
whose PID, start time, profile argument, and display mode match the saved task
state. The profile remains available for a later `start`. It never clears or
copies a normal browser profile. Browser control refuses to use another task or
the shared Arc bridge when a headless task is stopped or unavailable.

For first-time account setup, explicitly open the same private profile in a
visible window and sign in yourself:

```sh
rep --workspace research --task site-a browser headless start --headed \
  --extension /absolute/path/to/rep
# Complete the site's login manually in that dedicated window.
rep --workspace research --task site-a browser headless stop
rep --workspace research --task site-a browser headless start
```

The final start uses headless mode and retains that task's cookies and storage.
`--headed` applies only to the requested start; it is never inherited as the
default for a later stopped browser. Changing modes while a browser is running
returns a stop-first error. Status JSON reports `headed`, and `--browser headless`
continues to select this same task-owned browser in either mode. Rep does not
copy login data from another profile or perform the manual login.

The browser listens on an automatically assigned loopback debugging port. Rep
verifies its saved browser WebSocket identity before connecting. Native messaging
registration is written inside the new profile, not into an existing browser's
configuration. Native sockets live in a short, private, task-derived directory
under `/tmp`, because macOS Unix socket paths have a small maximum length.

Binary selection for a newly configured task is:

1. `--binary`, then `REP_HEADLESS_BINARY`.
2. Installed Playwright full-browser caches under
   `~/Library/Caches/ms-playwright/chromium-*/chrome-mac-*/` or
   `~/.cache/ms-playwright/chromium-*/chrome-linux*/`.
3. `chromium`, `chromium-browser`, or `google-chrome-for-testing` on `PATH`.

No download is attempted. `--host` defaults to `rep-host` next to the running Rep
binary. `--extension` or `REP_EXTENSION_PATH` selects the unpacked Rep extension;
within the Rep workspace, `extension/` and the sibling `../rep/` are also checked.
Successful configuration is retained for later starts. Stop the browser before
changing its binary, extension, or host. Native execution currently targets
macOS/Linux; Arc, Chrome's separate headless shell, and regular branded Google
Chrome are not supported by this extension-loading path.

Full Chrome headless supports browser extensions, unlike the separate legacy
headless shell. Chrome for Testing/Chromium retain unpacked extension loading;
branded Chrome removed the relevant launch switch beginning in Chrome 137.
These compatibility constraints come from Chrome's
[headless extension testing guidance](https://developer.chrome.com/docs/extensions/how-to/test/end-to-end-testing),
[extension loading announcement](https://groups.google.com/a/chromium.org/g/chromium-extensions/c/1-g8EFx2BBY),
and [native messaging documentation](https://developer.chrome.com/docs/extensions/develop/concepts/native-messaging).

Headless mode does not establish that operations are faster. Reusing one browser
avoids repeated startup, and Jev's existing cache can avoid repeated model calls;
measure the actual site's readiness and capture times before making speed claims.
Sites requiring interactive account setup can use the explicit `--headed`
workflow above. Authentication requirements remain the site's own.

## Local end-to-end verification

Build the CLI and host together, then run the real-browser fixture:

```sh
go build -o /tmp/rep-body-cli .
go build -o /tmp/rep-body-host ./cmd/host
REP_BINARY=/tmp/rep-body-cli REP_HOST_BINARY=/tmp/rep-body-host \
  python3 scripts/verify_body_capture.py
```

The script serves only loopback HTTP, creates a temporary task/profile/data
directory, and stops its browser and removes its fixtures afterward. It checks
complete gzip JSON, chunked NDJSON, finite SSE, binary data, and an unfinished
SSE prefix. It verifies capture states, decoded byte counts and SHA-256 digests,
bounded `body --info`/`--save` JSON, exact private saved artifacts, JSON pointers,
application record boundaries, strict rejection of incomplete bodies, and earlier
archive retrieval after later captures. Add `--with-jev` to make one configured
Jev API call selecting a harmless fixture button; credentials are never printed.

One measured run on 2026-09-20 used locally installed Chrome for Testing
153.0.8010.12 on macOS. Cold start took 0.834s; reusing the same browser took
0.032s; opening and capturing the fixture took 0.404s. The response captures were:

| Fixture | Captured decoded bytes | Capture time |
| --- | ---: | ---: |
| gzip JSON (2,162 bytes on the wire) | 2,109,569 | 0.852s |
| Chunked NDJSON, 2,000 records | 52,890 | 0.527s |
| Finite SSE, two events | 83 | 0.515s |
| Binary response | 2,304,000 | 1.043s |
| Unfinished SSE, explicitly partial | 266 | 2.249s |

The optional Jev selection identified the intended button in 0.498s. These are
single-run local fixture measurements, including CLI/bridge/capture work, not
performance claims about remote sites or other browser approaches.
