# Scripts

- `build_install.sh --host`: run the Go tests, then install `rep` and `rep-host` to `~/.local/bin`. Installation also records the adjacent extension's source path; set `REP_EXTENSION_PATH` for another extension checkout. The script can be invoked from the companion workspace or another directory.
- `flag_inventory.py BINARY`: count the visible command and flag surface of a built binary.
- `verify/`: synthetic browser journeys driven only through rep subprocesses. `verify_reference.py`, `verify_interaction.mjs` and `verify_lab.py` use a matching private build and explicit private XDG/profile/extension paths. `dispositions.json` records the seven retired legacy harnesses and their replacements; retired entrypoints exit before browser access.
- `verify/verify_journeys.py --rep /path/to/rep --output /tmp/journeys.json`: creates a private `/tmp` profile/XDG slot automatically and checks article/source reads, pagination, UI plans, captured API replay/export/download, thread coverage, design evidence, screenshots, public lab handles and sequential writes. It retains screenshots beside the report and stops its browser and port-0 fixture server. Build `rep-host` alongside `rep` and use `--extension` when the matching extension is elsewhere.
- `android/`: device capture helpers.
