# Export a captured request for other tools

`rep export` preserves the original captured URL, authentication headers, request
body and response. It reads the retained archive; it does not send a request or
retrieve cookies again.

```sh
rep page https://example.com/ --capture
rep net type:api
rep export h_ID request.json
rep export h_ID request.sh --format curl
```

Use the returned request handle in place of `h_ID`. Destinations must be new
files. Exported files are mode 0600; stdout reports their path, byte count,
digest, source archive, request ID and completeness.

The JSON export contains the native `record`, including original duplicate
headers and stream data when applicable. `request_body` and `response_body`
contain decoded bytes encoded as base64, their content types and capture
metadata. Missing, partial or invalid evidence is explicitly identified.
Invalid retained bytes remain in the native record; they are never silently
repaired or refetched. This makes `Cookie`, `Authorization`, CSRF headers and
response `Set-Cookie` values available for authorized local analysis and tooling.

The curl format writes a shell script. Run it when you intend to reproduce the
request:

```sh
sh request.sh
```

The script preserves URL/method and application/authentication headers, supports
duplicate headers and binary bodies, handles HTTP/2 pseudoheaders and HEAD,
and lets curl recompute body framing. It disables automatic `.curlrc` loading,
does not add retries or redirects, and stages request bytes before contacting
the server. Body staging requires Python 3 and `mktemp`; temporary bytes are
removed after the script finishes. A failed decode prevents the request.

Curl export needs a complete HTTP request body. JSON can export partial or
stream evidence. Exporting a command does not prove that a server will still
accept its session, signature or CSRF token. The script uses the captured values;
`rep replay` uses the current browser session instead. Browser connection
fingerprints, TLS and browser-generated headers may differ when using curl.
