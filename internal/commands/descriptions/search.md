# `rep search` contract

`rep search PATTERN` finds captured requests whose URL, headers or bodies match a
regular expression within the current task's capture (or `--saved HASH`). Results
are bounded by `--limit`/`--offset` after matching, so a small limit never hides
an earlier match. Filters mirror `rep list`: `--domain`, `--method`, `--status`,
`--status-range`, `--type`, `--primary`, and `--in` selects the fields searched
(url, headers, body, all).

Empty results are never silent: the output names the source, filters and a
concrete next command. Matches report the request ID, method, status and the
matched field; bodies are not echoed unless `--show-matches` is used.

## Advanced flags

These flags stay accepted with their existing behavior and are hidden from
`--help`: `--ignore-case`/`-i`, `--invert`/`-v` (requests that do not match),
`--show-matches` (print each match), `--context N` (characters of context around
a match).
