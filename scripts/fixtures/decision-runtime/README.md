These loopback pages are controlled test applications for
`scripts/verify_decision_runtime.py`. They contain no real accounts or user data.

The runner creates a unique workspace/task, temporary data directory, private
Chromium profile, and frozen extension copy. It verifies the owning Chromium PID
before restarting its native host, and stops the owned browser in `finally`.
`--output` is required and an existing report is never overwritten.

Run the local browser tests:

```sh
python3 scripts/verify_decision_runtime.py --build --output /tmp/runtime-local.json
```

Add labeled calls to the configured real provider and preserved baseline:

```sh
JEV_MODEL=jev-1.13.0 python3 scripts/verify_decision_runtime.py \
  --build --with-jev --baseline --output /tmp/runtime-comparison.json
```

The default baseline paths are `/tmp/rep-runtime-baseline-rep`,
`/tmp/rep-runtime-baseline-host`, and `/tmp/rep-runtime-baseline-extension`;
flags can replace them. Without `--build`, `REP_BINARY`, `REP_HOST_BINARY`, and
optionally `REP_EXTENSION_PATH` select the current arm.
`--with-jev --baseline-only` runs just the preserved baseline.
`--with-jev --public-github` selects a separate read-only public-page suite:
three fixed link goals on `https://github.com/browser-use/jev-ultrafast`, checked
against live DOM hrefs without clicking. Add `--baseline` for the old binaries.
An unavailable, changed, or challenged page stops that optional arm.
`--public-diagnose` reads eight compact snapshots, 250 ms apart, with no provider
calls and records candidate and binding changes. `--with-jev --public-settled`
runs the three public goals in auto mode only after the last three of eight
full semantic fingerprints match. This preparation is reported separately and
excluded from the per-choice timer; freshness checks still run after inference.
`--with-jev --public-scoped` declares the Repository navigation container for
Issues and the Folders and files table for README/docs. It settles the page
before resolving those named AX containers, then checks scoped fingerprints.
The report records those caller restrictions and preparation time explicitly;
the suite does not infer a scope silently or relax truncation checks.

Checks include row-price invalidation, declared subtree repair and deltas,
same-process and nested cross-origin frame actions, ancestor-frame occlusion,
already-satisfied cross-frame postcondition refusal, open and closed shadow
controls, replaced form controls, released leases,
native-host reconnect, and document generations. Provider runs add paired
flat/grouped choices, both orientations of duplicate Buy rows, no-match,
independent three-field completion, dependent country/region reselection, and
verified inference-free reuse. Independent renderer reads check final effects.

Reports include source/binary digests, pinned returned model identifiers,
request counts, transport timing, acceptance and label correctness separately.
Provider failures retain completed trials, fixed diagnostic classifications,
and a public fixture snapshot for packing diagnosis; arbitrary stderr is never
written to the report. Baseline abstentions remain measured outcomes.
`seconds` is CLI time including executor checks; `verified_seconds` additionally
includes independent readback where recorded. The small nearest-rank p95 is
descriptive, not an established tail-latency bound. The fixtures establish
these specific behaviors; they do not measure broad real-site accuracy.
