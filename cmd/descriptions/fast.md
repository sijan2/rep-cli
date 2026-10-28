# Fast page and chat workflows

For project/course/design research that also needs source links, CSS/layout,
fonts, assets, navigation/paint timings, and saved artifacts, start with
`rep browser reference URL`. See `rep describe reference`.

Select `--workspace PROJECT --task TASK` on every call. Dia is the default;
`--browser headless` selects the task's already-running private profile.
These commands use the real browser with no model request or traffic capture.
Alternatively set `REP_WORKSPACE` and `REP_TASK` once in each independent
agent's launch environment. `page`, `do`, `chat` and `reference` return plain
JSON by default, so `--raw-json` is optional; `--envelope` explicitly wraps it.

```sh
rep --workspace demo --task browse browser page https://example.com --raw-json
rep --workspace demo --task browse browser do RETURNED_REF --tab RETURNED_TAB --text 'hello' --raw-json
rep --workspace demo --task browse browser do RETURNED_REF --tab RETURNED_TAB --click --raw-json
rep --workspace demo --task browse browser page --tab RETURNED_TAB --raw-json

rep --workspace demo --task chat browser chat https://muse.ai/thread/new --message 'Reply only PONG. Do not use tools.' --raw-json
rep --workspace demo --task chat browser chat --tab RETURNED_TAB --turn RETURNED_TURN --raw-json
```

For a read, start with `page URL`; it creates an inactive owned tab, waits for
DOMContentLoaded, and returns page text and labeled control references. Reuse
the returned tab. `page --tab ID` reads without navigation. A URL with `--tab`
explicitly navigates that tab. There is no tab discovery, history lookup, or
implicit adoption of a user's tab.
Truncated reads include an exact `next` command with `--cursor`. It returns the
next slice of the same snapshot, preserving `observed_at`; it does not rescan
and repeat the first controls. Cursors expire after two minutes and belong to
one task and document. Changed controls fail and require a fresh page read.
Supported chat pages get a bounded wait for visible UI during fades or startup;
unready UI returns `loading` with an exact read continuation. An application
shell with no text and at most three controls after a cold two-second read
returns `loading` with code `awaiting_hydration` and a continuation whose
longer budget waits for content; a long budget that still finds nothing is
final. A read that fails with `page_changed` right after navigation is retried
once on the same tab (`rebound_after_url_change`), because applications such as
Google Docs rewrite their URL after commit.
Google Docs pages read the selected document tab through a same-session text
export. `content` reports source, scope, HTTP/type validation and completeness.
Empty document content is separate from unavailable/partial exports. Editor
toolbar text is not passed off as document text. Link controls also include
their observed URL. Changing a link target invalidates its action reference.

`do REF` takes exactly one of `--text TEXT`, `--click`, or `--press Enter`.
References expire after two minutes, belong to one task and document, and are
rechecked for DOM identity, parent, label, role, visibility, enabled state and
occlusion. Stale references fail; read the page again. Existing text is preserved
unless `--replace` is explicitly supplied. Text entry verifies exact readback.
Clicks use a native pointer press/release; key events use native browser keys.
They report `performed`, which is dispatch evidence. Read the
page or use `interact` with declared postconditions to verify the actual outcome.

`chat` sends one message through a unique visible composer labeled Message or
Ask, then clicks the unique observed Send or Send message button. Its current
transcript contract covers Muse's observed `data-message-role`
and `data-message-id` markers and explicitly compatible pages. Other pages fail
before input: use `page` and its exact control references. Chat does not guess
an API or reuse previously captured credentials. The Muse URL `/thread/new`
opens a separate side conversation; `/` opens the main conversation.

The default navigation/action/observation budget is two seconds. `--timeout 100ms..60s`
changes that budget; network, hydration and provider inference can exceed it.
There is no promise of a two-second provider response. A new rendered user
message establishes `submitted: true`. Submission evidence persists through UI
re-renders once observed. An optimistic message ID can be replaced by the
server's rendered ID; use `turn` for resumption. Its fresh assistant response is
returned as `reply_received`; it does not establish complete generation. `completed`
requires explicit `data-message-status="complete"` on every returned response
element, or Muse's generation control being observed active and then absent for
200ms after a fresh response. The latter is UI completion evidence, not raw
stream completeness. If that transition was not observed, generation completion
stays unknown. Partial text can be returned with `pending`; truncation is disclosed.
Process startup, output, optional journaling and bounded input/lease cleanup sit
outside that budget. Measure whole-command wall time when evaluating a deadline.

When no reply arrives within the budget, the output remains `pending` and
includes the exact `next` command with tab and turn. Use that command to observe
the original request; it never enters or sends text again. A private task receipt
is written before input. Lost acknowledgments are `unconfirmed`; do not rerun
`--message` to recover. Reloading the page can remove its turn observation state;
the receipt remains, and a missing turn fails instead of sending again.
The returned continuation uses a 30-second observation budget to avoid repeated
short polls through an agent. A completed reply has no continuation command.

`--max-text` bounds UTF-8 text bytes (default 4096; 256..16384). `--limit` bounds
returned page controls (default 40; 1..80). Output exposes omission counts and
iframe count. The read covers the top document and open shadow roots; use
`browser element`/`interact` for scoped frame or semantic selection. Password
values, cookies, headers and response bodies are not included. Editable controls
are projected before buttons and navigation links. Text prefers a unique main
landmark, or the current chat transcript for supported chat pages; older chat
messages omitted from the 12-message window are counted.
Open menus and dialogs are read first, followed by the chat composer and
controls in the viewport. Offscreen controls remain reachable through the
continuation and are counted separately. Coverage distinguishes a live read
from a snapshot continuation and reports remaining controls and text bytes.

With `--raw-json`, results are one JSON document, including failures. Browser
results contain `duration_ms`, `bridge_calls`, and `model_requests: 0`. The model
count measures Rep's own planning requests; Muse's inference is separate.
An unfinished operation includes a concrete next command. `dispatch_state`
separates a preserved/unsent draft, send dispatch, and unknown acknowledgments.
`--run RUN_ID` records the same optional operation journal as other browser commands.
Failed input
is never automatically retried. Browser scope/leases do not isolate accounts;
use task-owned tabs and separate profiles where login isolation is required.


## Page physics

Live `page` reads (not cursor continuations) also return three fields shared by
every site, whatever rendered it:

- `stack`: framework and data-layer fingerprint (for example `nextjs` with
  `rsc_flight`, `react`, `shreddit`, `turbo`, `htmx`, `wordpress`), generator
  meta, embedded state sizes (`__NEXT_DATA__`, Apollo, Redux) and custom
  elements. Embedded state is exact data that needs no DOM scraping.
- `data_requests`: every fetch/XHR endpoint the page issued since navigation,
  from Resource Timing, grouped by origin and path with ids collapsed, with byte
  totals and whether it happened after `DOMContentLoaded`. This is where the
  page's data comes from; capture one such request and replay it with
  `browser capture fanout`.
- `page_state`: in-page signals (`kind`, hints, text characters, visible
  controls, dialogs, password fields, new resources and mutations in a short
  window). `kind` is `content`, `loading`, `login`, `consent`, `challenge`,
  `rate_limited`, `error` or `empty`. For an ambiguous page, `rep browser state
  --tab ID` adds one calibrated Jev judgment about the screenshot.
