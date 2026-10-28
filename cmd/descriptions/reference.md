# Page references for everyday research

Start with one command in the signed-in browser:

```sh
export REP_WORKSPACE=project REP_TASK=unique_task
rep browser reference https://www.apple.com/
rep browser reference https://canvas.unm.edu/courses/72971
rep browser reference 'https://docs.google.com/document/d/DOCUMENT_ID/edit?tab=t.0'
```

Set those variables separately for each agent's launch. Scope flags remain an
alternative; there is no shared current task. Results are plain JSON by default;
`--raw-json` is accepted, and `--envelope` explicitly requests an envelope.

Use `page URL` when you only need text and controls. Use `reference URL` when
you want enough context to research, study, or build something: readable
content, source links, measured layout, fonts, CSS formulas, responsive rules,
asset URLs and dimensions, navigation/paint timings, and a viewport image.
No model, API-key discovery, network capture, generated JavaScript, or
help/status/tabs sequence is needed. Arc uses the existing login. A URL creates
an inactive task-owned tab and closes it after a completed saved observation.
`--keep-tab` retains it for more live inspection. Pending reads keep the owned
tab for their exact continuation. `--tab ID` reads an explicit existing tab and
preserves it unless it is this task's pending reference tab. URL plus tab
navigates it. `tab_state` reports cleanup; browser beforeunload handling is
preserved, and an unresolved close is disclosed instead of retried.

The result contains a compact preview and private artifacts: `snapshot.json`,
`content.md`, and `viewport.jpg` (JPEG quality 80, device resolution) when capture fits the budget. Use `rep browser screenshot --tab ID --format png` when a lossless image is required. `reference.id`
names the snapshot inside this workspace/task. Follow `next` for remaining
text, or copy one of `reference.read_sections` for a specific topic. These
commands read the saved observation with zero browser or model requests:

The preview provides body and primary-headline typography plus matching authored
headline CSS, including responsive conditions. Its CSS math examples connect
authored expressions and conditions with an element's measured CSS/device pixel
widths. Copy the supplied `json_pointer` and `item_json_pointers` for citations.
Use the focused `css_math` section for further formulas. Source, conditions and sampling
limits remain explicit; these CSS rules are candidates, not cascade winners.

```sh
rep --workspace PROJECT --task TASK browser reference --saved RETURNED_ID --section styles
rep --workspace PROJECT --task TASK browser reference --saved RETURNED_ID --section fonts
rep --workspace PROJECT --task TASK browser reference --saved RETURNED_ID --section assets
rep --workspace PROJECT --task TASK browser reference --saved RETURNED_ID --section css_math
rep --workspace PROJECT --task TASK browser reference --saved RETURNED_ID --section layout
```

Other sections are `content`, `headings`, `links`, `frames`, `elements`, and
`performance`. A continuation preserves `observed_at`. It does not revisit a
changed page or mix another task's history. `--offset` is a UTF-8 byte offset
for content and a record offset for other sections; use the returned command.
`--max-text` is 256–16384 bytes (default 4096); `--limit` is 1–80 records.
Section projections have a 16 KiB record budget. A larger single record points
to its exact artifact and offset instead of flooding the response. The preview
is sampled; section counts and extraction coverage remain visible.

To inspect one component, use `--selector 'CSS'` with a fresh observation.
It must match exactly one top-document element. The content and element sample
focus on that component; assets/performance still describe the page. Selectors
in design records describe that observation, not guarded input references.
`selector_unique` reports uniqueness at observation time. A pending continuation
preserves the selector and screenshot preference.
Use `page` and `do` for interaction.

Google Docs uses a same-session text export for the selected document tab.
Toolbar labels and the canvas renderer are not substituted for document text.
`content.status: empty` requires a successful text/plain export reaching EOF;
timeouts, rejected HTML/login responses, and access failures remain pending or
unavailable. Export URLs in results are stable; signed download redirects stay
inside the browser. Text exports cover document text, not images, comments,
suggestions, unsaved edits, or every document tab. Other pages use their
rendered main content, with same-origin iframe text retained separately.
Google Docs design samples describe its editor UI, not document glyph fonts or
rich formatting. Document text takes priority over scanning the editor's CSS.
Virtualized, hidden, unopened and inaccessible frame contents are not claimed
as complete site content. The 512 KiB content bound is explicit.

Design evidence includes computed CSS and bounding boxes in CSS pixels, root
font size, device pixel ratio, rem/viewport/parent ratios, and matching authored
rules as serialized by CSSOM. CSS math retains `var`, `clamp`, `min`, `max`,
percentages and responsive conditions where present. Rule order and active
conditions are retained; matching rules are candidates, not inferred cascade
winners. Inaccessible stylesheets, bounded rule scans and omitted matches are
counted. The default design sample is the top document; frame/shadow styling
is not included. Font stacks and loaded FontFaceSet entries do not establish
the exact fallback used for every glyph. Asset metadata describes observed
URLs and browser dimensions; asset file bytes are not downloaded.

Paint and navigation times describe one observed navigation, not FPS, a speed
benchmark, or a promise about another device. Missing paint entries are unknown.
Cross-origin timing restrictions and caching affect transfer sizes. Screenshots
are taken after the DOM/style observation, so animated pages may change between
them. Screenshot failure is disclosed independently of content extraction.

The initial browser-work budget is two seconds. Slow network or document
exports return `loading` and an exact continuation in the owned tab; they are
not reported as finished. `--timeout 100ms..60s` changes the budget.
`--screenshot=false` skips image capture. Process startup, artifact writes and
bounded cleanup are outside the browser budget; measure external wall time for
a latency claim. `ready` means the requested observation is available with the
reported coverage, not that an arbitrary research project is complete.
Close only tabs created by this task when finished.
