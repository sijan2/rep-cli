# internal/records test fixtures

Inputs for the records decoder: Reddit server-rendered thread documents, a
Reddit more-comments fragment, Reddit gate and challenge pages, and X
`TweetDetail` GraphQL responses. `facts.json` holds the expected values the
decoder tests assert.

Nothing here is a live capture. The three sanitized fixtures are derived from
private captures that stay outside the repository. The `*.synthetic.*` files
are hand-written, and live captures replace them later.

## Files

| File | Kind | Source |
|---|---|---|
| `reddit_thread_ssr.html` | sanitized | Main-frame document of a public r/programming thread, default sort (`CONFIDENCE`), captured 2026-10-29 by the runtime probe. Raw: `body-2207143399.html`, 3,458,945 B, sha256 `7e58833ecd12e87f02ad62c203d673368a8ba63d5beabd911515c610f9843810`. The thread has 207 comments; 100 are server-rendered. |
| `reddit_thread_ssr_new.html` | sanitized | The same thread with `sort=new`. Raw: `body-1372628643.html`, 3,366,213 B, sha256 `13f784ad5553a2686d4f965733abe1b5a2ee44d562780aa506f13c938c103441`. |
| `x_tweetdetail.json` | sanitized | `TweetDetail` response (page 1) for a public status. Raw: `body-2565534621.json`, 199,410 B, sha256 `d79241a9489d4e16117bb0433d8a4ef883ffa193b7c01e30e11ad06caa7a70cf`. |
| `reddit_more_comments.synthetic.html` | synthetic | Response to one branch loader of `reddit_thread_ssr.html`: the one whose cursor is `cursor_17` (parent `t1_c000066`, `startingDepth=1`, hint 7). |
| `reddit_gated.synthetic.html` | synthetic | An over-18 interstitial: a `shreddit-post` with `comment-count="57"` and no comments, plus an over18 form. This is the case that would otherwise read as drift. |
| `reddit_challenge.synthetic.html` | synthetic | A "Prove your humanity" page. |
| `x_tweetdetail_showmore.synthetic.json` | synthetic | A ShowMore module cursor with `displayTreatment.actionText`, a `TweetWithVisibilityResults` wrapper, a `TweetTombstone`, a quoted tweet, `TimelineAddToModule`, `TimelineReplaceEntry` and `TimelineTerminateTimeline` with `direction: Bottom`. |
| `x_tweetdetail_unknown.synthetic.json` | synthetic | 10 entries, 2 of them with unknown entry typenames (20%, above the 10% drift bound). |
| `facts.json` | generated | Expected facts per fixture. |

`body-3161589895.json` (a `rep body --pointer …/instructions` export of the
same status from another capture) is not a fixture. The privacy check uses it
only as an extra source of values that must never appear here.

## Regenerate and verify

Run from the repository root. The raw paths are private and local.

```sh
B=~/.local/share/rep-cli/workspaces/deep-review/tasks/runtime-probe-1029/body-exports
RAW=(--reddit-ssr "$B/body-2207143399.html" --reddit-new "$B/body-1372628643.html"
     --x-tweetdetail "$B/body-2565534621.json" --x-extra "$B/body-3161589895.json")
S=scripts/sanitize_records_fixtures.py
T=internal/records/testdata
python3 -I $S "${RAW[@]}" --out $T              # write, cross-check, verify
python3 -I $S --verify "${RAW[@]}" --out $T     # privacy check only
python3 -I $S --selftest "${RAW[@]}" --out $T   # prove the check fires
python3 -I $S --facts-only --out $T             # after editing a synthetic file
```

The array form works in bash and zsh.

Generation is deterministic: the same raw input gives byte-identical output.
After writing, the script runs the facts scan on the raw input as well, and
fails if any preserved count differs (comments, parents, depth histogram,
bodies, loaders, hints, anchors, expected counts, X entries, modules,
promoted, note tweets, cursors, terminate direction). All output is counts.

