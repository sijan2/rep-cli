# `rep arc` contract

## Purpose

Control ArcCore's browser-process DevTools endpoint for `Browser.*` and
`Target.*` commands and UI-free extension reloads.

## Start

```text
rep arc launch --port 9222 --restart -j
rep arc status --port 9222 -j
```

Arc is launched with a fixed nonzero loopback port and exact allowed origin.
The command never adds `--headless` or `--enable-automation`.

## Commands

```text
rep arc cdp Browser.getVersion -j
rep arc cdp Target.getTargets -j
rep arc cdp Target.createTarget --params '{"url":"https://example.com","background":true}' -j
rep arc cdp Runtime.evaluate --target <target-id> --params '{"expression":"document.title"}' -j
rep arc reload-extension -j
```

The direct CDP client refuses non-loopback WebSocket endpoints. `--params`
accepts a JSON object or `@path`. Omit `--target` for browser-process commands;
provide it for direct page or worker commands.
