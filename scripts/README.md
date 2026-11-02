# Scripts

- `build_install.sh --host --lab`: run the core and tagged Go tests, then install `rep`, `rep-host` and `rep-lab` to `~/.local/bin`. Installation also records the adjacent extension's source path; set `REP_EXTENSION_PATH` for another extension checkout. The script can be invoked from the companion workspace or another directory.
- `flag_inventory.py BINARY`: count the visible command and flag surface of a built binary.
- `verify/`: synthetic browser journeys driven only through rep subprocesses. `verify_reference.py`, `verify_interaction.mjs` and `verify_lab.py` use a matching private build and explicit private XDG/profile/extension paths. `dispositions.json` records the seven retired legacy harnesses and their replacements; retired entrypoints exit before browser access.
- `android/`: device capture helpers.
