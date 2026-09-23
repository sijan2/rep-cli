# rep browser headless

Launch or reuse a full Chrome for Testing/Chromium browser owned by this
workspace/task. A workspace and explicit task are required. No download or
normal-profile cookie import occurs.

```sh
rep --workspace PROJECT --task TASK browser headless start --extension /path/to/rep
rep --workspace PROJECT --task TASK browser open https://example.com --browser headless --keep-tab
rep --workspace PROJECT --task TASK jev select --browser headless --tab ID --goal 'Find the next page control'
rep --workspace PROJECT --task TASK browser headless status
rep --workspace PROJECT --task TASK browser headless stop
```

`start` accepts `--binary`, `--host`, `--extension`, and `--timeout`. It reuses
the same running process; changed configuration requires stop first. `--headed`
opens only the isolated profile for manual account setup. Stop, then start
without --headed to reuse its cookies in headless mode. Rep does not sign in
automatically. Profile data survives stop.

Use `--browser headless` on subsequent browser and Jev commands. Connection
verifies process identity, profile ownership, and the saved loopback debugging
endpoint. It never falls back to shared Arc or another task's browser.

Default executable discovery checks local Playwright full Chromium caches and
Chromium/Chrome-for-Testing on PATH. Regular branded Chrome, Arc, and the legacy
headless shell do not support this unpacked-extension launch path. The current
native implementation supports macOS/Linux. The extension and native host remain
the same capture/control implementation as graphical Rep browsing.
