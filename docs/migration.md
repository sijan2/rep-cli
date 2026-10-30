# Agent-first command migration

The final core has 16 verbs, 28 local command flags and one post-verb `--task`. Root help is the full compact contract; verb help lists local flags. Hidden command/flag shims are removed in the final migration, with no permanent advanced layer.

| Earlier intent | Current form |
| --- | --- |
| Read, navigate, create, observe | `rep page URL` or `rep page t3 URL` |
| Page continuation and output tuning | Run the returned p_ cursor next verbatim |
| Click, fill, key, upload | `rep do REF VALUE`, --press, --file |
| Ordered interaction | `rep do @plan.json t3` |
| Script in a page | `rep eval @script.js t3` |
| Screenshot or sweep | `rep shot t3 image.png` or --urls |
| Layout/design reference | `rep reference URL` |
| Capture open/action | --capture on page/do/eval |
| List, summary, search, extract | `rep net` and positional ANDed query terms |
| Detail, pointer, stream | `rep body h_ID`, POINTER or quoted #EVENT |
| New in-page request | `rep fetch URL` |
| Single replay or fanout | `rep replay h_ID`, --set or --matrix |
| Authenticated file | `rep download h_ID file.pdf` or URL PATH |
| Offline inventory | `rep scope` |
| Live tabs/liveness | `rep tabs` |
| Bridge repair | `rep doctor` |
| Private lifecycle | `rep browser start` and `rep browser stop` |
| Owned protocol call | `rep cdp METHOD JSON t3` |

Formatting flags disappear: machine stdout is already one plain JSON object. Scope, browser, tab and byte-budget ceremony is replaced by ambient context, explicit returned handles and durable cursors. A task override follows the verb. Secrets are fingerprinted; raw body export requires an explicit new private file. Eval enables await/user-gesture/REPL semantics. Reads state completeness; actions return deltas and distinguish verified from unobserved effects.

Packet/native capture, media/watch, Android/IG, research engines/probes/audits, Jev configuration and deep frame/vision work move to rep-lab, built with `go build -tags replab`. Use that binary's help for its specialized grammar. Core retains reference and guarded CDP. Automatic attach/detach, journaling and first-party marking have no agent ceremony. Credential-dump helpers, stored principals, chat shortcuts, describe and agent-prompt are retired.

The canonical skill is skills/rep/SKILL.md, mirrored at .agents/skills/rep and .claude/skills/rep in this repository. Personal agent profiles and installed skills require owner-managed updates; repository builds do not rewrite them.

Compatibility test entries have a fixed expiry and must be removed with each deletion. They name exact commands, flags and hidden declarations, rather than waiving whole directories. Historical research harnesses stay pinned to their pre-migration binary until ported.

The migration window is closed: removed commands and flags fail with a structured
usage error. There are no hidden commands, hidden flags, aliases, completion
protocols or migration allowlist entries. Use `REP_TASK=global` only when deliberately
selecting shared legacy device data for rep-lab Android/IG or all-tab watch. The
core has one optional inherited flag, placed after its verb: `--task`.

Operation evidence is recorded automatically for an isolated task. Inspect its
bounded journal with `rep scope`; no run identifier needs to be supplied.
