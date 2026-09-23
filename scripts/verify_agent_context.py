#!/usr/bin/env python3
"""Offline black-box verification of scoped captures and bounded context.

Run after building both binaries:
  REP_BINARY=/tmp/rep-scoped REP_HOST_BINARY=/tmp/rep-scoped-host \
    python3 scripts/verify_agent_context.py

This starts a real native host and CLI against a synthetic Native Messaging
peer. It never connects to a browser, website, Jev, or any network service.
All capture data, sockets, archives, and cursor checkpoints use a disposable
directory under /tmp. The host and directory are removed on success or failure.
"""

from __future__ import annotations

import concurrent.futures
import json
import os
from pathlib import Path
import shutil
import struct
import subprocess
import sys
import tempfile
import threading
import time
from urllib.parse import urlsplit


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def resolve_binary(name: str, fallback: str) -> str:
    value = os.environ.get(name, fallback)
    resolved = shutil.which(value)
    require(resolved is not None, f"{name} must identify an executable binary")
    return str(Path(resolved).resolve())


def read_exact(stream, count: int) -> bytes:
    chunks = []
    remaining = count
    while remaining:
        chunk = stream.read(remaining)
        if not chunk:
            raise EOFError("native host closed its output")
        chunks.append(chunk)
        remaining -= len(chunk)
    return b"".join(chunks)


class SyntheticBrowser:
    """Real host, synthetic extension; initial captures overlap at the RPC layer."""

    def __init__(self, host_binary: str, directory: Path, env: dict[str, str]):
        self.stderr = (directory / "host.stderr").open("wb")
        self.process = subprocess.Popen(
            [host_binary],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=self.stderr,
            env=env,
            cwd=directory,
            bufsize=0,
        )
        self.write_lock = threading.Lock()
        self.closed = threading.Event()
        self.errors: list[str] = []
        self.initial_results: list[dict] = []
        self.initial_released = False
        self.capture_count = 0
        self.reader = threading.Thread(target=self.run, daemon=True)
        self.reader.start()
        self.send({
            "action": "hello",
            "browser": "offline-fixture",
            "browser_label": "Offline fixture",
            "extension_id": "offline-fixture",
            "extension_version": "fixture",
        })

    def send(self, message: dict) -> None:
        payload = json.dumps(message, separators=(",", ":")).encode()
        frame = struct.pack("<I", len(payload)) + payload
        with self.write_lock:
            # Raw pipe writes may be partial for large request batches.
            remaining = memoryview(frame)
            while remaining:
                written = self.process.stdin.write(remaining)
                if not written:
                    raise RuntimeError("native host stopped reading fixture messages")
                remaining = remaining[written:]
            self.process.stdin.flush()

    def run(self) -> None:
        try:
            while not self.closed.is_set():
                header = read_exact(self.process.stdout, 4)
                size = struct.unpack("<I", header)[0]
                require(0 < size <= 32 * 1024 * 1024, "invalid native response length")
                message = json.loads(read_exact(self.process.stdout, size))
                if message.get("action") != "rpc":
                    require(message.get("success") is not False, "host rejected a fixture capture message")
                    continue
                method = message.get("method")
                if method in ("bridge.ping", "browser.status"):
                    self.send({
                        "action": "rpc_result", "id": message["id"],
                        "result": {"connected": True, "active_captures": 0},
                    })
                elif method == "browser.open":
                    self.capture(message)
                else:
                    self.send({
                        "action": "rpc_result", "id": message["id"],
                        "error": {"code": "fixture_unsupported", "message": "unsupported offline fixture RPC"},
                    })
        except EOFError:
            if not self.closed.is_set():
                self.errors.append("native host exited before validation completed")
        except Exception as error:
            self.errors.append(str(error))

    def capture(self, rpc: dict) -> None:
        raw_url = rpc.get("params", {}).get("url", "")
        parsed = urlsplit(raw_url)
        require(parsed.scheme == "https", "fixture expected an HTTPS URL label")
        if parsed.hostname == "ebay.example.test":
            site, tab, route = "ebay", 101, "items"
            count = 1000 if parsed.path == "/first" else 7
        elif parsed.hostname == "forms.example.test":
            site, tab, route, count = "form", 202, "applications", 200
        else:
            raise RuntimeError("fixture received an unexpected URL label")
        generation = parsed.path.strip("/")
        require(generation in ("first", "second"), "unexpected fixture generation")
        session = f"fixture-{site}-{generation}"
        origin = f"https://{parsed.hostname}"
        self.send({
            "action": "session_begin", "session_id": session,
            "url": raw_url, "tab_id": tab, "capture_mode": "navigate",
        })
        requests = []
        for index in range(count):
            requests.append({
                "id": f"req-{site}-{generation}-{index:04d}",
                "method": "GET",
                "url": f"{origin}/{route}/{index}?token=DO_NOT_SUMMARIZE",
                "page_url": raw_url,
                "resource_type": "fetch",
                "headers": {"Authorization": ["Bearer DO_NOT_SUMMARIZE"]},
                "response": {
                    "status": 200,
                    "headers": {"Content-Type": ["text/plain"]},
                    "body": f"offline-body:{site}:{generation}:{index:04d}",
                },
                "capture_source": "cdp", "tab_id": tab,
                "timestamp": 1700000000000 + index,
            })
        for start in range(0, count, 100):
            self.send({
                "action": "add_many", "session_id": session,
                "requests": requests[start:start + 100],
            })
        self.send({
            "action": "session_end", "session_id": session,
            "url": raw_url, "tab_id": tab, "capture_mode": "navigate",
        })
        self.capture_count += 1
        result = {
            "action": "rpc_result", "id": rpc["id"],
            "result": {
                "session_id": session, "final_url": raw_url,
                "tab_id": None, "tab_closed": True, "timed_out": False,
                "requests": count, "domains": 1, "response_bodies": count,
                "failed_requests": 0, "duration_ms": 1,
            },
        }
        if not self.initial_released:
            # Delay both RPC results until the shared host live file has been
            # replaced by the second capture. Each CLI must retrieve its own
            # sealed snapshot instead of accidentally reading the latest one.
            self.initial_results.append(result)
            if len(self.initial_results) == 2:
                for pending in self.initial_results:
                    self.send(pending)
                self.initial_released = True
        else:
            self.send(result)

    def check(self) -> None:
        require(not self.errors, self.errors[0] if self.errors else "fixture failed")
        require(self.process.poll() is None, "native host exited unexpectedly")

    def close(self) -> None:
        self.closed.set()
        try:
            self.process.stdin.close()
        except (OSError, ValueError):
            pass
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.terminate()
            try:
                self.process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=3)
        self.reader.join(timeout=2)
        self.process.stdout.close()
        self.stderr.close()


