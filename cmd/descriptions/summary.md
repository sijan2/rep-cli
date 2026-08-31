# rep summary

First-pass overview of captured traffic. Start here.

## What it returns

- Total requests, unique domains, ignored count.
- Method breakdown (GET / POST / …).
- Status breakdown (2xx / 3xx / 4xx / 5xx / 0xx).
- Pages: which URLs the traffic originated from.
- Primary domains (configured in store.json).
- Domain breakdown sorted by request count.

## Agent routing

Use this once per session to:
1. Confirm `live.json` is populated (non-zero totals).
2. Pick candidate primary domains from the breakdown.
3. See which primaries are stale (configured but not in current session).

If primaries don't match current traffic, use
`rep primary --clear && rep primary <domain>` or pass `--primary=false`
to `rep list`.
