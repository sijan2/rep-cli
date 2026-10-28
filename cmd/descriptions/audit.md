# rep audit — attack-surface model

`rep audit` turns a capture into ranked **endpoint families**, each paired with
the experiment worth running against it.

## What changed and why

The superseded scorer (`--legacy-score`) rated each request independently by
summing five booleans. One of them fired whenever a cookie was present — which a
browser attaches to every same-origin request, including fonts. Measured on a
real 228-request capture it scored 206 requests, **all tied at the same value**,
so `--top N` degenerated into sort-by-request-id, and 15 of the top 20 rows were
woff2 fonts, script bundles, a stylesheet and a favicon. On a capture taken
without a login it scored 2 of 84 requests, both static assets.

The default path fixes the cause rather than the symptom:

1. **Static traffic is excluded before scoring.** Fonts, stylesheets, images,
   media and content-hashed bundles cannot hold the vulnerabilities this tool
   tests for. A cookie on a font proves nothing.
2. **Requests collapse into endpoint families.** Numeric, UUID, long-hex and
   opaque path segments are object identifiers, not route structure, so
   `/api/doc/1` and `/api/doc/2` become `/api/doc/{num}`. Readable slugs and
   version segments (`/v2/`) stay literal. One row per family, not per request.
3. **Scores are continuous and weighted**, so families separate instead of tying.
4. **Every candidate carries an oracle and a runnable next command.**

## The object-reference signal

A collapsed slot is only interesting once the client has been observed choosing
**more than one value** for it. One observed value yields the weaker
`object_slot` signal; two or more yields `object_reference`, because that is the
evidence the identifier is client-chosen and therefore enumerable. This is the
precondition for a cross-principal authorization test.

## Oracles

Each candidate selects exactly one:

| Oracle | Selected when |
|---|---|
| `authz_cross_principal` | enumerable object identifier on an authenticated endpoint |
| `object_reference` | enumerable identifier, no authentication observed |
| `mutation_side_effect` | client asserts a trusted field, or a cookie-authenticated mutation with no CSRF token |
| `authz_unauthenticated` | authenticated endpoint; confirm the credential is actually required |
| `information_disclosure` | large response on an unauthenticated endpoint |
| `input_reflection` | otherwise |

## Reading the output

`families_static` is the count excluded from scoring; a high number is normal and
healthy. `families_scored` is what remains.

An empty candidate list means **nothing in this capture scored** — not that the
target is secure. The usual cause is a capture taken without authenticated
traffic. Capture a logged-in session and audit again.

Scores rank candidates within one capture. They are not comparable across
captures and are not severity.

## Flags

`--top N` candidates to return. `--primary-only` restricts to primary domains;
without any primary domain set, every host is treated as in scope.
`--legacy-score` restores the superseded per-request scoring for comparison.
`--saved <id>` audits an archived session instead of the live capture.

Note that a live capture changes while lanes are still writing to it, so two
audits of `live.json` taken seconds apart can legitimately differ. Use
`--saved` for a stable corpus.