## Sanitization rules

The rules below apply to both Reddit documents, which share one identity
map, so a comment present in both sorts has the same synthetic id. 56 ids
are shared and 144 distinct.

Reddit, kept:

- the doctype, `<html lang>`, `<head><title>` (text replaced by filler) and
  the element chain down to every kept node;
- `<shreddit-post>` as a start and end tag only, with every attribute;
- the comment tree in its real place, inside a `<template for>` after
  `</html>`: `shreddit-comment-tree-stats`, `shreddit-comment-tree`, the
  `section` and the nested `template#deferred-comments`;
- every `<shreddit-comment>` with all of its attributes;
- each `<div slot="comment">` body with its full markup;
- each more-comments `<faceplate-partial>` with its hidden cursor input,
  buttons, `faceplate-tracker` and `faceplate-number`;
- the `more-comments-permalink` anchors;
- the `challenge-prompt`, `human-verification-dialog` and
  `banned-user-banner` partial tags, which appear on every page, together
  with the `data-human-verification-contribution-dialog-root` span and
  `reputation-recaptcha`. A gate or challenge classifier must ignore all
  of these.

Reddit, dropped: scripts, styles, svg, images, the header, user drawer, chat,
inbox and every other partial, the composer, action rows (they carry the
viewer's user id), ads, hover cards and the comment meta line. Wrapper
elements keep only structural attributes (`id`, `slot`, `open`, `role`,
`for`, `target`, `template`, `lang`, `bundleName`, `data-more-replies-link`,
`data-comment-section-anchor`; `routeName`, `pageType` and `user-logged-in`
on `shreddit-app`; `aria-label` on `section`). Nonces are removed.

Reddit, remapped consistently in every attribute (thingId, parentId, postId,
slot, permalinks, reload-url, loader src, anchor ids and hrefs):

- comment ids become `t1_c000001`…;
- the post id becomes `t3_p000001` and the subreddit id `t5_s000001`;
- user ids become `t2_u000001`…, and the viewer's becomes `t2_v000001`;
- authors become `u1`…`uN` (in `author`, `ariaLabel` and `/u/` paths);
- cursors become `cursor_1`…`cursor_29`, in document order (the SSR
  fixture uses 1–25, the sort=new fixture 26–29);
- the permalink slug, `post-title` and all text become same-length filler;
- partial build hashes become `hash01`…;
- chrome partial query values (`params`, `query`, `sig`) become
  `synthetic`;
- avatars and icons become a default avatar URL;
- `content-href` becomes `https://example.com/article` and `domain`
  becomes `example.com`;
- body links become `https://example.com/link/N`, `/u/uN/` or `/r/<sub>/`;
- timestamps are shifted back 728 days (104 weeks, so weekdays still
  match). The relative ages inside `ariaLabel` ("4 days ago") are not
  shifted.

`seeker-session` is the boolean `true` on the wire, not a token, so it is
kept. Numeric attributes (score, depth, comment-position, award and share
counts, upvote ratio, faceplate-number, comment-count, totalComments) are
kept.

Filler replaces every run of letters or digits with filler of the same
length. Case and the letter-or-digit class of each character are preserved.
Whitespace, punctuation, emoji and character references (`&#39;`, `&amp;`,
`&quot;`) are kept, so the decoded length, the non-space character count and
the paragraph structure match the original. The filler depends only on
position and character class, never on the original letters.

X, kept (keys stay in the export's order, which is alphabetical, so a
decoder must not depend on key order):

- instructions and their `type`, `direction`, `entries`, `entryId`,
  `sortIndex`, `entryType`, `__typename`, `displayType`, module `items` and
  `metadata.conversationMetadata`;
- `itemType`, `tweetDisplayType`, `cursorType`, `displayTreatment.actionText`
  and `promotedMetadata` (advertiser, synthetic `impressionId` and
  `impressionString`);
- per tweet: `rest_id`, `core.user_results.result` (`__typename`, `id`,
  `rest_id`, `core.screen_name`, `core.name`), `views.count`/`state`,
  `note_tweet` (`is_expandable`, result `id` and `text`) and
  `quoted_status_result`;
- legacy fields: `full_text`, `display_text_range`, `created_at`, `id_str`,
  `user_id_str`, `conversation_id_str`, the `in_reply_to_*` fields, `lang`,
  `is_quote_status`, `quoted_status_*`, and the reply, retweet, favorite,
  quote and bookmark counts;
- `metadata.scribeConfig.page`.

X, dropped: `clientEventInfo` (controller data), entities and media, cards,
user profiles (bio, location, avatar, banner, counts, verification),
`relationship_perspectives` and the viewer's `favorited`, `retweeted` and
`bookmarked` flags, edit control, AI-annotation fields and ad tracking.

X, remapped:

- tweet ids become 19-digit `19000000000000000NN`, ranked by the original
  numeric order, so newer still sorts higher;
- user ids become `29000000000000000NN` in document order, and user `id`
  is re-encoded as base64 `User:<id>`;
- note ids are re-encoded as `NoteTweet:<id>`;
- `sortIndex` keeps its gaps under a synthetic base, and `cursor-bottom-N`
  matches its `sortIndex`;
- the promoted entryId suffix becomes `feedface0000000N`;
- screen names become `u1`…, with the focal author `u1`, and names
  become `Name uN`;
- `@mentions` in text become the mapped `@uN`, and `display_text_range`
  is recomputed to match;
- `t.co` codes are replaced at the same length;
- cursor values become `x_cursor_<type>_<n>`;
- `created_at` is shifted by the same 728 days.

## Privacy check

`--verify` extracts these values from the raw files (including
`body-3161589895.json`):

- names: author names, hover-card and `/u/` names, screen names, mentions
  and display names;
- ids: thing ids with and without prefix, rest_ids, user ids, id_str and
  sortIndex values;
- cursors, nonces, csrf, session, signature and impression values, and URLs
  with a path;
- every 20+ character opaque token (letters and digits);
- every 30-character window of raw text.

It fails if any of them occurs in any file in this directory or in the
script itself. Raw text means text nodes, user-content attributes and X
string values; aria labels are excluded because they are UI templates
around the author name, and the name check covers that name.

`--selftest` plants one raw value per category into in-memory copies of the
fixtures and requires every category to report it. Nothing is written to
disk.

## Facts

`facts.json` is computed from these files themselves with a regex tag scan
(HTML) or a JSON walk (X). It does not use the sanitizer's tree code.

Reddit keys:

- `comments`, `with_parentId`, `top_level`, `wire_depth_histogram`,
  `derived_depth_mismatches`, `comments_with_body` and
  `text_nonspace_chars_total` (non-space characters of all body text after
  entity decoding); the fragment also lists its `records` (id, parent, wire
  depth, author, non-space characters);
- `expected`: `comment-count`, `totalComments` and `total-comments`;
- `loaders`: totals, hints, param sets, slots, tracker nouns and the decoded
  `paths`; `loaders.items` has one row per loader, in the order given by
  `loaders.item_columns` (cursor, kind, parent, parent wire depth, hint,
  `startingDepth`, entity-decoded query). The match key is path plus cursor;
- `permalink_anchors`: `loader_fallback` when the anchor's comment owns a
  branch loader, otherwise `continue_thread`, plus the
  `faceplate-tracker noun` counts and the parent ids of each kind;
- `partials`, `markers` and the scan's `page_class`.

X keys: `instructions`, entry and module counts, `promoted`,
`note_tweet_keys`, `cursor_types`, `terminate_directions`, `focal`
(id, author, `reply_count`), `direct_replies_to_focal`, unknown typename
counts and ratio, and the ids of promoted entries, tombstones,
visibility-wrapped, quoted and added-to-module tweets.

Synthetic fixtures carry an `expect` block with the class each one is built
to exercise. The fragment also carries `answers_loader`, the merged
`derived_depth` (absolute), `wire_offset` (equal to `startingDepth`) and
`hint_conservation` against the design rule `hint - max(2, ceil(10% of
hint))`.

## Structure notes for the decoder

Reddit:

- The comment tree is not inside `<body>`. It streams after `</html>` in a
  top-level `<template for="…">`, which `<suspense-replace>` moves into
  place. The first top-level comment sits in the `section`; the other 21
  and the lazy loader sit in a nested `<template id="deferred-comments">`.
- `thingId`, `parentId` and `postId` are camelCase on the wire; there are
  0 lowercase `thingid` attributes. Top-level comments have no `parentId`.
  `depth` equals the depth derived from the parent chain, and the element
  nesting (`div#comment-children`) agrees with `parentId` for all 100.
- A body is `<div id="t1_…-comment-rtjson-content" slot="comment">` wrapping
  `<div id="t1_…-post-rtjson-content">` with `p`, `em`, `blockquote`,
  `ul`/`li`, `a`, `code`, `strong`, `sup` and `br`.
- Every loader src path is `/svc/shreddit/more-comments/r/<sub>/t3_<post>`,
  so the parent is the enclosing comment, not the path. Branch loaders
  (`loading="action"`, `slot="children"`) carry `sort`, `startingDepth`
  (the parent's depth plus 1), `seeker-session=true`,
  `commentParentPositions` and sometimes `commentPositionOffset`.
- The lazy `top-level-more-comments-partial` carries only `top-level`,
  `comments-remaining` and `commentPositionOffset` (equal to the number of
  top-level comments). Under `sort=new` it also carries a lowercase
  `sort=new`, while the branch loaders say `NEW`.
- Each branch loader holds 2 `faceplate-number` elements with the same
  value, one visible and one in its `slot="loading"` copy, and 3 buttons.
  Count one hint per loader: summing every number gives 178, not 89.
- Each branch loader has a fallback anchor on the same comment: the same
  count, `noun="more_replies"`, label "N more replies" (or "1 more reply"). The other anchors
  (`noun="continue_thread"`, label "More replies", no number) sit on
  comments whose replies are rendered. Every anchor href is the owning
  comment's own permalink with `?force-legacy-sct=1`.

X:

- Page 1 has these instructions: `TimelineClearCache`,
  `TimelineAddEntries` and `TimelineTerminateTimeline` (`Top`, which is not
  exhaustion).
- Its 40 entries are 1 focal `tweet-<id>` item, 38
  `conversationthread-<id>` modules with one item each (36 replies and 2
  `…-promoted-tweet-…` items with `promotedMetadata`), and 1
  `cursor-bottom-<sortIndex>`.
- One module's `allTweetIds` lists a second id that is not on the page.
- `screen_name` appears only under `core.user_results.result.core`, plus
  `promotedMetadata.advertiser_results`.

## Assumptions in the synthetic fixtures

These are not observed. Replace them with sanitized live captures when
those exist.

- The more-comments fragment is a bare sequence of `<shreddit-comment>`
  elements with no document wrapper. Its `depth` is relative to the
  request's `startingDepth`; a nested loader's `startingDepth` is absolute.
  A deleted comment has `author="[deleted]"` and the body "Comment deleted
  by user". The repeated `thingId` is a byte-identical duplicate element.
- The over-18 page is assumed to keep the shreddit app shell and a
  `shreddit-post`, and to add an interstitial with an `/over18?dest=` form
  and `name="over18"`. The element ids and wording are invented.
- The challenge page is a minimal standalone document whose title and
  heading say "Prove your humanity".
- In the X ShowMore file, one page combines instructions that X spreads
  over separate responses. The inner tweet of `TweetWithVisibilityResults`
  has no `__typename`. The ShowMore `actionText` is "Show replies". The
  unknown typenames (`TimelineSyntheticUnknownA` and `…B`) are invented.
