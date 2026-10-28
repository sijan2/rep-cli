# rep setup

One-shot recon initialization for a target domain.

## Default behavior: DRY RUN

Without `--write`, `rep setup` previews every config mutation but does NOT
modify `store.json`. It still:

- Extracts auth tokens and writes them to `~/.rep/auth-<target>.env` (0600).
- Prints a `config_diff` so the agent can verify the proposed primaries
  and noise-ignore list.

To commit: rerun with `--write`.

## Secret handling

`--json` emits `TokenInfoRedacted` by default:
```json
{ "name": "...", "value_preview": "Abcd…WXYZ", "env_name": "TGT_AUTH",
  "env_file": "~/.rep/auth-target.env", "header": "authorization" }
```

Raw values only appear in JSON with `--include-secrets` (big red warning
to stderr). Prefer sourcing the `env_file` in the user's shell and
referencing `env_name` downstream.

## Exit status

Exit 0 on both dry-run and write. Non-zero only on load/save errors.
