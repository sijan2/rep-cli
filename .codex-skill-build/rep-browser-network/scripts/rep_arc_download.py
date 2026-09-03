#!/usr/bin/env python3
"""Stream an authenticated URL through Arc's Network/IO CDP domains."""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
from urllib.parse import urlparse


DEFAULT_REP = "~/.local/bin/rep"

BINARY_OUTPUT_SUFFIXES = frozenset({
    ".7z", ".a", ".aab", ".apk", ".bin", ".bz2", ".dmg", ".dll",
    ".dylib", ".exe", ".gif", ".gz", ".img", ".ipa", ".iso", ".jar",
    ".jpeg", ".jpg", ".macho", ".mkv", ".mov", ".mp3", ".mp4", ".otf",
    ".pdf", ".pkg", ".png", ".rar", ".so", ".tar", ".ttf", ".war",
    ".wasm", ".webp", ".woff", ".woff2", ".xz", ".zip",
})
HTML_CONTENT_TYPES = frozenset({"text/html", "application/xhtml+xml"})
HTML_SNIFF_BYTES = 8192


class DownloadError(RuntimeError):
    pass


def resolve_rep(explicit):
    candidate = explicit or shutil.which("rep") or DEFAULT_REP
    path = Path(candidate).expanduser()
    if not path.is_file() or not os.access(path, os.X_OK):
        raise DownloadError("rep executable was not found")
    return str(path)


def run_rep(rep, args, deadline):
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise DownloadError("download timed out")
    env = os.environ.copy()
    env["NO_COLOR"] = "1"
    try:
        completed = subprocess.run(
            [rep, *args, "-j", "--raw-json"],
            check=False,
            capture_output=True,
            text=True,
            timeout=max(1.0, remaining),
            env=env,
        )
    except subprocess.TimeoutExpired as error:
        raise DownloadError("rep command timed out") from error
    if completed.returncode != 0:
        detail = completed.stderr.strip() or completed.stdout.strip() or "unknown rep failure"
        raise DownloadError(detail)
    try:
        payload = json.loads(completed.stdout)
    except json.JSONDecodeError as error:
        raise DownloadError("rep returned non-JSON output") from error
    if isinstance(payload, dict) and payload.get("error"):
        problem = payload["error"]
        if isinstance(problem, dict):
            raise DownloadError(str(problem.get("message") or problem.get("code") or "rep command failed"))
        raise DownloadError(str(problem))
    return payload


def best_effort(rep, args):
    try:
        run_rep(rep, args, time.monotonic() + 15)
    except Exception:
        pass


def nested(payload, *keys):
    value = payload
    for key in keys:
        if not isinstance(value, dict) or key not in value:
            raise DownloadError("rep response was missing " + ".".join(keys))
        value = value[key]
    return value


def header_first(headers, wanted):
    if not isinstance(headers, dict):
        return ""
    for name, value in headers.items():
        if str(name).lower() != wanted.lower():
            continue
        if isinstance(value, list):
            return str(value[0]) if value else ""
        return str(value)
    return ""


def output_implies_binary(output):
    return any(suffix.lower() in BINARY_OUTPUT_SUFFIXES for suffix in output.suffixes)


def content_type_is_html(content_type):
    media_type = content_type.partition(";")[0].strip().lower()
    return media_type in HTML_CONTENT_TYPES


def prefix_looks_like_html(prefix):
    probe = bytes(prefix[:HTML_SNIFF_BYTES]).lstrip(b"\xef\xbb\xbf\x00\t\r\n ").lower()
    if probe.startswith((b"<!doctype html", b"<html", b"<head", b"<body")):
        return True
    if probe.startswith(b"<?xml") and b"<html" in probe[:2048]:
        return True
    return b"<html" in probe[:1024] and (b"<head" in probe[:2048] or b"<body" in probe[:2048])


