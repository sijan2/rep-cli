# rep android

Read legacy shared Android capture data. Scoped tasks are unsupported: explicit
`--global` is required for stored traffic. This command does not create an
independent device or account namespace.

`rep --global android [PACKAGE]` lists packages when PACKAGE is absent; a partial
package name selects its captured requests. `--packages/-p` lists packages and
`--domains/-d` shows domains per package. Device-management subcommands retain
their existing help and behavior.

## Request selection and display

- `--select errors,mutations`: intersect presets, comma-separated or repeatable.
  `errors` preserves the legacy Android predicate (status at least 400, reading
  the top-level status or nested response status); `mutations` selects
  POST/PUT/DELETE/PATCH. No `api` or `interesting` preset exists here.
- `--view line|detail`: text layout; line is the default.
- `--domain`: partial domain match; `--method/-m`: method filter.
- `--since`: duration such as 5m or 1h; `--all`: include skipped requests.
- `--limit/-n`: maximum requests in text output, default 50.
- `--json/-j`: existing JSON output. Text view and text limit do not change the
  legacy JSON projection or its filtering behavior.

`--errors`, `--mutations`, `--line`, and `--detail` still parse (including explicit
false) as hidden legacy flags and emit notes on stderr. Their legacy precedence
is preserved: detail wins; `--line=false` alone still uses the line layout on
Android. Do not combine --select with legacy presets or --view with legacy
layout flags. Invalid presets/layouts and mixed syntaxes return invalid_argument.

`--budget` was never consumed by the Android renderer. It remains accepted as a
hidden no-op for compatibility and emits a stderr note to omit it; no output
budget or truncation guarantee is added by this flag.
