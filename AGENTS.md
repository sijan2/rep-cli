# Repository Guidelines

## Project Structure & Module Organization
- `main.go` wires the CLI entrypoint.
- `cmd/` holds Cobra commands (one file per command).
- `cmd/host` contains the native messaging host for Chrome extension.
- `internal/` contains shared packages (`internal/store`, `internal/output`, `internal/noise`).
- `scripts/` has build helpers (see `scripts/build_install.sh`).
- `VERSION` defines the default CLI version.

## Available Commands (Minimal Set)
- `setup <target>` - Initialize recon, extract tokens, set primary domains
- `summary` - Traffic overview with domain breakdown
- `primary <domain>` - Manage primary target domains
- `list` - List requests for review
- `mute <path>` - Mute specific endpoints
- `js` - List all JavaScript files
- `save` - Save current session with notes

## Build, Test, and Development Commands
- `scripts/build_install.sh --host --install-dir ~/.local/bin`: build and install
- `go build ./...`: quick compile check
- `go run . --help`: run CLI directly

## Coding Style & Naming Conventions
- Go formatting is standard `gofmt`; use tabs for indentation
- New commands should live in `cmd/<name>.go` and register with Cobra in `init()`
- Keep flags consistent with existing patterns (e.g., `--output`/`-o`, `--json`)

## Testing Guidelines
- Run `go test ./...` before submitting changes

## Configuration & Native Host Tips
- Native host writes live export to `~/.local/share/rep-cli/live.json`
- Override with `REPLIVE_PATH` environment variable
- Set `REP_KEEP_ON_DISCONNECT=1` to preserve `live.json` when extension disconnects

## Multi-agent browser work

- Select a distinct workspace/task for each independent agent. Use explicit
  `--workspace/--task` flags or process environment; run `rep scope -j` first.
- Never interpret another site's shared capture as inability to access the
  requested site. An empty scoped capture is `no_capture`; observe the authorized
  site with a task-owned tab when the task requires it.
- Use `rep summary --max-bytes 4096`, then `rep context --since CURSOR`; retain the
  newest cursor and inspect omission counts. Fetch individual response bodies only
  when needed. Do not load all archives or notes into context automatically.
- `primary` filters are task-local. `--global` deliberately opens legacy shared
  data; it is not a workaround for an empty task.
- Browser cookies and tabs remain shared. Use explicit owned tab IDs. Independent
  account state requires different browser profiles.
- Full architecture and limits: `docs/agent-context.md`; command contracts:
  `rep describe scope` and `rep describe summary`.

- Diagnose large/missing bodies with `rep body ID --saved HASH --info` before
  interpreting content. Require complete evidence when the task depends on it.
  Use JSON pointers, SSE/NDJSON pages, byte ranges, or private artifacts; do not
  dump a full large body or silently refetch to cover an incomplete observation.
- Reuse `browser headless start` and `--browser headless` for a private persistent
  task profile. `--headed` supports manual login only in that profile. Browser
  evaluation results should stay small; archive-pinned artifacts carry bodies.
- Read `docs/body-capture.md`, `rep describe body`, and `rep describe headless` for current
  contracts. Body inline success output is bounded JSON, including --head.
