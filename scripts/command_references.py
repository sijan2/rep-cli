#!/usr/bin/env python3
"""Extract command contracts without running examples or reading transcripts.

Scans Markdown examples, literal rep hints in source, Python call/list argv,
and JS/shell rep invocations. Dynamic argument slots become VALUE; flag names
and command paths remain testable. Go tests resolve these against Cobra, so
hidden flags and compatibility spellings are included during migration.
"""
import ast
import json
from pathlib import Path
import re
import shlex
import sys


def tokens(text):
    text = text.replace("\\\n", " ").strip()
    try:
        return shlex.split(text, comments=True)
    except ValueError:
        return text.split()


def extract(root):
    roots = [root / "docs", root / "skills", root / "scripts", root / "internal/commands",
             root.parent / "rep-workspace/tools"]
    files = [root / "AGENTS.md", root / "README.md", root.parent / "rep-workspace/AGENTS.md",
             root.parent / "rep-workspace/README.md", root.parent / "rep-workspace/Makefile"]
    for directory in roots:
        if directory.exists():
            files += [p for p in directory.rglob("*") if p.is_file() and p.suffix in (".md", ".go", ".py", ".js", ".mjs", ".sh")
                      and not p.name.endswith("_test.go") and not p.name.startswith("test_")
                      and "test" not in p.parts and "tests" not in p.parts]
    for directory in (root.parent / "rep-workspace/tools/sitelearn", root.parent / "rep-workspace/tools/fbmp"):
        if directory.exists():
            files += [p for p in directory.iterdir() if p.is_file() and not p.suffix]
    rows, seen = [], set()

    def add(path, line, argv, kind):
        if argv and argv[0] == "rep":
            argv = argv[1:]
        if not argv:
            return
        # Prose and Cobra placeholders do not promise an executable command.
        if argv[0] in ("CLI", "drives", "supplies", "[command]"):
            return
        argv = [arg.rstrip("`,") for arg in argv]
        # A compact command-path alternative promises every named path.
        for index, arg in enumerate(argv[:3]):
            if re.fullmatch(r"[a-z-]+(?:[|/][a-z-]+)+", arg):
                for choice in re.split(r"[|/]", arg):
                    add(path, line, argv[:index] + [choice] + argv[index+1:], kind)
                return
        key = (str(path.relative_to(root.parent)), line, tuple(argv))
        if key not in seen:
            seen.add(key)
            rows.append({"file": key[0], "line": line, "argv": argv, "kind": kind})

    for path in sorted(set(files)):
        if not path.exists() or path.name == Path(__file__).name:
            continue
        source = path.read_text()
        if path.suffix == ".py" or (not path.suffix and source.startswith("#!/usr/bin/env python")):
            try:
                tree = ast.parse(source)
            except SyntaxError:
                continue
            for node in ast.walk(tree):
                parts = None
                if isinstance(node, ast.Call):
                    name = node.func.attr if isinstance(node.func, ast.Attribute) else getattr(node.func, "id", "")
                    if name in ("call", "cli", "rep") and node.args and isinstance(node.args[0], ast.Constant) and isinstance(node.args[0].value, str):
                        parts = node.args
                        if name in ("rep", "cli") and path.name == "verify_agent_context.py":
                            parts = node.args[2:]  # wrapper's first two args are scope
                if isinstance(node, (ast.List, ast.Tuple)) and node.elts:
                    first = node.elts[0]
                    if isinstance(first, ast.Constant) and first.value in ("rep", "browser", "body", "list", "detail", "summary", "jev", "scope"):
                        parts = node.elts
                if parts:
                    add(path, node.lineno, [x.value if isinstance(x, ast.Constant) and isinstance(x.value, str) else "VALUE" for x in parts], "argv")
        # Quoted/inline examples. Stop at a quote delimiter, not at a shell
        # metacharacter inside an argument. Multi-line code blocks are scanned
        # separately as command lines. Plain prose is deliberately not a call.
        for number, line in enumerate(source.splitlines(), 1):
            for match in re.finditer(r'`(rep(?:\s+[^`\n]*)?)`', line):
                add(path, number, tokens(match.group(1)), "example")
            stripped = line.strip().lstrip("$ ")
            stripped = re.split(r"\s{2,}(?=[A-Z])", stripped)[0]
            if (path.suffix in (".md", ".sh", ".go") or path.name == "Makefile") and (stripped.startswith("rep ") or stripped == "rep"):
                add(path, number, tokens(stripped), "example")
            if path.suffix in (".go", ".js", ".mjs"):
                for match in re.finditer(r'"(rep [^"\n]+)"', line):
                    add(path, number, tokens(match.group(1)), "hint")
    return sorted(rows, key=lambda row: (row["file"], row["line"], row["argv"]))


if __name__ == "__main__":
    print(json.dumps(extract(Path(sys.argv[1]).resolve())))