def main() -> None:
    rep_binary = resolve_binary("REP_BINARY", "rep")
    host_binary = resolve_binary("REP_HOST_BINARY", "rep-host")
    with tempfile.TemporaryDirectory(prefix="rep-ctx-", dir="/tmp") as temporary:
        directory = Path(temporary)
        env = os.environ.copy()
        for name in ("REP_WORKSPACE", "REP_TASK", "REPLIVE_PATH", "REPANDROID_PATH", "REP_BRIDGE_DIR", "XDG_DATA_HOME"):
            env.pop(name, None)
        env.update({
            "XDG_DATA_HOME": str(directory / "data"),
            "REP_BRIDGE_DIR": str(directory / "b"),
            "NO_COLOR": "1",
        })
        peer = SyntheticBrowser(host_binary, directory, env)
        try:
            deadline = time.monotonic() + 8
            while not list((directory / "b").glob("bridge-*.json")):
                peer.check()
                require(time.monotonic() < deadline, "host did not publish an isolated bridge")
                time.sleep(0.02)

            def cli(workspace: str, task: str, *args: str, ok: bool = True) -> tuple[dict, bytes]:
                peer.check()
                completed = subprocess.run(
                    [rep_binary, "--workspace", workspace, "--task", task, *args, "--raw-json", "-j"],
                    cwd=directory, env=env, capture_output=True, timeout=25,
                )
                peer.check()
                require((completed.returncode == 0) == ok,
                        f"CLI outcome mismatch for {workspace}/{task} {args[0]} (exit {completed.returncode})")
                try:
                    value = json.loads(completed.stdout)
                except (UnicodeDecodeError, json.JSONDecodeError) as error:
                    raise RuntimeError(f"CLI did not return one JSON value for {args[0]}") from error
                return value, completed.stdout

            def browse(workspace: str, task: str, url: str) -> dict:
                value, _ = cli(workspace, task, "browse", url, "--browser", "any", "--timeout", "15s")
                return value

            with concurrent.futures.ThreadPoolExecutor(max_workers=2) as executor:
                ebay_future = executor.submit(browse, "shopping", "ebay", "https://ebay.example.test/first")
                form_future = executor.submit(browse, "applications", "form", "https://forms.example.test/first")
                captures = {"ebay": ebay_future.result(), "form": form_future.result()}
            require(peer.initial_released, "initial captures were not interleaved")

            summaries = {}
            summary_bytes = {}
            for site, workspace, task, count, origin in (
                ("ebay", "shopping", "ebay", 1000, "https://ebay.example.test"),
                ("form", "applications", "form", 200, "https://forms.example.test"),
            ):
                capture = captures[site]
                preview = capture.get("captured_requests", [])
                require(len(preview) <= 16, "capture preview exceeded 16 handles")
                require(capture.get("captured_requests_total") == count, "capture count was not preserved")
                require(capture.get("captured_requests_omitted") == count - len(preview), "capture omitted count mismatch")
                require(capture.get("capture_snapshot_verified") is True, "capture was not verified")
                require(capture.get("workspace") == workspace and capture.get("task") == task, "capture scope mismatch")
                require(bool(capture.get("saved_hash_id")), "capture was not automatically archived")
                summary, raw = cli(workspace, task, "summary", "--max-bytes", "2048")
                require(len(raw) <= 2048, "summary exceeded byte budget including newline")
                require(summary["totals"]["requests"] == count, "summary included another capture or lost requests")
                require(summary["totals"]["groups"] == 1 and len(summary["groups"]) == 1, "duplicate route compression failed")
                require(summary["groups"][0]["origin"] == origin, "summary contains another site's origin")
                require(summary["provenance"]["workspace"] == workspace and summary["provenance"]["task"] == task, "summary provenance mismatch")
                require(summary["provenance"]["source_status"] == "captured", "summary omitted capture provenance")
                require(summary["complete"] is True, "single aggregate should fit the summary budget")
                require(b"DO_NOT_SUMMARIZE" not in raw and b"offline-body:" not in raw, "summary leaked raw capture content")
                delta, delta_raw = cli(workspace, task, "context", "--since", summary["cursor"], "--max-bytes", "2048")
                require(delta["no_change"] is True and delta["groups"] == [], "same capture did not produce an empty delta")
                require(delta["cursor"] == summary["cursor"] and len(delta_raw) <= 2048, "stable cursor or delta budget failed")
                summaries[site], summary_bytes[site] = summary, len(raw)

            empty, empty_raw = cli("applications", "empty", "summary", "--max-bytes", "2048")
            require(empty["provenance"]["source_status"] == "no_capture" and empty["totals"]["requests"] == 0,
                    "empty task inherited another task's capture")
            require(len(empty_raw) <= 2048 and empty["groups"] == [], "empty context is not bounded")
            foreign, _ = cli("applications", "form", "context", "--since", summaries["ebay"]["cursor"], ok=False)
            require(bool(foreign.get("error")), "foreign cursor did not return an explicit error")
            foreign_body, _ = cli("applications", "empty", "body", "req-form-first-0000", ok=False)
            require(bool(foreign_body.get("error")), "empty task retrieved another task's response body")

            # Primary settings must stay local even after both captures exist.
            cli("shopping", "ebay", "primary", "ebay.example.test")
            form_primary, _ = cli("applications", "form", "primary")
            require(form_primary == [], "primary domains leaked across tasks")

            replacement = browse("shopping", "ebay", "https://ebay.example.test/second")
            require(replacement["captured_requests_total"] == 7, "replacement capture failed")
            latest, _ = cli("shopping", "ebay", "summary", "--max-bytes", "2048")
            require(latest["totals"]["requests"] == 7, "latest scoped capture did not replace its prior live view")
            archived, _ = cli("shopping", "ebay", "summary", "--saved", captures["ebay"]["saved_hash_id"], "--max-bytes", "2048")
            require(archived["totals"]["requests"] == 1000 and archived["provenance"]["source_status"] == "saved",
                    "earlier archive was lost or confused with latest capture")
            body, _ = cli("shopping", "ebay", "body", "req-ebay-first-0999")
            require(body.get("id") == "req-ebay-first-0999" and body.get("body") == "offline-body:ebay:first:0999",
                    "earlier archived response body was not retrieved by its exact request ID")
            peer.check()
            require(peer.capture_count == 3, "unexpected number of synthetic captures")
            print(json.dumps({
                "passed": True,
                "synthetic_captures": peer.capture_count,
                "separate_request_counts": [1000, 200],
                "capture_preview_counts": [len(captures["ebay"]["captured_requests"]), len(captures["form"]["captured_requests"])],
                "summary_bytes": [summary_bytes["ebay"], summary_bytes["form"]],
                "summary_budget": 2048,
                "empty_task_requests": empty["totals"]["requests"],
                "unchanged_deltas": 2,
                "foreign_cursor_rejected": True,
                "foreign_body_rejected": True,
                "primary_isolation": True,
                "earlier_archive_body_retrieved": True,
            }, separators=(",", ":")))
        finally:
            peer.close()


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(f"Offline agent-context verification failed: {error}", file=sys.stderr)
        sys.exit(1)
