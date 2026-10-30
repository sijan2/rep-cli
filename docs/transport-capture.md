# Transport diagnostics

Protocol-payload, media and transport forensic diagnostics ship in the specialized rep-lab build (`go build -tags replab -o bin/rep-lab ./cmd/rep`). Read that binary's help for its accepted flags and platform prerequisites. Core is buildable with CGO_ENABLED=0; platform-specific capture support may need additional libraries in a lab build.

For ordinary browser work use `rep page URL`, `rep do REF VALUE` and `rep eval @script.js t3`. For captured HTTP evidence use `rep page URL --capture`, `rep net` and `rep body h_ID`. For liveness use `rep tabs`; repair uses `rep doctor`. Use [the migration map](migration.md) to find the current intent.

Lab privileges and model configuration are explicit; an absent capability is reported rather than inferred as a successful observation. Use synthetic fixtures and a private browser/profile for development. Do not export credentials, replace installed browser configuration, or affect another task's tabs. Historical files under validation/ are dated pre-migration evidence, not current acceptance claims.
