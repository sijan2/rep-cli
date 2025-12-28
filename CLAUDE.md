# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Install

```bash
# Build and install rep CLI
scripts/build_install.sh --install-dir ~/.local/bin

# Build with native messaging host (for Chrome/Arc extension)
scripts/build_install.sh --host --install-dir ~/.local/bin
```

The build script injects version, commit, and build date via ldflags.

## Architecture

rep-cli is an AI-agent optimized HTTP traffic analyzer for bug bounty hunting. It works with the rep+ Chrome/Arc extension to capture and analyze HTTP traffic.

### Data Flow
1. **rep+ extension** captures HTTP traffic in browser
2. **rep-host** (native messaging binary) receives traffic via Chrome Native Messaging protocol and writes to `live.json`
3. **rep CLI** reads from store (`store.json`) and imports from `live.json` via sync command

### Key Paths
- Store: `~/.local/share/rep-cli/store.json` (or `$XDG_DATA_HOME/rep-cli/store.json`)
- Live export: `~/.local/share/rep-cli/live.json` (override with `$REPLIVE_PATH`)

### Package Structure
- `cmd/` - Cobra CLI commands (sync, list, body, summary, domains, ignore, primary, clear, etc.)
- `cmd/host/` - Native messaging host binary for extension communication
- `internal/store/` - Data store, types, filtering, and persistence
- `internal/output/` - Output formatting, body truncation, JSON serialization

### Core Types (internal/store/types.go)
- `Store` - Singleton holding requests, ignored/primary domains, checkpoints
- `Request` - HTTP request with headers, body, response, computed Domain/Path
- `FilterOptions` - Domain, method, status, pattern (regex), pagination
- `OutputMode` - compact (default), meta (headers only), full, json

### Output Modes
All listing commands support `--output` flag:
- `compact` - Truncated bodies (500 chars), good for scanning
- `meta` - Headers only, no bodies
- `full` - Complete response bodies
- `json` - Raw JSON for piping

## CLI Commands Reference

```bash
rep sync                    # Import from live.json
rep sync --watch            # Watch and auto-import on changes
rep sync --since <RFC3339>  # Import only after timestamp
rep summary                 # Traffic overview (domains, methods, suggestions)
rep domains                 # List domains with request counts
rep list                    # List requests (one-line format with IDs)
rep list --detail           # Multi-line request output
rep list -d <domain>        # Filter by domain
rep list -p "pattern"       # Filter by URL regex
rep list --status-range 4xx # Filter by status range
rep body <id>               # Get full response body
rep body <id> --request     # Get request body
rep ignore <domain>...      # Add domains to ignore list
rep primary <domain>...     # Mark domains as primary targets
rep clear                   # Clear store (keeps ignore list)
rep clear --live            # Also clear live.json
```

## Dependencies

- `github.com/spf13/cobra` - CLI framework
- `github.com/bytedance/sonic` - Fast JSON serialization
- `github.com/fsnotify/fsnotify` - File watching for `--watch` mode
- `github.com/pterm/pterm` - Terminal output formatting
