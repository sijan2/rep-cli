# rep curl

Generate a curl command that replays a captured request.

## Input

`<request-id>`: accepts any ≥4-character prefix of the canonical
`h_xxx...` ID, the semantic `xxxx_METHOD_STATUS` label, or the short hash.

## Token-saving workflow

Default output inlines every header value, including secrets. For agent
workflows prefer:

```
rep auth --save -d <domain>                   # writes ~/.rep/auth-<domain>.env (0600)
eval "$(rep auth --vars -d <domain>)"
rep curl <id> --use-vars                       # curl with $BEARER_TOKEN, $SESSION_COOKIE, ...
```

With `--use-vars`, the emitted curl references shell variables instead
of raw secrets, so the agent's context never sees the values.

HTTP/2 pseudoheaders are always omitted. A signed query is still part of the
URL and therefore remains visible in generated output. For downloads, prefer
`rep download <request-id> <path>` so the URL, cookies, and body never enter the
agent transcript.