def download(args):
    parsed = urlparse(args.url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise DownloadError("URL must use http:// or https://")

    output = Path(args.output).expanduser().resolve()
    if not output.parent.is_dir():
        raise DownloadError("output parent directory does not exist")
    if output.exists() and not args.overwrite:
        raise DownloadError("output already exists; pass --overwrite to replace it")

    rep = resolve_rep(args.rep_bin)
    deadline = time.monotonic() + args.timeout
    tab_id = None
    stream = ""
    attached = False
    temporary_path = None
    byte_count = 0
    digest = hashlib.sha256()
    prefix = bytearray()
    status = 0
    content_type = ""

    try:
        status_payload = run_rep(rep, ["browser", "status", "--browser", "arc"], deadline)
        if not status_payload.get("connected"):
            raise DownloadError("rep browser bridge is not connected")

        created = run_rep(
            rep,
            ["browser", "create", "about:blank", "--browser", "arc"],
            deadline,
        )
        tab_id = int(nested(created, "tab_id"))

        run_rep(rep, ["browser", "attach", "--browser", "arc", "--tab", str(tab_id)], deadline)
        attached = True
        frame_tree = run_rep(
            rep,
            ["browser", "cdp", "Page.getFrameTree", "--browser", "arc", "--tab", str(tab_id)],
            deadline,
        )
        frame_id = str(nested(frame_tree, "result", "frameTree", "frame", "id"))

        loaded = run_rep(
            rep,
            [
                "browser", "cdp", "Network.loadNetworkResource",
                "--browser", "arc", "--tab", str(tab_id),
                "--params", json.dumps({
                    "frameId": frame_id,
                    "url": args.url,
                    "options": {
                        "disableCache": bool(args.no_cache),
                        "includeCredentials": not args.omit_credentials,
                    },
                }),
            ],
            deadline,
        )
        resource = nested(loaded, "result", "resource")
        if not resource.get("success"):
            detail = resource.get("netErrorName") or resource.get("netError") or "resource load failed"
            raise DownloadError(str(detail))
        status = int(resource.get("httpStatusCode") or 0)
        if status < 200 or status >= 300:
            raise DownloadError("resource returned HTTP status %d" % status)
        stream = str(resource.get("stream") or "")
        if not stream:
            raise DownloadError("resource load returned no IO stream")
        content_type = header_first(resource.get("headers"), "content-type")

        temp = tempfile.NamedTemporaryFile(
            mode="wb",
            prefix=".%s." % output.name,
            suffix=".part",
            dir=str(output.parent),
            delete=False,
        )
        temporary_path = Path(temp.name)
        os.chmod(temporary_path, 0o600)
        try:
            while True:
                read = run_rep(
                    rep,
                    [
                        "browser", "cdp", "IO.read",
                        "--browser", "arc", "--tab", str(tab_id),
                        "--params", json.dumps({"handle": stream, "size": args.chunk_size}),
                    ],
                    deadline,
                )
                result = nested(read, "result")
                data = str(result.get("data") or "")
                if result.get("base64Encoded"):
                    try:
                        chunk = base64.b64decode(data, validate=True)
                    except Exception as error:
                        raise DownloadError("CDP returned invalid base64 data") from error
                else:
                    chunk = data.encode("utf-8")
                if chunk:
                    temp.write(chunk)
                    digest.update(chunk)
                    byte_count += len(chunk)
                    if len(prefix) < HTML_SNIFF_BYTES:
                        prefix.extend(chunk[:HTML_SNIFF_BYTES - len(prefix)])
                if result.get("eof"):
                    break
                if not chunk:
                    raise DownloadError("CDP stream stalled before EOF")
            temp.flush()
            os.fsync(temp.fileno())
        finally:
            temp.close()

        if byte_count == 0 and not args.allow_empty:
            raise DownloadError("download was empty; pass --allow-empty if expected")
        if byte_count < args.min_bytes:
            raise DownloadError(
                "download was %d bytes, below required minimum of %d" % (byte_count, args.min_bytes)
            )
        if (
            output_implies_binary(output)
            and not args.allow_html
            and (content_type_is_html(content_type) or prefix_looks_like_html(prefix))
        ):
            raise DownloadError(
                "binary output resolved to HTML; refusing to publish (pass --allow-html if intentional)"
            )
        if args.overwrite:
            os.replace(temporary_path, output)
        else:
            try:
                os.link(temporary_path, output)
            except FileExistsError as error:
                raise DownloadError("output appeared during transfer; refusing to overwrite it") from error
            temporary_path.unlink()
        temporary_path = None
        os.chmod(output, 0o600)
        return {
            "ok": True,
            "output": str(output),
            "bytes": byte_count,
            "sha256": digest.hexdigest(),
            "status": status,
            "content_type": content_type,
        }
    finally:
        if stream and tab_id is not None:
            best_effort(
                rep,
                [
                    "browser", "cdp", "IO.close", "--browser", "arc", "--tab", str(tab_id),
                    "--params", json.dumps({"handle": stream}),
                ],
            )
        if attached and tab_id is not None:
            best_effort(rep, ["browser", "detach", "--browser", "arc", "--tab", str(tab_id)])
        if tab_id is not None:
            best_effort(rep, ["browser", "close", str(tab_id), "--browser", "arc"])
        if temporary_path is not None:
            try:
                temporary_path.unlink()
            except FileNotFoundError:
                pass


def parse_args():
    parser = argparse.ArgumentParser(
        description="Download an HTTP(S) resource through the signed-in Arc profile without UI automation."
    )
    parser.add_argument("url")
    parser.add_argument("output", help="Explicit output file; parent directory must already exist")
    parser.add_argument("--rep-bin", help="Path to rep executable")
    parser.add_argument("--timeout", type=float, default=300.0, help="Total timeout in seconds")
    parser.add_argument("--chunk-size", type=int, default=4 * 1024 * 1024, help="CDP read size in bytes")
    parser.add_argument("--no-cache", action="store_true")
    parser.add_argument("--omit-credentials", action="store_true")
    parser.add_argument("--allow-empty", action="store_true")
    parser.add_argument(
        "--allow-html",
        action="store_true",
        help="Allow HTML when the output extension normally implies a binary artifact",
    )
    parser.add_argument(
        "--min-bytes",
        type=int,
        default=0,
        help="Require at least this many response bytes before publishing",
    )
    parser.add_argument("--overwrite", action="store_true")
    arguments = parser.parse_args()
    if arguments.timeout <= 0:
        parser.error("--timeout must be positive")
    if arguments.chunk_size < 64 * 1024 or arguments.chunk_size > 4 * 1024 * 1024:
        parser.error("--chunk-size must be between 65536 and 4194304")
    if arguments.min_bytes < 0:
        parser.error("--min-bytes must be non-negative")
    return arguments


def main():
    try:
        result = download(parse_args())
    except (DownloadError, OSError) as error:
        print(json.dumps({"ok": False, "error": str(error)}), file=sys.stderr)
        return 1
    print(json.dumps(result, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
