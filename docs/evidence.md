# Evidence and journals

`rep scope` returns the task's recent operation journal, owned tabs, captures and storage without a browser round-trip. State-changing calls record operation metadata and artifact paths/digests. The journal does not contain credentials or script/plan secrets.

Captured requests retain archive provenance and canonical h_ handles. Use `rep net s_CAPTURE` to inspect one capture and `rep body h_ID` for one retained request; inspect completeness before drawing conclusions. Use `rep reference URL` for design evidence and its always-written screenshot. Use `rep shot t3 image.png` for image evidence.

Retained bytes and projections are separate. Default stdout fingerprints credential-bearing values and labels source content untrusted. Explicit `rep body h_ID --out response.json` writes raw decoded bytes to a new mode-0600 file. Large automatic artifacts remain redacted. Keep artifacts private and cite their digest/source rather than printing raw exports.

An unobserved action is not verified success; a timed-out read is not an empty page. Report partial evidence and follow returned continuations. Historical docs/validation files describe the older implementation; current gates must measure the current binary.
