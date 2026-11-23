# Working on rep

`cmd/rep` is the CLI; `cmd/rep-host` is the native messaging host. Cobra constructors live in `internal/commands`; shared state, output, bridge, headless and browser execution packages live under `internal/`. `VERSION` is the release version.

Read `rep --help` once before browser work. Use only the rep binary built for this checkout, first on PATH, and a private headless `REP_PROFILE` under private XDG data/runtime paths. Never drive the user's real browsers or another agent's tabs for development verification. Use rep for UI checks; do not substitute browser automation libraries or direct protocol scripts. Fixture servers bind port 0 and must be stopped, along with private browsers.

The grammar is verb first. Use `rep page URL`, returned tab/ref handles, and `rep do REF VALUE` for an action. Identity and browser are inferred; a deliberate `--task` override follows the verb. Same-session siblings share an identity and must use handles for isolation. Check `complete` and `page_state`; follow one returned `next` verbatim. A dispatched but unobserved action must be verified without repeating it.

Capture only when needed with `rep page URL --capture` or the action/eval capture switch. Inspect with `rep net`, then one `rep body h_ID`. Requests are bound to their archive; an empty task never falls back to unrelated captures. Preserve redaction before projection, explicit completeness, private artifact permissions and refusal to overwrite an existing body export. Explicit body/eval/cdp `--out`, request export and cookies export preserve raw bytes in user-selected files. Use cookies list/export/import/delete for session work, including HttpOnly cookies.

Use `rep scope` for offline context, `rep tabs` for liveness and ownership, and `rep doctor` for repair. Preserve holder and execute-lease guards. Browser/profile accounts are not isolated by a task name; independent login state requires independent profiles.

Run `gofmt` on Go changes and `go test ./...`. Extension changes require its `npm test`; shared-client and companion changes require their Python suites. Keep the binary buildable with `CGO_ENABLED=0`; specialized tools live under the `rep lab` group. Help is generated from Cobra metadata; maintain at most five local command flags, one inherited post-verb task flag, zero hidden flags and useful examples/defaults in help. Do not hide capabilities or delete useful explanations to meet a byte or total-flag quota.

The source-contract scanner checks documented commands and executable argv. Keep examples in the final grammar, keep the three copies of the versioned skill identical, and remove dated migration-test entries as their shims disappear. Preserve unrelated changes and never install or publish as a side effect of tests.
