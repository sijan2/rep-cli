---
name: rep-cli
description: Workflows for rep-cli and the rep+ Chrome/Arc extension integration: build/install rep and rep-host, configure native messaging, manage live export (live.json, REPLIVE_PATH), run rep sync/clear/list/summary/body, and troubleshoot empty live files or checkpoint issues. Use when modifying or operating the rep-cli Go codebase or rep extension JS code.
---

# rep-cli

## Overview
Use this skill to operate, build, and troubleshoot the rep-cli pipeline and its rep+ extension integration on macOS (Chrome/Arc). Favor concise fixes, reproducible steps, and minimal changes.

## Quick Start
- Locate repos: `/Users/sijan/bug/rep-cli` (CLI) and `/Users/sijan/bug/rep` (extension). If missing, search by `go.mod` or `manifest.json`.
- Build/install: `cd /Users/sijan/bug/rep-cli && scripts/build_install.sh --host --install-dir ~/.local/bin`.
- Ensure native host manifest exists for Chrome/Arc and points to `rep-host` (see `references/paths-and-manifests.md`).
- In the extension DevTools panel, toggle Live Export and verify `live.json` updates.
- Import traffic: `rep sync` (one-shot) or `rep sync --watch` (tail).

## Core Workflow
1. Confirm live export path
   - `rep-host` writes to `store.GetLiveFilePath()` (default `~/.local/share/rep-cli/live.json`).
   - `REPLIVE_PATH` overrides; `~` is expanded.
2. Use CLI for analysis
   - `rep summary`, `rep domains`, `rep list`, `rep body <id>`.
   - Filters: `--domain`, `--status-range`, `--method`, `--pattern` (regex).
3. Reset data safely
   - `rep clear` clears store and sets a checkpoint.
   - `rep clear --live` also clears the live export file.
   - `rep sync --since 0` re-imports from the live export.

## Troubleshooting
- "No requests in live file": verify `rep-host` is on PATH, manifest path is correct, and the Live Export toggle shows connected; then check `live.json` path and permissions.
- "No new requests since checkpoint": use `rep sync --since 0` or `rep clear --no-checkpoint`.
- "Extension context invalidated": reload the extension and re-open the DevTools rep+ panel.

## When Changing Code
- CLI changes: update Go in `cmd/` or `internal/`, rebuild with `scripts/build_install.sh`.
- Extension changes: update JS in `/Users/sijan/bug/rep/js/`, reload in `arc://extensions` or `chrome://extensions`.

## References
- Paths and native host manifest: `references/paths-and-manifests.md`
