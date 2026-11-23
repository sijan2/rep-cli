# Jev

Jev configuration, status, doctor and semantic selection live under `rep lab jev`. Read `rep lab --help` for accepted flags and platform prerequisites. A `CGO_ENABLED=0` build still compiles; live packet capture then reports that it is unsupported.

For ordinary browser work use `rep page URL`, `rep do REF VALUE` and `rep eval @script.js t3`. For captured HTTP evidence use `rep page URL --capture`, `rep net` and `rep body h_ID`. For liveness use `rep tabs`; repair uses `rep doctor`. Use [the migration map](migration.md) to find the current intent.

Lab privileges and model configuration are explicit; an absent capability is reported rather than inferred as a successful observation. Use synthetic fixtures and a private browser/profile for development. Use explicit cookie/request exports or eval/CDP --out for original credential values when needed; see [authentication](authentication.md) and [request exports](request-export.md). Development checks use synthetic credentials and preserve installed browser configuration and other tasks' tabs. Historical files under validation/ are dated pre-migration evidence, not current acceptance claims.
