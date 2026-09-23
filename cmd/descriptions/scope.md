# rep workspace / rep scope

Rep's data scope is process-local: workspace plus an explicit task. Each task
has separate live data, archives, primary/ignore/mute settings, notes, and context
checkpoints. There is no global "current workspace" for agents to overwrite.

```sh
rep --workspace shopping --task ebay-review scope -j
rep --workspace shopping --task ebay-review summary --max-bytes 4096
rep --workspace applications --task form-2027 summary --max-bytes 4096
```

Selection precedence is flags, process environment, then the nearest parent
`.rep/workspace.json`. `rep workspace init NAME` binds the current project to a
workspace without moving or importing existing captures. It does not choose a
task. Set `REP_TASK` in the agent's launch environment, or pass `--task` on every
call. An `export` in one short-lived tool shell may not persist to the next.

Names are lowercase, 1–64 letters/digits/dots/underscores/hyphens, beginning with
a letter or digit. Use a distinct task for each concurrent agent. Resume the
same task intentionally when continuity is needed. Same-task concurrent edits
to settings are not transactional; do not share a task between independent agents.

Data commands reject missing workspace/task selection. `--global` deliberately
opens the old shared dataset; it cannot be combined with explicit scope flags.
Existing global captures and settings remain intact. Scoped commands reject
inherited `REPLIVE_PATH`/`REPANDROID_PATH` overrides that could bypass ownership.
Android/IG acquisition and shared credential-export commands are not supported
inside task scopes; legacy access to these requires `--global`.

## Browser capture ownership

The browser bridge stays shared. Before scoped browse/fetch/action, the CLI
checks that the native host supports immutable capture handoff. Unsupported
hosts fail before browser operations. A successful operation retrieves its
exact sealed capture by host/session identity, verifies digest/count/provenance,
archives it in the task, and publishes its latest live view atomically.

Results carry workspace, task, capture verification, and saved archive IDs.
Large scoped capture responses return at most 16 request descriptors with
explicit omitted counts; the complete capture remains in its archive.
Use a returned saved hash ID to revisit an earlier operation after a later one.

Dataset isolation is not browser isolation or an OS access-control boundary.
Tabs, signed-in accounts, cookies, and browser control are shared in one profile.
Use task-owned tabs and explicit tab IDs; close only the tabs the task created.
Use separate browser profiles for independent login/account state. Scoped
all-tab ambient watching is unsupported.

No task implicitly imports old global captures or another task's history.
Primary domains filter display; they do not claim ownership of a website.
