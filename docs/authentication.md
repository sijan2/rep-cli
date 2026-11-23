# Authentication and practical API work

Rep uses the selected browser profile's login state. Tasks isolate tab ownership
and captures; profiles isolate accounts. Use returned handles in these examples
in place of `t3`, `t3.e12` and `h_ID`.

## Sign in and reuse a profile

```sh
export REP_BROWSER=headless
export REP_PROFILE=research-login
rep browser start --headed
rep page https://example.com/login
```

Sign in through the visible browser or use observed form controls with `rep do`.
Read the page again to verify the signed-in account. A private profile retains
cookies and site storage across stops. After closing owned tabs and releasing
holders, `rep browser stop` followed by `rep browser start` uses the same profile
headlessly. `rep scope` shows the selected profile; `rep tabs` shows live tabs.
To use an already running extension-backed browser, select it explicitly with
`rep browser start chrome`, `rep browser start arc` or `rep browser start dia`.

## Inspect and transfer cookies

```sh
rep cookies list t3
rep cookies list t3 --all-paths
rep cookies export t3 cookies.json --all-paths
rep cookies export t3 cookies.txt --all-paths --format netscape
rep cookies import t3 @cookies.json
rep cookies import t3 @cookies.txt
rep cookies import t3 @browser-cookies.txt --all-domains
rep cookies delete t3 session --path /
rep page t3 --reload
```

By default, list/export includes HttpOnly cookies applicable to the tab's URL.
For a full host login, `--all-paths` includes every path and scheme for that host,
plus applicable parent-domain cookies. This captures an `/api` cookie while the
tab is on `/`, without including unrelated hosts or sibling host-only cookies.
List fingerprints values; export writes raw JSON or Netscape rows to a new file.
List metadata is in `untrusted.items`; `sessionOnly` identifies a
session cookie. Use the returned domain/path to select a cookie precisely for
deletion. For partitioned cookies, also pass the returned partition object as
`--partition-key '{"topLevelSite":"https://example.com","hasCrossSiteAncestor":false}'`.
Import auto-detects the exported `{"cookies":[...]}` object, a JSON array, or a
Netscape cookie jar (including `#HttpOnly_` records); `@-` reads stdin. A minimal
JSON input looks like:

```json
[{"name":"session","value":"your-session-value","path":"/","httpOnly":true,"secure":true,"sameSite":"Lax"}]
```

Omitted URL/domain uses the addressed tab. By default domains must match its
host. Use `--all-domains` for a full-browser jar: every cookie is restored to its
declared host/domain in the selected profile, whose tabs share those logins.
Rows are never silently filtered or retagged to the current tab. HttpOnly, Secure, SameSite,
expiry, host-only/domain scope and supported partition keys are retained.
Existing cookies with the same identity are replaced; unrelated cookies remain.
The browser's acknowledgement means import finished, not that the server accepts
the session: verify with a page reload or authenticated fetch. A session may also
depend on site storage, expiry, or other server-side state.

Netscape files work with cookie-jar consumers such as curl and yt-dlp. The format
itself has no SameSite, partition-key or browser priority fields. Rep stores
these in comments bound to the following cookie's identity, and restores them
when importing its own jars; regular consumers ignore the comments and do not
enforce partitioning. Export metadata lists the attributes those consumers
ignore. Editing a row's value or expiry takes precedence over metadata. If a
cookie is renamed or moved to another domain/path, its old metadata is ignored
and the import reports that count. JSON is the direct native-field format.

## Site storage and raw results

LocalStorage belongs to the page's origin. Export it with:

```sh
rep eval 'Object.fromEntries(Object.entries(localStorage))' t3 --out storage.json
```

To restore it, generate a script from that JSON and run it on the same origin:

```sh
python3 - <<'PY'
import json
from pathlib import Path
values = json.loads(Path('storage.json').read_text())
Path('restore-storage.js').write_text(
    'for (const [key, value] of Object.entries(' + json.dumps(values) +
    ')) localStorage.setItem(key, value); true')
PY
rep eval @restore-storage.js t3
```

SessionStorage can be handled the same way but
is tab-specific. Page scripts cannot read HttpOnly cookies; use `rep cookies`.

`--out` on eval/cdp saves the raw result as JSON. `rep body h_ID --out body.bin`
exports captured bytes without silently refetching. Explicit export files are
mode 0600, include a digest in stdout, and refuse to overwrite an existing path.
Normal output and automatic artifacts fingerprint credential fields.

## Authenticated requests, replay and downloads

```sh
rep fetch https://example.com/api/me
rep fetch https://example.com/api/search -H 'Content-Type: application/json' -d @query.json
rep fetch https://example.com/api/me -H 'Authorization: Bearer TOKEN'
rep fetch https://example.com/api/update -H 'X-CSRF-Token: TOKEN' -d @update.json
rep page t3 --capture
rep net type:api
rep body h_ID --request
rep replay h_ID --set query.page=2
rep replay h_ID --matrix 'query.page=[1,2,3]'
rep replay h_ID --set body.enabled=true --write
rep download https://example.com/report.pdf report.pdf
```

Fetch/replay includes browser-managed cookies automatically. Explicit Cookie,
Origin, Referer and other browser-controlled headers are rejected with a useful
next step instead of silently ignored. Use cookie import for Cookie and normal
`-H`/`--set header.NAME=VALUE` for Authorization or application CSRF headers.
Request data defaults fetch to POST; `-X` selects another method.

Explicit API work is not refused solely by site name. Server errors and cooldowns
still matter; inspect each result's completeness and status. Signed request
headers may expire or cease to match a mutated body. In that case, capture the
page's current request or read its loaded responses with `rep records`.

## Other tools

`rep lab --help` lists packet/native capture, Android/IG, media, research and
vision tools. Every accepted option is visible in its command help. `rep cdp
--help` describes the page protocol surface; use its specialized alternatives
for browser-session cookies and captured bodies. Help includes usable examples
and defaults rather than imposing a byte quota.
