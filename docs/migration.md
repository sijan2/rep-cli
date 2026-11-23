# Agent-first command migration

The core uses verb-first commands and one optional post-verb `--task`. Root help includes common workflows and points to specialized tools; command help lists every accepted flag, its defaults, and practical examples. Help has no byte quota: examples and operational details must not be removed merely to meet a token target. Core commands retain at most five local flags, with no hidden advanced flag layer. New capabilities may add visible commands and flags; total verb and flag counts are not frozen.

| Earlier intent | Current form |
| --- | --- |
| Read, navigate, create, observe | `rep page URL` or `rep page t3 URL` |
| Page continuation and output tuning | Run the returned p_ cursor next verbatim |
| Click, fill, key, upload | `rep do REF VALUE`, --press, --file |
| Ordered interaction | `rep do @plan.json t3` |
| Script in a page | `rep eval @script.js t3` |
| Export a script result with original values | `rep eval @script.js t3 --out ./result.json` |
| Screenshot or sweep | `rep shot t3 image.png` or --urls |
| Layout/design reference | `rep reference URL` |
| Capture open/action | --capture on page/do/eval |
| List, summary, search, extract | `rep net` and positional ANDed query terms |
| Detail, pointer, stream | `rep body h_ID`, POINTER or quoted #EVENT |
| Original captured headers/body for external tools | `rep export h_ID request.json` or `--format curl` |
| New in-page request | `rep fetch URL` |
| Single replay or fanout | `rep replay h_ID`, --set or --matrix |
| Authenticated file | `rep download h_ID file.pdf` or URL PATH |
| Inspect cookies for a tab | `rep cookies list t3` |
| Export/import cookie state | `rep cookies export t3 ./cookies.json` and `rep cookies import t3 @cookies.json` |
| Delete one cookie | `rep cookies delete t3 session` |
| Offline inventory | `rep scope` |
| Live tabs/liveness | `rep tabs` |
| Bridge repair | `rep doctor` |
| Private lifecycle | `rep browser start` and `rep browser stop` |
| Owned protocol call | `rep cdp METHOD JSON t3` |
| Export an original protocol result | `rep cdp METHOD JSON t3 --out ./protocol.json` |

Formatting flags disappear: machine stdout is already one plain JSON object. Scope, browser, tab and byte-budget ceremony is replaced by ambient context, explicit returned handles and durable cursors. A task override follows the verb. Normal output fingerprints credential fields; explicit body/eval/cdp exports and cookie exports preserve original values in a new private file. Eval enables await/user-gesture/REPL semantics. Reads state completeness; actions return deltas and distinguish verified from unobserved effects.

Packet/native capture, media/watch, Android/IG, research engines/probes/audits, Jev configuration and deep frame/vision work live under `rep lab` in the one rep binary. Use `rep lab --help` for its specialized grammar and `rep lab COMMAND --help` for each tool's full flags. Core retains reference and guarded CDP. Automatic attach/detach, journaling and first-party marking have no agent ceremony. Legacy credential-dump helpers are replaced by visible, tab-scoped cookie operations and explicit original-value exports. Stored principals, chat shortcuts, describe and agent-prompt remain retired.

For authenticated work, set `REP_PROFILE` to a reusable named private profile and run `rep browser start --headed` to sign in. Use that same profile for later commands. Fetch, replay and download automatically use its browser cookies; cookie export/import can move an existing session between profiles. Use `rep cookies --help` for supported formats and tab scope. Profiles isolate login state; task names only organize work.

The canonical skill is skills/rep/SKILL.md, mirrored at .agents/skills/rep and .claude/skills/rep in this repository. Personal agent profiles and installed skills require owner-managed updates; repository builds do not rewrite them.

Compatibility test entries have a fixed expiry and must be removed with each deletion. They name exact commands, flags and hidden declarations, rather than waiving whole directories. Historical research harnesses stay pinned to their pre-migration binary until ported.

The migration window is closed: removed commands and flags fail with a structured
usage error. There are no hidden commands, hidden flags, aliases, completion
protocols or migration allowlist entries. Use `REP_TASK=global` only when deliberately
selecting shared legacy device data for `rep lab` Android/IG or all-tab watch. The
core has one optional inherited flag, placed after its verb: `--task`.

Operation evidence is recorded automatically for an isolated task. Inspect its
bounded journal with `rep scope`; no run identifier needs to be supplied.
