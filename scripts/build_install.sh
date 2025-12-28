#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
VERSION_FILE="${VERSION_FILE:-$ROOT_DIR/VERSION}"
BUILD_HOST=false

usage() {
  cat <<'EOF'
Usage: scripts/build_install.sh [--host] [--install-dir <dir>]

Builds the rep CLI and installs it into the target directory.

Options:
  --host               Also build and install rep-host.
  --install-dir <dir>  Install directory (default: $HOME/.local/bin)
  -h, --help           Show this help.

Environment:
  INSTALL_DIR          Alternative to --install-dir.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --host)
      BUILD_HOST=true
      ;;
    --install-dir)
      shift
      INSTALL_DIR="${1:-}"
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
  shift
done

mkdir -p "$INSTALL_DIR"

VERSION_VALUE="${VERSION:-}"
if [[ -z "$VERSION_VALUE" ]]; then
  if [[ -f "$VERSION_FILE" ]]; then
    VERSION_VALUE="$(cat "$VERSION_FILE")"
  else
    VERSION_VALUE="dev"
  fi
fi

COMMIT_VALUE="$(git -C "$ROOT_DIR" rev-parse --short HEAD 2>/dev/null || echo "none")"
BUILD_DATE_VALUE="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"

LDFLAGS=(
  "-X" "github.com/repplus/rep-cli/cmd.Version=${VERSION_VALUE}"
  "-X" "github.com/repplus/rep-cli/cmd.Commit=${COMMIT_VALUE}"
  "-X" "github.com/repplus/rep-cli/cmd.BuildDate=${BUILD_DATE_VALUE}"
)

go build -ldflags "${LDFLAGS[*]}" -o "$INSTALL_DIR/rep" "$ROOT_DIR"
echo "Installed: $INSTALL_DIR/rep"

if [[ "$BUILD_HOST" == "true" ]]; then
  go build -ldflags "${LDFLAGS[*]}" -o "$INSTALL_DIR/rep-host" "$ROOT_DIR/cmd/host"
  echo "Installed: $INSTALL_DIR/rep-host"
fi
