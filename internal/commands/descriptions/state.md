# `rep browser state` contract

`rep browser state --tab ID` answers the one question every site shares before
any read or action is trusted: what is this tab showing right now? The answer is
one of a closed set, each with a next step, so an agent never concludes "empty"
or "done" from a page that was still loading, walled, or blocked.

| Kind | Meaning | Next |
|---|---|---|
| `content` | The real content is visible | read with `browser page` or act |
| `loading` | Shell, skeletons, spinners, hydration in progress | `browser page --tab ID --timeout 10s` |
| `login_wall` | Sign-in blocks the content | a person signs in manually in the profile; never enter credentials through Rep |
| `consent_dialog` | Cookie or privacy dialog covers the page | `browser locate` the accept or close control, then read again |
| `challenge` | Captcha, Cloudflare check, access denied, rate limit | stop automated access to this origin; a person clears it |
| `error` | Not found, server error, something went wrong | reopen once, then treat as unavailable |
| `empty` | Loaded with zero results or an empty state | zero results is the answer |

## How it decides

1. **In-page signals, no model.** Over a 300 ms window: new fetch/XHR resource
   entries, DOM mutations, main-landmark text length, visible controls, open
   dialogs, password fields, and text or frame hints for login, consent,
   challenge, rate limit and error pages. These are the same signals every stack
   produces, whatever framework rendered the page.
2. **One Jev choice about the pixels when the signals are ambiguous.** Clear
   content (200+ characters, no hints) and a visible password field need no
   model. Otherwise one screenshot and the DOM signals go to Jev as a single
   `choice` question over the seven kinds. `basis` records which path decided;
   a confidence below 0.8 is marked `jev_screenshot_choice_low_confidence`.
   Without a configured Jev key the heuristic classification is returned with
   `basis: in_page_signals_only`.

The output also carries the page's `stack` fingerprint (framework, data layer,
embedded state sizes, custom elements) and `data_requests`: every fetch/XHR
endpoint the page issued since navigation, from Resource Timing, grouped by
origin and path with ids collapsed, with byte totals and whether they happened
after `DOMContentLoaded`. That list is where the page's data actually comes
from; capture one of those requests and replay it with `browser capture fanout`
instead of scraping the rendering.

`browser page` includes the same `stack`, `data_requests` and `page_state`
fields on live reads (not on cursor continuations), measured with a 150 ms
window only when the page looks unsettled.

## Limits

Signals are heuristics tuned on common markup; the model judgment is a
calibrated probability, not ground truth. The screenshot is the viewport only.
A `challenge` verdict means stop: repeated automated attempts against bot
checks make the block worse for the signed-in person.
