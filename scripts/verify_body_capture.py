#!/usr/bin/env python3
"""Real task-owned headless smoke; all HTTP fixtures bind to loopback.

REP_BINARY=/tmp/rep-body-cli REP_HOST_BINARY=/tmp/rep-body-host \
  python3 scripts/verify_body_capture.py [--with-jev]

--with-jev makes one benign configured Jev API call for a fixture button.
Everything else remains local. No normal browser profile or tab is touched.
"""
from __future__ import annotations

import argparse
import gzip
import hashlib
import http.server
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading
import time


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


JSON_BODY = json.dumps({"marker": "gzip-complete", "payload": "x" * (2 * 1024 * 1024 + 12345), "unicode": "नेपाल 🙂"}, ensure_ascii=False, separators=(",", ":")).encode()
GZIP_BODY = gzip.compress(JSON_BODY, mtime=0)
NDJSON_BODY = b"".join(json.dumps({"i": i, "text": "record"}, separators=(",", ":")).encode() + b"\n" for i in range(2000))
SSE_BODY = b'id: 1\nevent: progress\ndata: {"step":1}\n\nid: 2\nevent: complete\ndata: {"done":true}\n\n'
BINARY_BODY = bytes(range(256)) * 9000
HTML_BODY = b'<!doctype html><meta charset="utf-8"><title>Rep body fixture</title><main><h1>Capture verification</h1><button>Review complete capture</button></main>'


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_):
        pass

    def handle(self):
        # Chromium closes idle keepalive connections when its owned task stops.
        try:
            super().handle()
        except (BrokenPipeError, ConnectionResetError):
            pass

    def do_GET(self):
        try:
            if self.path == "/gzip":
                self.fixed(GZIP_BODY, "application/json", "gzip")
            elif self.path == "/ndjson":
                self.chunked(NDJSON_BODY, "application/x-ndjson")
            elif self.path == "/sse-ended":
                self.chunked(SSE_BODY, "text/event-stream")
            elif self.path == "/sse-open":
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-cache")
                self.send_header("Transfer-Encoding", "chunked")
                self.end_headers()
                for index in range(40):
                    self.chunk(f'id: {index}\ndata: {{"tick":{index}}}\n\n'.encode())
                    time.sleep(0.2)
                self.wfile.write(b"0\r\n\r\n")
            elif self.path == "/binary":
                self.fixed(BINARY_BODY, "application/octet-stream")
            elif self.path == "/favicon.ico":
                self.fixed(b"", "image/x-icon", status=204)
            else:
                self.fixed(HTML_BODY, "text/html; charset=utf-8")
        except (BrokenPipeError, ConnectionResetError):
            pass

    def fixed(self, payload, content_type, encoding=None, status=200):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(payload)))
        self.send_header("Cache-Control", "no-store")
        if encoding:
            self.send_header("Content-Encoding", encoding)
        self.end_headers()
        self.wfile.write(payload)

    def chunk(self, payload):
        self.wfile.write(f"{len(payload):x}\r\n".encode() + payload + b"\r\n")
        self.wfile.flush()

    def chunked(self, payload, content_type):
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Transfer-Encoding", "chunked")
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        # Deliberately split inside application records and UTF-8-safe text.
        for offset in range(0, len(payload), 137):
            self.chunk(payload[offset:offset + 137])
        self.wfile.write(b"0\r\n\r\n")
        self.wfile.flush()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--with-jev", action="store_true")
    options = parser.parse_args()
    binary = shutil.which(os.environ.get("REP_BINARY", "rep"))
    host = shutil.which(os.environ.get("REP_HOST_BINARY", "rep-host"))
    require(binary and host, "REP_BINARY and REP_HOST_BINARY must name executable binaries")
    extension = os.environ.get("REP_EXTENSION_PATH", str(Path(__file__).resolve().parents[2] / "rep"))
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.daemon_threads = True
    server_thread = threading.Thread(target=server.serve_forever, daemon=True)
    server_thread.start()
    origin = f"http://127.0.0.1:{server.server_port}"
    bridge_dir = None
    started = False
    try:
        with tempfile.TemporaryDirectory(prefix="rep-body-", dir="/tmp") as temporary:
            directory = Path(temporary)
            env = os.environ.copy()
            for key in ("REP_WORKSPACE", "REP_TASK", "REP_BRIDGE_DIR", "REPLIVE_PATH", "REPANDROID_PATH"):
                env.pop(key, None)
            env["XDG_DATA_HOME"] = str(directory / "data")
            env["NO_COLOR"] = "1"
            base = [str(Path(binary).resolve()), "--workspace", "body-smoke", "--task", "agent"]

            def cli(*args, ok=True):
                before = time.monotonic()
                result = subprocess.run(base + list(args) + ["--raw-json", "-j"], env=env, cwd=directory, capture_output=True, timeout=40)
                elapsed = time.monotonic() - before
                require((result.returncode == 0) == ok, f"CLI {' '.join(args[:2])} returned unexpected exit {result.returncode}: {result.stderr.decode(errors='replace')[:240]}")
                if not ok:
                    return None, elapsed, result.stdout
                try:
                    value = json.loads(result.stdout)
                except json.JSONDecodeError as error:
                    raise RuntimeError(f"CLI {args[0]} did not emit one JSON value") from error
                return value, elapsed, result.stdout

            try:
                state, cold, _ = cli("browser", "headless", "start", "--extension", extension, "--host", str(Path(host).resolve()))
                started = True
                bridge_dir = Path(state["bridge_dir"])
                require(state["connected"] and state["running"], "headless task did not connect")
                reused, warm, _ = cli("browser", "headless", "start")
                require(reused["pid"] == state["pid"] and reused["connected"], "headless start did not reuse its task process")
                opened, navigation, _ = cli("browser", "open", origin + "/", "--browser", "headless", "--keep-tab", "--idle", "100ms")
                tab = str(opened["tab_id"])
                require(tab != "None", "fixture tab was not retained")
                records = {}
                live_path = directory / "data/rep-cli/workspaces/body-smoke/tasks/agent/live.json"
                for route, expected in (("gzip", JSON_BODY), ("ndjson", NDJSON_BODY), ("sse-ended", SSE_BODY), ("binary", BINARY_BODY)):
                    url_file = directory / "url.txt"
                    url_file.write_text(origin + "/" + route)
                    capture, duration, _ = cli("browser", "fetch", "@" + str(url_file), "--browser", "headless", "--tab", tab, "--cache", "no-store")
                    live = json.loads(live_path.read_text())
                    matches = [request for request in live["requests"] if request["url"] == origin + "/" + route]
                    require(len(matches) == 1, f"{route}: primary request was not captured uniquely")
                    request_id = matches[0]["id"]
                    info, _, info_raw = cli("body", request_id, "--info", "--require-complete", "--max-bytes", "2048")
                    require(len(info_raw) <= 2048 and info["body_capture"]["state"] == "complete", f"{route}: body completeness or bounded info failed")
                    require(info["body_bytes"] == len(expected), f"{route}: decoded body length mismatch")
                    digest = hashlib.sha256(expected).hexdigest()
                    require(info["body_capture"]["sha256"] == digest, f"{route}: captured digest mismatch")
                    saved, _, save_raw = cli("body", request_id, "--save", "--require-complete", "--max-bytes", "2048")
                    artifact = Path(saved["path"])
                    require(artifact.is_relative_to(directory) and artifact.read_bytes() == expected, f"{route}: raw saved artifact differs from response bytes")
                    require(len(save_raw) <= 2048 and artifact.stat().st_mode & 0o777 == 0o600, f"{route}: artifact metadata budget or privacy failed")
                    records[route] = {"id": request_id, "archive": capture["saved_hash_id"], "bytes": len(expected), "capture_seconds": round(duration, 3), "sha256": digest}
                    if route == "gzip":
                        selected, _, _ = cli("body", request_id, "--pointer", "/marker", "--require-complete")
                        require(json.loads(selected["body"]) == "gzip-complete", "gzip JSON pointer selection failed")
                    if route in ("ndjson", "sse-ended"):
                        projected, _, _ = cli("body", request_id, "--format", "ndjson" if route == "ndjson" else "sse", "--records", "2", "--require-complete")
                        require(projected["selection"]["records"] == 2, f"{route}: application record parsing failed")
                        parsed_records = json.loads(projected["body"])
                        if route == "ndjson":
                            require([record["value"]["i"] for record in parsed_records] == [0, 1] and projected["selection"]["more_records"], "NDJSON records were confused with HTTP chunks")
                        else:
                            require([record["event"] for record in parsed_records] == ["progress", "complete"], "SSE event boundaries were not preserved")

                url_file.write_text(origin + "/sse-open")
                _, stream_duration, _ = cli("browser", "fetch", "@" + str(url_file), "--browser", "headless", "--tab", tab, "--timeout", "2s", "--cache", "no-store")
                live = json.loads(live_path.read_text())
                stream = [request for request in live["requests"] if request["url"] == origin + "/sse-open"]
                require(len(stream) == 1, "open SSE stream request missing")
                stream_info, _, _ = cli("body", stream[0]["id"], "--info")
                require(stream_info["body_capture"]["state"] == "partial" and stream_info["body_bytes"] > 0, "unfinished SSE did not retain an explicit partial prefix")
                cli("body", stream[0]["id"], "--info", "--require-complete", ok=False)
                first = records["gzip"]
                archived, _, _ = cli("body", first["id"], "--saved", first["archive"], "--info", "--require-complete")
                require(archived["body_bytes"] == len(JSON_BODY) and archived["body_capture"]["sha256"] == first["sha256"], "earlier complete gzip archive was replaced or lost")
                jev = None
                if options.with_jev:
                    decision, elapsed, _ = cli("jev", "select", "--browser", "headless", "--tab", tab, "--goal", "Find the button labeled Review complete capture", "--no-cache")
                    selected = decision.get("selected") or {}
                    require(selected.get("name") == "Review complete capture", "Jev did not find the fixture button")
                    jev = {"status": decision["status"], "seconds": round(elapsed, 3), "confidence": decision.get("confidence")}
                result = {
                    "passed": True, "browser": "full Chromium headless", "cold_start_seconds": round(cold, 3),
                    "reuse_seconds": round(warm, 3), "navigation_capture_seconds": round(navigation, 3),
                    "gzip_wire_bytes": len(GZIP_BODY),
                    "complete_bodies": {route: {"bytes": record["bytes"], "capture_seconds": record["capture_seconds"]} for route, record in records.items()},
                    "unfinished_sse": {"state": stream_info["body_capture"]["state"], "bytes": stream_info["body_bytes"], "capture_seconds": round(stream_duration, 3)},
                    "earlier_archive_verified": True, "jev": jev,
                }
            finally:
                if started:
                    stopped, _, _ = cli("browser", "headless", "stop")
                    require(not stopped["running"], "owned headless browser did not stop")
                    started = False
                if bridge_dir is not None and str(bridge_dir).startswith(f"/tmp/rep-headless-{os.getuid()}-"):
                    shutil.rmtree(bridge_dir, ignore_errors=True)
            print(json.dumps(result, separators=(",", ":")))
    finally:
        server.shutdown()
        server.server_close()
        server_thread.join(timeout=2)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        raise SystemExit(f"Headless body verification failed: {error}")
