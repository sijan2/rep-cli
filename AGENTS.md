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
