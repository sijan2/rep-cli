#!/usr/bin/env python3
"""Download an authenticated URL with the version-matched rep client."""
import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[3] / "python"))
from repclient import Client, RepError


def download(args):
    argv = ["download", args.url, str(Path(args.output).expanduser()), "--timeout", str(args.timeout) + "s"]
    if args.overwrite:
        argv.append("--overwrite")
    options = {"inherit_scope": True}
    if args.rep_bin:
        options["binary"] = args.rep_bin
    return Client(**options).call(*argv, timeout=args.timeout + 10)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("url")
    parser.add_argument("output")
    parser.add_argument("--rep-bin")
    parser.add_argument("--timeout", type=float, default=120)
    parser.add_argument("--overwrite", action="store_true")
    args = parser.parse_args()
    if not 1 <= args.timeout <= 1800:
        parser.error("--timeout must be between 1 and 1800 seconds")
    try:
        result = download(args)
    except (RepError, OSError):
        print(json.dumps({"error": "download failed; inspect the rep error and destination"}), file=sys.stderr)
        return 1
    print(json.dumps(result))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
