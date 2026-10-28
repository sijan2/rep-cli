#!/usr/bin/env python3
"""Inventory the visible Cobra command tree without treating prose as commands.

The older /tmp/flagscan2.py counts headings in Long help as command groups. In
particular stats' Fields section repeats its own help at invented child paths.
Only Cobra's generated sections after Usage are part of the command tree.
Counts include --help to remain comparable with that scanner. Hidden aliases
and hidden flags are deliberately absent: this measures the advertised surface,
not the number of declarations in Go source or the accepted compatibility API.
"""

import argparse
from collections import Counter
import json
from pathlib import Path
import re
import subprocess


def parse_help(help_text):
    commands, local, inherited = [], [], []
    mode = None
    in_usage = False
    for line in help_text.splitlines():
        if line == "Usage:":
            in_usage = True
            mode = None
            continue
        if not in_usage:
            continue
        if line == "Flags:":
            mode = "local"
            continue
        if line == "Global Flags:":
            mode = "inherited"
            continue
        if line.startswith('Use "') or line in ("Aliases:", "Examples:"):
            mode = None
            continue
        # Cobra also emits application-defined command group titles here.
        if re.fullmatch(r"[A-Za-z][A-Za-z /-]*:", line):
            mode = "commands"
            continue
        if mode == "commands":
            match = re.match(r"^  ([a-z][a-z0-9-]*)\s+\S", line)
            if match:
                commands.append(match.group(1))
        elif mode in ("local", "inherited"):
            match = re.match(r"^  (?:-[A-Za-z?], )?\s*--([a-z][a-z0-9-]*)\b", line)
            if match:
                (local if mode == "local" else inherited).append(match.group(1))
    return commands, local, inherited


def inventory(binary):
    rows = []

    def walk(path):
        result = subprocess.run([binary, *path, "--help"], text=True,
                                capture_output=True, timeout=20, check=True)
        children, local, inherited = parse_help(result.stdout)
        children = [child for child in children if child not in ("help", "completion")]
        rows.append({"path": " ".join(path), "children": children,
                     "local_flags": sorted(local), "inherited_flags": sorted(inherited)})
        for child in children:
            walk([*path, child])

    walk([])
    root, commands = rows[0], rows[1:]
    frequencies = Counter(flag for row in commands for flag in row["local_flags"])
    return {
        "binary": str(Path(binary).resolve()),
        "method": "generated help sections after Usage; excludes help/completion commands; includes implicit help flags; visible surface only",
        "metrics": {
            "root_flags": len(root["local_flags"]),
            "commands": len(commands),
            "commands_with_local_flags": sum(bool(row["local_flags"]) for row in commands),
            "leaf_commands": sum(not row["children"] for row in commands),
            "local_flag_occurrences": sum(frequencies.values()),
            "distinct_local_flag_names": len(frequencies),
            "inherited_flag_occurrences": sum(len(row["inherited_flags"]) for row in commands),
            "effective_flag_occurrences": sum(len(row["local_flags"]) + len(row["inherited_flags"]) for row in commands),
        },
        "local_flag_frequency": dict(sorted(frequencies.items(), key=lambda item: (-item[1], item[0]))),
        "commands": rows,
    }


def render_text(report):
    lines = ["METHOD: " + report["method"], "BINARY: " + report["binary"]]
    lines.extend(f"{key.upper()}: {value}" for key, value in report["metrics"].items())
    lines.append("\nPATH | LOCAL FLAGS | INHERITED FLAGS")
    for row in report["commands"]:
        lines.append(f'{row["path"] or "(root)"} | {",".join(row["local_flags"])} | {",".join(row["inherited_flags"])}')
    return "\n".join(lines) + "\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", help="path to a built rep executable")
    parser.add_argument("--json", dest="json_path", help="write complete JSON inventory")
    parser.add_argument("--text", dest="text_path", help="write complete text inventory")
    args = parser.parse_args()
    report = inventory(str(Path(args.binary).resolve()))
    rendered = render_text(report)
    if args.json_path:
        Path(args.json_path).write_text(json.dumps(report, indent=2) + "\n")
    if args.text_path:
        Path(args.text_path).write_text(rendered)
    print(json.dumps(report["metrics"], indent=2))


if __name__ == "__main__":
    main()
