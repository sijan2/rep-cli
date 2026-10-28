# rep CLI grammar

Choose a resource family and an operation; flags qualify that operation.
The current task's capture is the implicit resource for `list`, `summary`,
`body`, `search`, `stats`, and `group`. These short data commands stay at root.
`identity`, `probe`, `evidence`, and `engine` keep their existing noun–verb
grammars and their distinct proposal, enforcement, verification, and record roles.

## Browser resources

- `browser capture open URL`: navigate and record network traffic.
- `browser capture fetch URL`: make a browser-managed HTTP request and record it.
- `browser capture action EXPR`: evaluate JavaScript and record its traffic.
- `browser element observe|select|select-batch|validate`: inspect or resolve
  page elements; selection does not perform an interaction.
- `browser interact PLAN --apply`: perform an explicit typed interaction.
- `browser navigate`, `create`, `eval`, and `cdp` retain their separate
  control contracts. They do not silently turn into network captures.

`--browser` belongs to the browser parent. Capture's `--timeout`, `--max-body`,
`--protocol-payloads`, and `--webrtc-media` belong to the capture parent.
Element's `--tab`, `--kind`, `--origin`, `--frame`, `--frame-url`, and
`--root-node` belong to the element parent. Family flags work on either side of
the verb, for example:

```sh
rep browser --browser headless capture --timeout 20s open https://example.test
rep browser element --tab 42 --origin https://example.test observe
```

See `rep describe capture` and `rep describe element` for exact defaults and
operation-specific options. Tab lifecycle, archive completeness, evidence
identifiers, and operation outcomes are unchanged.

## Selection and projection

`list --select api,errors` intersects named presets. `--view line|detail`
controls text layout; `--json` controls serialization. `--primary` remains a
task-local display filter, not an authorization policy. See `rep describe list`.

`stats count|unique|distribution FIELD` uses real Cobra subcommands; `group FIELD`
takes one field as data. `--limit` bounds group, count, and distribution output;
unique retains its full values. See their descriptions for
the field grammar, aggregation semantics, and legacy adapters.

## Flag ownership

Only invocation context and output controls are root persistent flags:
`--workspace`, `--task`, `--global`, `--run`, `--output`, `--json`, `--raw-json`,
`--envelope`, and `--no-color` (plus automatic help/version).
No new implicit identity, run, account, or global current-task setting exists.
Use REP_WORKSPACE/REP_TASK in each agent process to avoid repeating identity;
a project binding chooses only the workspace. All scope guards still apply.

Budget and source flags stay on commands that implement them. In particular,
`--offset` may mean bytes, records, or result rows; `--format` may mean image
encoding or body parsing; timeouts have operation-specific meanings and defaults.
`--saved` never becomes an ignored option on live browser operations. A flag
inherited by a family applies to every operation in that family.

## Compatibility

`browse` and `browser open|fetch|action` remain hidden compatibility commands.
`browser select|select-batch|observe|validate` remain hidden compatibility commands.
Each runs the same operation and emits a deprecation note on stderr. The older
`jev select` entry point is also retained with a note. No removal date is set.

Legacy list/Android boolean presets and layouts, stats mode flags and `--top`,
and group `--by` remain hidden adapters; see their descriptions. Do not mix a
canonical selector with its legacy equivalent in the same invocation.
Ignored stats/group positional garbage and contradictory mode selections now
fail explicitly with `invalid_argument`. Help is read-only. Compatibility notes
never enter JSON stdout or become verification evidence.
