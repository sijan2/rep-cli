# rep identity — principals for differential testing

An authorization finding needs a named **subject**, a named **resource**, and an
observed **denial**. A status-code delta with no identity attached is not
evidence of anything.

`rep identity` binds the credential headers of a captured request to a
principal name so `rep replay --as` can re-execute a request as somebody else.

## Why this exists

Without it, testing as a second principal means pasting a multi-kilobyte
`Cookie` or JWT into `--set 'header.Cookie=…'` on every probe. That floods agent
context with secret material, truncates unpredictably, and is not repeatable.

Credential bytes are written to the task's `identities.json` (mode 0600) and are
**never printed**. Commands report a 12-character fingerprint instead, which is
enough to tell two principals apart and to notice a rotated credential.

## Commands

```sh
rep identity capture <request-id> --as alice   # bind from a captured request
rep identity list                              # names + fingerprints only
rep identity forget alice
```

`capture` lifts `Cookie`, `Authorization`, `X-CSRF-Token`, `X-API-Key`,
`X-Auth-Token` and vendor header names containing `api-key`, `csrf-token`,
`auth-token`, `session-token` or `access-token`. If the request carries none of
these it fails loudly rather than binding an empty principal — capture a request
that was actually made while authenticated as that principal.

## The reserved principal `anon`

`anon` always exists, is never stored, and cannot be bound. Replaying as `anon`
strips every credential header rather than substituting one.

## Credential hygiene during replay

Before applying a principal, `rep replay --as` removes whatever credentials the
captured request already carried. A partial overwrite can never leave two
principals' credentials on the same request — which would make the result
uninterpretable.

## Scope

Identities are task-local, like captured data. They are not shared across tasks
and are not exported by `rep evidence`. Binding a principal does not isolate
browser cookies or login state: scope isolates captured data, not the browser
profile. Use separate browser profiles when account isolation must be real.
