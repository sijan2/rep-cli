# Runtime architecture

The Go CLI resolves identity and handles, calls the native bridge, bounds/redacts results, and writes private artifacts. The MV3 extension observes and acts in browser tabs. The browser starts rep-host through native messaging; rep-host advertises its profile identity, browser epoch and build digest through a private bridge registry.

`internal/commands` holds Cobra metadata and the generated command help, examples and defaults. `internal/scope` derives identity. `internal/agentstate` owns the flocked tab/ref/cursor/capture registry and leases. `internal/browserfast` implements bounded page reads and observed-ref actions; `internal/browserflow` validates explicit plans and native key events. `internal/bridge` handles transport and pre-dispatch recovery. `internal/headless` manages stable private profiles and holder guards. `internal/output` owns result/error projection and secret fingerprinting.

A normal read is `rep page URL`. Its short tab/ref handles identify owner, profile and document generation. `rep do REF VALUE` validates the observed target, executes once and returns changed evidence. `rep eval @script.js t3` uses the same execution lease and returns untrusted values. Navigation and mutations do not share an implicit target when another worker can own it.

`internal/records` decodes thread responses into one model with provenance and cross-checked coverage; `internal/sitepolicy` holds the embedded per-site policies (body operations, private paths, pacing, stop signals); `internal/governor` keeps per-site locks and cooldowns under the global data directory; `internal/ledger` is the typed client of the extension's per-tab network ledger. The extension's `network-ledger.js` redacts credentials before anything crosses native messaging. See [records](records.md).

Network capture is explicit. The extension's capture-session queue feeds archive-backed `rep net` and `rep body h_ID`; canonical IDs prevent ambiguous archive fallback. Fetch/replay use a rep-owned background tab per task/origin. Evidence bytes and redacted projections remain distinct, and only explicit body export can write raw credential-bearing bytes.

[State and output](agent-context.md), [UI plans](interactions.md), [bodies](body-capture.md), and [profiles](headless.md) define the user contract. Tests cover parser/flags/help, archive resolution, redaction, stale refs, concurrent ownership, dispatch uncertainty and companion argv. Browser gates use synthetic local pages and the built rep binary only.

Specialized packet, mobile, Jev/vision and research constructors register under the `rep lab` group in the same binary. Historical JSON validation artifacts in docs/validation are dated observations from the pre-migration implementation, not claims about the current command grammar or current acceptance results.
