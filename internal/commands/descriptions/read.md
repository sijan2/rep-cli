# rep read — bounded page reading for agents

`rep read <url>` renders a URL in the task's browser and returns its readable
content as markdown. It exists so an agent can read a page without pulling
hundreds of kilobytes of markup into context.

## When to use read vs capture

| Need | Command |
|---|---|
| The *text* of a page | `rep read` |
| The *network evidence* (requests, bodies, timing) | `rep browser capture open` |

`read` uses `browser.create`, which navigates **without sealing a network
capture**. A page that is merely heavy stays readable even when a capture byte
budget would abort an archive. `read` therefore never produces captured-request
IDs; use `browser capture open` when you need those.

## Fidelity contract

These rules exist because lossy extraction silently corrupts research:

- `<pre>`/`<code>` are preserved **verbatim** inside fences. Exact payload
  bytes, line endings and indentation survive. Never infer payload validity
  from a lossy transcript.
- Tables become markdown tables with their header row.
- Images emit `![alt](src)`; `data:` URIs are replaced with
  `data-uri-omitted` rather than inlined.
- SVG text (chart labels and values) is emitted as `[svg-text] …`.
- Main-content selection penalizes link-dense containers, so archive sidebars
  and tag clouds do not win over the article body.

Content that is genuinely absent from the DOM — a value that exists only inside
a bitmap image, for example — is absent from the output. `read` does not infer
it. If a document says "one of three tiers:" and no tiers follow, the tiers were
in an image; treat them as unrecovered, not as nonexistent.

## Bounding and navigation

`--max-bytes` (default 20000) bounds the returned content; `--offset` pages
through it. Output states `[document A..B of N chars]` and, when truncated,
prints the exact next command.

For long documents prefer structure over paging:

```sh
rep read <url> --format index            # heading outline, a few hundred bytes
rep read <url> --section "Key Takeaways" # just that section
```

`--section` matches a heading case-insensitively by substring and returns that
heading's body, including deeper subsections, stopping at the next heading of
the same or higher rank.

## Provenance

Every extraction records `requested_url`, the final `url`, `redirected`,
`fetched_at` and `content_sha256`. A redirect is reported, not hidden: a
permalink that silently becomes a generic index is a changed source, not a
successful retrieval. Check `redirected` before citing a page as the original.

Extractions are cached per task, keyed by requested URL. A zero-character
extraction is **never** cached, so a transient render failure cannot poison
later reads. `--refresh` bypasses the cache; `--tab ID` reads an already-open
owned tab and never consults the cache.

## Tabs

Without `--tab`, `read` creates an inactive owned tab and closes it when done.
`--keep-tab` retains it for follow-up `rep browser` work. Reading does not
activate tabs or disturb another task's tab.

## Formats

- `md` (default) — readable markdown
- `index` — heading outline with levels
- `links` — absolute outbound links as `text :: url`
- `json` — full envelope with range, stats and provenance

`stats` reports `code_blocks`, `tables` and `images` recovered, so an agent can
tell a successful text extraction from one that dropped the figures it needed.

## Advanced flags

These flags stay accepted with their existing behavior and are hidden from `--help` to keep the everyday surface small: `--refresh` bypasses the cached extraction.
