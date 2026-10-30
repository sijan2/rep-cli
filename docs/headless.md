# Private browser profiles and recovery

`rep browser start` starts or resumes a private Chromium profile. `REP_PROFILE` is its stable name; XDG data/runtime paths locate its state and sockets. The extension and host come from recorded configuration or `REP_EXTENSION_PATH` and sibling binaries; `REP_HEADLESS_BINARY` selects the browser binary. `rep browser start --headed` allows a person to sign in. A later mode switch reuses the profile subject to the holder guard.

An explicit browser name in `rep browser start arc`, `rep browser start chrome`, or `rep browser start dia` records a real-profile choice rather than launching it. Task choice and profile ID are echoed. Rep never silently starts a private profile while a real-profile bridge is live, or switches accounts when the remembered bridge is down.

`rep scope` is an offline inventory. `rep tabs` tests live liveness and shows ownership. `rep doctor` performs repair: bounded reconnect waiting, manifest repair, guarded private restart, and an extension reload only when the bridge is live, its build digest is stale, and no capture or execute lease is active. It reports a concrete human-only step where needed. The native messaging host is launched by the extension, not by an agent retry loop.

`rep browser stop` releases the private profile and refuses to disrupt another task's holders. Real browsers are never quit by this command. Closing an owned tab also refuses active leases/capture. Tab reaping cannot discard unread form values or dialogs.

Inline healing only retries a known pre-dispatch failure after a recent live bridge. A lost response after possible dispatch is `outcome_unknown`; verify the result without resending an action.

For repository checks, use the freshly built CLI/host, put their bin directory first on PATH, set a unique private profile and XDG paths, and point at the matching extension checkout. Serve synthetic fixtures on port 0, drive them only with rep, and stop both browser and server. Do not invoke a browser binary directly or modify the installed binaries, live manifests, user tabs or other agents' profiles.
