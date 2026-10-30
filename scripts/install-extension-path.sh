#!/usr/bin/env bash
# Sourced by build_install.sh. Merely sourcing this file never writes config.

rep_extension_source() {
  local cli_root="$1" candidate
  if [[ -n "${REP_EXTENSION_PATH:-}" ]]; then
    candidate="$REP_EXTENSION_PATH"
  elif [[ -f "$cli_root/../rep/manifest.json" ]]; then
    candidate="$cli_root/../rep"
  else
    candidate="$cli_root/../extension"
  fi
  if [[ ! -f "$candidate/manifest.json" ]]; then
    echo 'Set REP_EXTENSION_PATH to the unpacked rep extension before installing.' >&2
    return 1
  fi
  (cd "$candidate" && pwd -P)
}

rep_config_directory() {
  if [[ -n "${XDG_CONFIG_HOME:-}" ]]; then
    printf '%s\n' "$XDG_CONFIG_HOME"
  elif [[ "$(uname -s)" == Darwin ]]; then
    printf '%s\n' "$HOME/Library/Application Support"
  else
    printf '%s\n' "$HOME/.config"
  fi
}

rep_record_extension_path() (
  set -e
  umask 077
  local directory temporary
  directory="$(rep_config_directory)/rep"
  mkdir -p "$directory"
  temporary="$(mktemp "$directory/.extension-path.XXXXXX")"
  trap 'rm -f "$temporary"' EXIT
  printf '%s\n' "$1" > "$temporary"
  chmod 600 "$temporary"
  mv -f "$temporary" "$directory/extension-path"
)
