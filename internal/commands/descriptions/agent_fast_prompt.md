# Rep browser task routing

Give each agent its own `REP_WORKSPACE` and `REP_TASK` in its launch environment;
subsequent calls need no scope flags. Otherwise pass `--workspace project --task
unique_task` on each call. Never use a shared current task. Reuse returned tab
IDs. Arc uses the authenticated shared profile; `--browser headless` uses the
task's separately started private profile. Close only tabs this task created
with `rep browser close --tab ID` (positional IDs remain supported).
Completed `reference URL` reads close their owned tab automatically. Use
`--keep-tab` for more live inspection; an explicit existing `--tab` stays open.
`page`, `do`, `chat` and `reference` return plain JSON by default.

Reuse a warm tab for repeated reads of one site. Measured in Dia on 2026-10-28:
a cold `reference` of apple.com took 1172 ms (navigation 415, renderer readiness
480, screenshot 219); the same read in a kept tab took 319 ms with the image and
86 ms with `--screenshot=false`. Open once with `--keep-tab`, then read with
`--tab ID`; pass `--screenshot=false` unless the image is needed. Close the tab
with `rep browser close --tab ID` when the task ends.

Observe before you replay. Every live `page` read returns `page_state` (is the
page content, loading, a wall or an error), `stack` (what rendered it) and
`data_requests` (the fetch/XHR endpoints the page itself used). When a task
needs many values from one site, capture the page's own request with
`browser capture open URL` or `capture action`, then `browser capture fanout ID
--matrix ...` replays it in parallel from the same page session. Never guess an
API path, never scrape a rendering when its request is listed, and treat
`page_state.kind` other than `content` as a reason to wait, ask a person, or
stop, not as an empty result.

Choose the command from the requested task:

| Task | First command |
|---|---|
| Read or use a page | `rep browser page URL` |
| Gather project/course/design context, CSS, fonts, assets or timings | `rep browser reference URL` |
| Type into an observed field | `rep browser do REF --tab ID --text TEXT` |
| Click an observed control | `rep browser do REF --tab ID --click` |
| Press a key in an observed control | `rep browser do REF --tab ID --press Enter` |
| Chat with Muse | `rep browser chat https://muse.ai/thread/new --message TEXT` |
| Read a changed page | `rep browser page --tab ID` |
| Resolve a control by meaning | `rep browser element select GOAL --tab ID --raw-json` |
| Find where something is on the visible page from a screenshot | `rep browser locate GOAL --tab ID` |
| Decide whether a page is content, loading, a login wall, consent, challenge, error or empty | `rep browser state --tab ID` |
| Run one captured request with many values in parallel | `rep browser capture fanout ID --matrix 'query.NAME=[a,b,c]' --raw-json` |
| Execute a supplied UI plan and outcome checks | `rep browser interact PLAN.json --tab ID --apply --raw-json` |
| Capture network evidence | `rep browser capture open URL --keep-tab --raw-json` |
| Inspect this task's captured traffic | `rep summary --max-bytes 4096` |

The scope environment or flags above still apply. Start the actual task
directly; the fast path needs no help/status/tabs sequence, model request,
custom JavaScript or generated plan. `page` returns exact labeled references.
Inspect coverage and omission counts. Existing drafts are preserved; `--replace`
explicitly replaces field contents. References expire and fail on changed
documents, nodes or labels. Read the page again after a stale reference.
For truncated page output, follow its exact `next` command to read the remaining
snapshot; avoid repeated first-page reads or invented selectors.

For Canvas/course material and Google Docs, start from the supplied URL in the
signed-in profile. Google Docs text comes from the selected document tab's
authenticated export; check `content.status` and `complete`. A blank export,
unavailable export and toolbar text are different. `reference` saves content,
CSS/layout/font/asset evidence and a viewport image as private task artifacts.
Its preview includes body/headline typography and authored headline CSS with
media conditions. CSS math examples include conditions, element geometry and
exact source pointers; use `read_sections.css_math` for more formulas. Use these
facts before reading large element/style sections.
Its `read_sections` commands retrieve exact saved details without browser work.
`loading` includes one continuation in the existing tab; use it rather than
opening the URL again. Timings are observed page timings, not an FPS benchmark.

For Muse's main conversation use `https://muse.ai/`; `/thread/new` creates a
separate conversation. A pending chat reply includes the exact `next` command
with tab and turn and a 30-second observation budget. Run it once to observe
without resending or repeated short polls. Default initial browser-work
budget is two seconds; pending provider work remains pending. `completed` is
supported completion evidence for the returned operation, not proof of an
arbitrary user goal. Click/key `performed` means dispatch; verify the requested
page outcome. Lost input acknowledgments are `unconfirmed`; never blindly retry.

Use `rep describe page`, `reference`, `chat`, `interact`, or `browser` only when that command's
details are needed. For captured bytes, pin the exact saved archive and inspect
`body ID --saved HASH --info`; empty and incomplete differ. Use artifacts and
bounded reads. `no_capture` describes the task dataset, not site access.
For extended evidence and specialized workflows use `rep agent-prompt --full`.
