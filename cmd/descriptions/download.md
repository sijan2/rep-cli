# rep download

Replay one captured final GET directly to a file without exposing request
secrets or response bytes in the agent transcript.

```text
rep download <request-id> <output-path> -j
```

The request ID accepts the same full, semantic, and >=4-character prefix forms
as `rep body` and `rep curl`. The command:

- passes the signed URL and captured headers to curl through stdin;
- disables user and system curl config before loading the isolated stdin config;
- strips HTTP/2 pseudoheaders and `Range` by default;
- does not follow redirects, preventing explicit cookies from crossing hosts;
- retries bounded transient GET failures;
- writes a mode-0600 part file and atomically publishes without overwrite;
- validates the completed part file before publishing;
- emits only request ID, output path, status, byte count, declared and detected
  content types, and SHA-256.

An HTML response is refused automatically when the output has a known binary
extension such as `.ipa`, `.zip`, `.dmg`, or `.pdf`. This catches HTTP 200
login, quota, and challenge pages without displaying their bodies. Add explicit
artifact invariants whenever they are known:

```text
rep download <request-id> app.ipa \
  --min-bytes 1000000 \
  --expect-content-type application/zip \
  --expect-content-type application/octet-stream \
  --expect-magic zip -j
```

`--expect-content-type` is repeatable, ignores media-type parameters, and
accepts `type/*`. `--expect-magic` accepts common names (`zip`, `ipa`, `apk`,
`gzip`, `pdf`, `elf`, `macho`, `png`, `jpeg`, `xz`, `bzip2`, `zstd`, `7z`,
`rar`, `xar`, `wasm`, and `sqlite`) or an exact prefix such as
`hex:504b0304`. A validation failure removes the part file and leaves any
existing destination untouched.

Use `--keep-range` only when a partial download is intentional. Use
`--overwrite` only when replacement was explicitly requested. Use
`--allow-html` only when HTML under a binary-looking output name is intentional.
If curl is rejected by browser-bound controls, use the real-profile Arc
streaming route with the same artifact invariants:

```text
rep browser download <request-id> app.ipa \
  --min-bytes 1000000 --expect-magic ipa -j
```

This bridge-native path does not require Arc's browser-process debugging port.
