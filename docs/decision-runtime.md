# Decision runtime

Model-assisted selection, visual grounding and decision research live under `rep lab`. Read `rep lab --help` for accepted flags and platform prerequisites. A `CGO_ENABLED=0` build still compiles; live packet capture then reports that it is unsupported.

For ordinary browser work use `rep page URL`, `rep do REF VALUE` and `rep eval @script.js t3`. For captured HTTP evidence use `rep page URL --capture`, `rep net` and `rep body h_ID`. For liveness use `rep tabs`; repair uses `rep doctor`. Use [the migration map](migration.md) to find the current intent.

Lab page tools accept the same public tab handles returned by core commands:

```sh
rep lab element observe --tab t3 --kind all
rep lab element observe --tab t3 --frame-url https://example.com/embedded-form
rep lab state --tab t3
rep lab locate 'the search button' --tab t3
rep lab step 'fill the search field' --tab t3
```

The handle supplies its task, browser and exact profile. You do not need to look
up Chromium's numeric tab ID or repeat `--browser`. Legacy numeric IDs remain
accepted when needed. Conflicting explicit task/browser choices fail before
acting. Element observation and heuristic state classification need no model;
locate/step require the configured Jev service described by their help.

Lab privileges and model configuration are explicit; an absent capability is reported rather than inferred as a successful observation. Use synthetic fixtures and a private browser/profile for development. Explicit credential/session exports use the workflows in [authentication](authentication.md). Keep other tasks' tabs and installed browser configuration intact. Historical files under validation/ are dated pre-migration evidence, not current acceptance claims.
