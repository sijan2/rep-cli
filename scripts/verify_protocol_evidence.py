#!/usr/bin/env python3
"""Verify browser protocol evidence and form outcomes against loopback fixtures.

REP_BINARY=/tmp/rep/rep REP_HOST_BINARY=/tmp/rep/rep-host \
  python3 scripts/verify_protocol_evidence.py --output /tmp/protocol-report.json

Uses a unique task, temporary data, a frozen extension copy, and an owned private
Chromium profile. It makes no model calls and visits no external application.
The fixture is an application-message oracle, not a packet-capture benchmark.
"""
from __future__ import annotations

import argparse
import base64
import hashlib
import http.server
import json
import os
from pathlib import Path
import shutil
import struct
import subprocess
import tempfile
import threading
import time
import urllib.parse
import urllib.request

from verify_decision_runtime import ROOT, Runner, require


CLIENT_TEXT = "client café 雪"
SERVER_TEXT = "server café 雪"
CLIENT_BINARY = bytes([0, 255, 1, 128, 42])
SERVER_BINARY = bytes([254, 0, 127, 128, 2])
STRESS_MESSAGES = 2048
STRESS_MESSAGE_BYTES = 1024


def stress_payload(index):
    prefix = f"{index:08d}|".encode()
    return prefix + b"x" * (STRESS_MESSAGE_BYTES - len(prefix))


STRESS_PAGE = b"""<!doctype html><meta charset="utf-8"><title>WebSocket pressure fixture</title>
<h1>WebSocket pressure fixture</h1><p id="status">Connecting</p><script>
window.stressReadback = {messages:0,bytes:0,ordered:true,closed:false};
const socket = new WebSocket('ws://' + location.host + '/socket?mode=stress');
window.fixtureSocket = socket;
socket.onmessage = event => {
  const state = window.stressReadback;
  state.ordered = state.ordered && typeof event.data === 'string' && Number(event.data.slice(0,8)) === state.messages;
  state.messages += 1;
  state.bytes += new TextEncoder().encode(event.data).byteLength;
};
socket.onclose = () => { window.stressReadback.closed = true; document.querySelector('#status').textContent = 'Closed'; };
</script>"""


def protocol_page(mode):
    return ("""<!doctype html><meta charset="utf-8"><title>Protocol fixture</title>
<h1>Protocol fixture</h1><p id="status">Connecting</p><script>
const socket = new WebSocket('ws://' + location.host + '/socket?mode=__MODE__');
window.fixtureSocket = socket;
window.fixtureMessages = [];
socket.binaryType = 'arraybuffer';
socket.onopen = () => {
  socket.send(__TEXT__);
  socket.send(new Uint8Array(__BINARY__));
};
socket.onmessage = event => {
  fixtureMessages.push(typeof event.data === 'string' ? event.data : [...new Uint8Array(event.data)]);
  document.querySelector('#status').textContent = 'Received ' + fixtureMessages.length;
};
socket.onclose = () => { document.querySelector('#status').textContent = 'Closed'; };
</script>""".replace("__MODE__", mode).replace("__TEXT__", json.dumps(CLIENT_TEXT))
            .replace("__BINARY__", json.dumps(list(CLIENT_BINARY)))).encode()


FORM_PAGE = b"""<!doctype html><meta charset="utf-8"><title>Form fixture</title>
<h1>Form fixture</h1><form id="application">
<label for="name">Name</label><input id="name" required>
<label for="mode">Validation</label><select id="mode"><option value="accept">Accept</option><option value="reject">Reject</option></select>
<button id="submit" type="submit">Submit fixture</button></form>
<p id="status" role="status">Ready</p><p id="receipt"></p>
<script>
document.querySelector('#application').addEventListener('submit', async event => {
  event.preventDefault();
  document.querySelector('#status').textContent = 'Validating';
  const response = await fetch('/submit', {method:'POST', headers:{'Content-Type':'application/json'},
    body:JSON.stringify({name:document.querySelector('#name').value, mode:document.querySelector('#mode').value})});
  const result = await response.json();
  document.querySelector('#receipt').textContent = result.receipt || '';
  document.querySelector('#status').textContent = response.ok ? 'Submitted ' + result.receipt : 'Rejected: fixture validation';
});
</script>"""


class FixtureState:
    def __init__(self):
        self.lock = threading.Lock()
        self.stop = threading.Event()
        self.attempts = []
        self.receipts = []
        self.sockets = []
        self.errors = []

    def snapshot(self):
        with self.lock:
            return {"attempts": list(self.attempts), "receipts": list(self.receipts),
                    "sockets": list(self.sockets), "errors": list(self.errors)}


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_):
        pass

    def reply(self, status, payload, content_type="application/json"):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(payload)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        try:
            self.wfile.write(payload)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        if parsed.path == "/socket":
            self.websocket(urllib.parse.parse_qs(parsed.query).get("mode", ["closed"])[0])
        elif parsed.path == "/protocol":
            mode = urllib.parse.parse_qs(parsed.query).get("mode", ["closed"])[0]
            require(mode in ("closed", "open"), "unsupported fixture mode")
            self.reply(200, protocol_page(mode), "text/html; charset=utf-8")
        elif parsed.path == "/form":
            self.reply(200, FORM_PAGE, "text/html; charset=utf-8")
        elif parsed.path == "/stress":
            self.reply(200, STRESS_PAGE, "text/html; charset=utf-8")
        elif parsed.path == "/state":
            self.reply(200, json.dumps(self.server.fixture.snapshot()).encode())
        else:
            self.reply(404, b'{"error":"fixture route not found"}')

    def do_POST(self):
        if self.path != "/submit":
            self.reply(404, b'{}')
            return
        length = int(self.headers.get("Content-Length", "0"))
        require(0 < length <= 65536, "fixture submission exceeds its bound")
        value = json.loads(self.rfile.read(length))
        # A real asynchronous application response, independent of input readback.
        time.sleep(.05)
        with self.server.fixture.lock:
            self.server.fixture.attempts.append({"name": value.get("name"), "mode": value.get("mode")})
            if value.get("mode") == "reject" or not value.get("name"):
                status, response = 422, {"error": "fixture validation"}
            else:
                receipt = "receipt-" + str(len(self.server.fixture.receipts) + 1)
                self.server.fixture.receipts.append(receipt)
                status, response = 200, {"receipt": receipt}
        self.reply(status, json.dumps(response).encode())

    def read_frame(self):
        def read_exact(length):
            value = self.rfile.read(length)
            require(len(value) == length, "WebSocket fixture received an incomplete frame")
            return value
        first, second = read_exact(2)
        require(first & 128 and not first & 112 and second & 128, "fixture requires one masked, uncompressed client message")
        length = second & 127
        if length == 126:
            length = struct.unpack("!H", read_exact(2))[0]
        elif length == 127:
            length = struct.unpack("!Q", read_exact(8))[0]
        require(length <= 4096, "WebSocket fixture payload exceeds its bound")
        mask, payload = read_exact(4), read_exact(length)
        return first & 15, bytes(byte ^ mask[index % 4] for index, byte in enumerate(payload))

    def send_frame(self, opcode, payload):
        require(len(payload) <= 4096, "fixture server payload exceeds its bound")
        header = bytes([128 | opcode, len(payload)]) if len(payload) < 126 else bytes([128 | opcode, 126]) + struct.pack("!H", len(payload))
        self.wfile.write(header + payload)
        self.wfile.flush()

    def websocket(self, mode):
        self.close_connection = True
        try:
            require(mode in ("closed", "open", "stress"), "unsupported WebSocket fixture mode")
            require(self.headers.get("Upgrade", "").lower() == "websocket", "missing WebSocket upgrade")
            key = self.headers.get("Sec-WebSocket-Key", "")
            require(len(base64.b64decode(key, validate=True)) == 16, "invalid fixture WebSocket key")
            accepted = base64.b64encode(hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
            self.send_response(101)
            self.send_header("Upgrade", "websocket")
            self.send_header("Connection", "Upgrade")
            self.send_header("Sec-WebSocket-Accept", accepted)
            self.end_headers()
            self.wfile.flush()
            self.connection.settimeout(5)
            if mode == "stress":
                digest = hashlib.sha256()
                started = time.perf_counter()
                for index in range(STRESS_MESSAGES):
                    payload = stress_payload(index)
                    self.send_frame(1, payload)
                    digest.update(payload)
                send_seconds = time.perf_counter() - started
                self.send_frame(8, struct.pack("!H", 1000) + b"stress complete")
                opcode, _ = self.read_frame()
                require(opcode == 8, "stress client did not acknowledge a clean close")
                with self.server.fixture.lock:
                    self.server.fixture.sockets.append({"mode": mode, "client_messages": 0, "server_messages": STRESS_MESSAGES,
                                                        "server_payload_bytes": STRESS_MESSAGES * STRESS_MESSAGE_BYTES,
                                                        "payload_sha256": digest.hexdigest(), "send_loop_seconds": send_seconds,
                                                        "close_acknowledged": True})
                return
            received = [self.read_frame(), self.read_frame()]
            require(received == [(1, CLIENT_TEXT.encode()), (2, CLIENT_BINARY)], "server observed unexpected client messages")
            self.send_frame(1, SERVER_TEXT.encode())
            self.send_frame(2, SERVER_BINARY)
            with self.server.fixture.lock:
                self.server.fixture.sockets.append({"mode": mode, "client_messages": 2, "server_messages": 2})
            if mode == "closed":
                self.send_frame(8, struct.pack("!H", 1000) + b"fixture complete")
                opcode, _ = self.read_frame()
                require(opcode == 8, "client did not acknowledge a clean close")
            else:
                self.server.fixture.stop.wait(30)
        except (BrokenPipeError, ConnectionResetError):
            if not self.server.fixture.stop.is_set():
                with self.server.fixture.lock:
                    self.server.fixture.errors.append("WebSocket fixture disconnected unexpectedly")
        except Exception as error:
            with self.server.fixture.lock:
                self.server.fixture.errors.append(str(error))


def operation_pair(runner, run_id, result):
    reference = result.get("evidence") or {}
    require(reference.get("run_id") == run_id and reference.get("operation_id"), "CLI result omitted run evidence")
    page, _, _ = runner.cli("evidence", "operations", run_id, "--limit", "100")
    require(not page.get("has_more"), "fixture operation journal unexpectedly needs another page")
    started = [item for item in page["operations"] if item["id"] == reference["operation_id"]]
    finished = [item for item in page["operations"] if item.get("parent_id") == reference["operation_id"]]
    require(len(started) == 1 and len(finished) == 1, "operation must retain one start and one linked completion")
    require(started[0]["status"] == "pending" and started[0]["phase"] == "started", "start is not pending evidence")
    require(finished[0]["phase"] == "finished" and finished[0]["sequence"] > started[0]["sequence"], "completion ordering is invalid")
    return started[0], finished[0]


def verify_capture_link(runner, run_id, capture):
    require(capture.get("capture_snapshot_verified") and capture.get("saved_hash_id") and capture.get("capture_sha256"), "capture is not pinned to verified archive bytes")
    _, finished = operation_pair(runner, run_id, capture)
    references = [item for item in finished.get("references", []) if item["kind"] == "capture_archive"]
    require(len(references) == 1 and references[0]["id"] == capture["saved_hash_id"]
            and references[0]["sha256"] == capture["capture_sha256"], "operation references the wrong capture")
    require(finished["status"] == "completed" and finished["verification"] == "unverified", "capture bytes incorrectly imply a verified task goal")


def save_exact_stream_payload(runner, directory, request_id, archive, sequence, expected, complete):
    args = ["stream", request_id, "--saved", archive, "--event", str(sequence), "--save", "--max-bytes", "2048"]
    if complete:
        args.append("--require-complete")
    artifact, _, _ = runner.cli(*args)
    path = Path(artifact["path"])
    require(path.is_relative_to(directory) and path.read_bytes() == expected, "saved stream payload differs from fixture bytes")
    require(artifact["artifact_sha256"] == hashlib.sha256(expected).hexdigest() and path.stat().st_mode & 0o777 == 0o600, "stream artifact identity or privacy mismatch")
    return {"sequence": sequence, "bytes": len(expected), "sha256": artifact["artifact_sha256"]}


def protocol_suite(runner, origin, run_id, live_path):
    cases = []
    closed_record = None
    for mode in ("closed", "open"):
        capture, elapsed, _ = runner.cli("browser", "open", origin + "/protocol?mode=" + mode, "--browser", "headless", "--keep-tab", "--idle", "250ms", "--timeout", "5s")
        verify_capture_link(runner, run_id, capture)
        live = json.loads(live_path.read_text())
        require(live.get("session_id") == capture["session_id"], "task live data belongs to another capture")
        websocket = [item for item in live["requests"] if item.get("record_kind") == "websocket"]
        document = [item for item in live["requests"] if item.get("url") == origin + "/protocol?mode=" + mode]
        require(len(websocket) == 1 and len(document) == 1, "fixture did not retain one document and one WebSocket connection")
        archive, request = capture["saved_hash_id"], websocket[0]
        page, _, _ = runner.cli("stream", request["id"], "--saved", archive, "--events", "100", "--max-bytes", "16384")
        sequences = [event["sequence"] for event in page["events"]]
        require(page["page_complete"] and sequences == list(range(1, len(sequences) + 1)), "stream event ordering or page completeness mismatch")
        expected_state = "complete" if mode == "closed" else "partial"
        require(page["capture"]["state"] == expected_state, "closed/open connection coverage was misreported")
        if mode == "closed":
            require(any(event["kind"] == "closed" for event in page["events"]), "clean close lifecycle event is missing")
            runner.cli("stream", request["id"], "--saved", archive, "--info", "--require-complete")
            closed_record = (request["id"], archive)
        else:
            require(page["capture"].get("reason") == "capture_ended" and not live.get("browser_session", {}).get("timed_out")
                    and any(event["kind"] == "capture_end" for event in page["events"]), "open stream does not distinguish normal capture end from a deadline")
            _, _, code = runner.cli("stream", request["id"], "--saved", archive, "--info", "--require-complete", success=False)
            require(code != 0, "open stream passed require-complete")
        messages = [event for event in page["events"] if event["kind"] == "message" and event.get("opcode") in (1, 2)]
        expected = [("sent", 1, CLIENT_TEXT.encode()), ("sent", 2, CLIENT_BINARY), ("received", 1, SERVER_TEXT.encode()), ("received", 2, SERVER_BINARY)]
        require([(event.get("direction"), event.get("opcode")) for event in messages] == [(direction, opcode) for direction, opcode, _ in expected], "message direction/order/opcode mismatch")
        payloads = [save_exact_stream_payload(runner, runner.directory, request["id"], archive, event["sequence"], payload, mode == "closed")
                    for event, (_, _, payload) in zip(messages, expected)]
        body, _, _ = runner.cli("body", document[0]["id"], "--saved", archive, "--info", "--require-complete", "--max-bytes", "2048")
        require(body["body_capture"]["sha256"] == hashlib.sha256(protocol_page(mode)).hexdigest(), "saved document body changed")
        require(document[0].get("completion_monotonic_timestamp", 0) > 0 and document[0].get("response", {}).get("protocol"), "HTTP protocol/completion clock did not survive archiving")
        cases.append({"mode": mode, "capture_seconds": elapsed, "archive": archive, "capture_sha256": capture["capture_sha256"],
                      "coverage": page["capture"], "event_count": len(sequences), "payloads": payloads,
                      "http_protocol": document[0]["response"]["protocol"], "http_body_sha256": body["body_capture"]["sha256"]})
    retained, _, _ = runner.cli("stream", closed_record[0], "--saved", closed_record[1], "--info", "--require-complete")
    require(retained["capture"]["state"] == "complete", "later capture replaced the earlier closed stream archive")
    return cases


class OwnedHostRSS:
    """Sample only the known host PID and verify its browser parent each time."""
    def __init__(self, runner):
        self.pid = runner.registry()["pid"]
        self.parent = runner.state["pid"]
        self.samples = []
        self.unavailable = 0
        self.done = threading.Event()
        self.thread = None

    def sample(self):
        try:
            result = subprocess.run(["ps", "-p", str(self.pid), "-o", "ppid=,rss="], capture_output=True, timeout=1)
            values = result.stdout.split()
            if result.returncode == 0 and len(values) == 2 and int(values[0]) == self.parent:
                self.samples.append(int(values[1]) * 1024)
                return
        except (OSError, ValueError, subprocess.TimeoutExpired):
            pass
        self.unavailable += 1

    def start(self):
        self.sample()
        def poll():
            while not self.done.wait(.05):
                self.sample()
        self.thread = threading.Thread(target=poll, daemon=True)
        self.thread.start()

    def finish(self):
        self.done.set()
        self.thread.join(timeout=2)
        self.sample()
        return {"process": "owned native host only", "pid": self.pid, "samples": len(self.samples),
                "unavailable_samples": self.unavailable, "sample_interval_seconds": .05,
                "baseline_bytes": self.samples[0] if self.samples else None,
                "peak_sampled_bytes": max(self.samples) if self.samples else None,
                "delta_from_baseline_bytes": max(self.samples) - self.samples[0] if self.samples else None,
                "limits": "RSS from ps in 50ms samples. This can miss peaks; it excludes Chromium, renderers, the CLI, and the fixture server. Sampling overhead is included in capture time."}


def stress_suite(runner, origin, run_id, data_dir, state):
    archive_path = data_dir / "sessions.jsonl"
    archive_bytes_before = archive_path.stat().st_size if archive_path.exists() else 0
    memory = OwnedHostRSS(runner)
    memory.start()
    try:
        capture, elapsed, _ = runner.cli("browser", "open", origin + "/stress", "--browser", "headless", "--keep-tab", "--idle", "500ms", "--timeout", "15s")
    finally:
        memory_report = memory.finish()
    verify_capture_link(runner, run_id, capture)
    verify_started = time.perf_counter()
    # The entire fixture archive is small and privately owned. Read the selected
    # immutable session directly for an independent all-payload digest; exercise
    # the public bounded stream reader separately below.
    sessions = []
    with archive_path.open("rb") as source:
        for raw in source:
            entry = json.loads(raw)
            session = entry.get("session") or {}
            if entry.get("action") == "session" and session.get("hash_id") == capture["saved_hash_id"]:
                sessions.append(session)
    require(len(sessions) == 1, "stress archive hash did not identify one immutable session")
    session = sessions[0]
    require(session.get("capture_session_id") == capture["session_id"] and session.get("capture_digest") == capture["capture_sha256"], "stress archive provenance differs from the completed capture")
    requests = [request for request in session["requests"] if request.get("record_kind") == "websocket"]
    require(len(requests) == 1, "stress capture did not contain one WebSocket connection")
    request = requests[0]
    stream = request["stream"]
    expected_bytes = STRESS_MESSAGES * STRESS_MESSAGE_BYTES
    expected_events = STRESS_MESSAGES + 4  # created, two handshakes, messages, close
    events = stream["events"]
    require(stream["state"] == "closed" and stream["capture"]["state"] == "complete", "closed stress connection was not complete")
    require([event["sequence"] for event in events] == list(range(1, expected_events + 1)), "stress capture lost or reordered lifecycle/message events")
    require([event["kind"] for event in events[:3]] == ["created", "handshake_request", "handshake_response"] and events[-1]["kind"] == "closed", "stress connection lifecycle differs from the fixture")
    messages = [event for event in events if event["kind"] == "message"]
    require(len(messages) == STRESS_MESSAGES, "stress capture message count differs from the sender")
    expected_digest, captured_digest = hashlib.sha256(), hashlib.sha256()
    for index, event in enumerate(messages):
        expected = stress_payload(index)
        payload = event.get("payload", "").encode()
        require(event.get("direction") == "received" and event.get("opcode") == 1
                and event.get("payload_encoding") in ("utf-8", "utf8", "text")
                and event.get("bytes") == STRESS_MESSAGE_BYTES and payload == expected,
                f"stress message {index} changed direction, encoding, ordering, or bytes")
        expected_digest.update(expected)
        captured_digest.update(payload)
    require(captured_digest.digest() == expected_digest.digest(), "stress cumulative payload digest mismatch")
    coverage = stream["capture"]
    require(coverage["captured_events"] == expected_events and coverage["observed_events"] == expected_events
            and coverage["captured_bytes"] == expected_bytes and coverage["observed_bytes"] == expected_bytes
            and coverage["dropped_events"] == 0 and not any(event.get("truncated") or event.get("error") for event in events),
            "stress capture reported incomplete/lost data")
    serialized_bytes = len(json.dumps(request, ensure_ascii=False, separators=(",", ":")).encode())
    require(serialized_bytes < 8 * 1024 * 1024, "stress record exceeds half the default native backlog budget")
    browser, _ = runner.evaluate(capture["tab_id"], "window.stressReadback")
    require(browser == {"messages": STRESS_MESSAGES, "bytes": expected_bytes, "ordered": True, "closed": True}, "independent renderer did not receive the expected stress messages")
    observations = [item for item in state.snapshot()["sockets"] if item["mode"] == "stress"]
    require(len(observations) == 1 and observations[0]["server_messages"] == STRESS_MESSAGES
            and observations[0]["server_payload_bytes"] == expected_bytes and observations[0]["payload_sha256"] == expected_digest.hexdigest()
            and observations[0]["close_acknowledged"], "independent sender did not establish the expected stress bytes/close")
    metadata, after, pages, page_seconds = [], -1, 0, 0
    while True:
        page, duration, _ = runner.cli("stream", request["id"], "--saved", capture["saved_hash_id"], "--after", str(after),
                                      "--events", "1000", "--max-bytes", "65536", "--require-complete")
        pages += 1
        page_seconds += duration
        require(page["next_after"] > after and pages <= 16, "stress metadata pagination stopped making progress")
        metadata.extend(page["events"])
        after = page["next_after"]
        if page["page_complete"]:
            break
    require([item["sequence"] for item in metadata] == list(range(1, expected_events + 1))
            and sum(item["bytes"] for item in metadata) == expected_bytes, "bounded CLI pages lost or repeated stress event metadata")
    retrieved = [save_exact_stream_payload(runner, runner.directory, request["id"], capture["saved_hash_id"],
                                          messages[index]["sequence"], stress_payload(index), True)
                 for index in (0, STRESS_MESSAGES // 2, STRESS_MESSAGES - 1)]
    return {"messages": STRESS_MESSAGES, "message_bytes": STRESS_MESSAGE_BYTES, "direction": "received", "protocol": "WebSocket text messages over loopback HTTP/1.1",
            "payload_bytes": expected_bytes, "payload_sha256": captured_digest.hexdigest(), "coverage": coverage,
            "capture_seconds": elapsed, "server_send_loop_seconds": observations[0]["send_loop_seconds"],
            "verification_seconds": time.perf_counter() - verify_started, "metadata_pages": pages, "metadata_page_seconds": page_seconds,
            "sampled_exact_payloads": retrieved, "serialized_record_bytes": serialized_bytes,
            "archive_growth_bytes": archive_path.stat().st_size - archive_bytes_before,
            "archive": capture["saved_hash_id"], "capture_sha256": capture["capture_sha256"], "owned_host_rss": memory_report,
            "limits": "One 2MiB receive burst. Capture time includes navigation, 500ms idle, collection, transport, sealing, archive/evidence writes, and host RSS sampling. All archived payloads were checked; three payloads were also retrieved through CLI. This is not sustained throughput, wire-rate, aggregate browser memory, or observer-overhead measurement."}


def form_suite(runner, origin, run_id, state):
    url = origin + "/form"
    opened, _, _ = runner.cli("browser", "open", url, "--browser", "headless", "--keep-tab", "--idle", "100ms")
    verify_capture_link(runner, run_id, opened)
    tab = opened["tab_id"]
    filled, fill_time, _ = runner.plan(tab, url, [{"id": "name", "action": "fill", "target": {"selector": "#name"}, "value": "Fixture Exact Name"}])
    require(filled["status"] == "verified", "exact fill was not read back")
    _, fill_evidence = operation_pair(runner, run_id, filled)
    require(fill_evidence["verification"] == "satisfied" and not state.snapshot()["attempts"], "fill implied a server submission")
    click = {"id": "submit", "action": "click", "target": {"selector": "#submit"},
             "after": [{"target": {"selector": "#status"}, "text": "Submitted receipt-1"}], "timeout_ms": 3000}
    submitted, submit_time, _ = runner.plan(tab, url, [click])
    require(submitted["status"] == "verified", "fresh submission receipt was not verified")
    snapshot = state.snapshot()
    require(snapshot["receipts"] == ["receipt-1"] and snapshot["attempts"] == [{"name": "Fixture Exact Name", "mode": "accept"}], "independent server receipt/counter disagrees with success")
    _, accepted_evidence = operation_pair(runner, run_id, submitted)
    require(accepted_evidence["verification"] == "satisfied", "successful declared check was not recorded")
    stale, stale_time, stale_code = runner.plan(tab, url, [click], success=False)
    require(stale_code != 0 and stale and stale["steps"][0].get("attempted") is False and len(state.snapshot()["attempts"]) == 1, "preexisting receipt allowed duplicate submission")
    _, stale_evidence = operation_pair(runner, run_id, stale)
    require(stale_evidence["verification"] != "satisfied", "stale receipt recorded as new verified work")
    reject_steps = [
        {"id": "rejected-name", "action": "fill", "target": {"selector": "#name"}, "value": "Rejected Exact Name", "replace": True},
        {"id": "validation", "action": "choose", "target": {"selector": "#mode"}, "values": ["Reject"]},
        {"id": "rejected-submit", "action": "click", "target": {"selector": "#submit"},
         "after": [{"target": {"selector": "#status"}, "text": "Submitted receipt-2"}], "timeout_ms": 500},
    ]
    rejected, rejected_time, code = runner.plan(tab, url, reject_steps, success=False)
    require(code != 0 and rejected and rejected["status"] != "verified", "server rejection was reported as confirmed submission")
    readback, _ = runner.evaluate(tab, "({name:document.querySelector('#name').value,status:document.querySelector('#status').textContent,receipt:document.querySelector('#receipt').textContent})")
    snapshot = state.snapshot()
    require(readback == {"name": "Rejected Exact Name", "status": "Rejected: fixture validation", "receipt": ""}
            and snapshot["receipts"] == ["receipt-1"] and len(snapshot["attempts"]) == 2
            and rejected["steps"][-1].get("id") == "rejected-submit" and rejected["steps"][-1].get("attempted") is True,
            "rejected submit was not independently established: " + json.dumps({"readback": readback, "server": snapshot, "report": rejected}, sort_keys=True))
    _, rejected_evidence = operation_pair(runner, run_id, rejected)
    require(rejected_evidence["verification"] != "satisfied", "rejection became satisfied operation evidence")
    return {"exact_fill_seconds": fill_time, "submit_seconds": submit_time, "stale_receipt_seconds": stale_time,
            "rejection_seconds": rejected_time, "accepted_receipts": snapshot["receipts"], "attempts": len(snapshot["attempts"]),
            "accepted_status": submitted["status"], "rejected_status": rejected["status"], "stale_receipt_blocked": True,
            "verification_sources": ["declared DOM checks", "independent loopback server counter and receipt"],
            "limits": "No upload adapter, authenticated pagination, dropped-action-ACK injection, or third-party application is exercised."}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--stress", action="store_true", help="Also capture 2,048 received WebSocket messages of 1,024 bytes and sample only owned host RSS")
    options = parser.parse_args()
    output = options.output.resolve()
    require(not output.exists(), "output already exists; choose a new report path")
    binary, host = (shutil.which(os.environ.get(key, fallback)) for key, fallback in (("REP_BINARY", "rep"), ("REP_HOST_BINARY", "rep-host")))
    require(binary and host, "REP_BINARY and REP_HOST_BINARY must name executables")
    extension = Path(os.environ.get("REP_EXTENSION_PATH", ROOT.parent / "rep")).resolve()
    fixture = FixtureState()
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.fixture = fixture
    server.daemon_threads = True
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    result = {"schema": 1, "passed": False, "fixtures": "loopback HTTP/WebSocket and submission form only", "with_model": False,
              "harness_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
              "measurements": "Single fixture run; CLI completion timings do not establish sustained throughput, peak memory, or broad task accuracy."}
    try:
        with tempfile.TemporaryDirectory(prefix="rep-protocol-evidence-", dir="/tmp") as temporary:
            directory = Path(temporary)
            frozen = directory / "extension"
            shutil.copytree(extension, frozen, ignore=shutil.ignore_patterns(".git", "node_modules", ".env", ".env.*", "coverage", "*.zip", "*.crx"))
            runner = Runner(Path(binary).resolve(), Path(host).resolve(), frozen, directory, "protocol-evidence")
            runner.base = [runner.binary, "--workspace", "protocol-evidence", "--task", runner.task]
            runner.owner = "protocol-evidence/" + runner.task
            result.update(task=runner.task, cli_sha256=hashlib.sha256(Path(binary).read_bytes()).hexdigest(), host_sha256=hashlib.sha256(Path(host).read_bytes()).hexdigest())
            digest = hashlib.sha256()
            for path in sorted([*frozen.rglob("*.js"), frozen / "manifest.json"]):
                digest.update(str(path.relative_to(frozen)).encode() + b"\0" + path.read_bytes())
            result["extension_js_sha256"] = digest.hexdigest()
            try:
                result["browser_start_seconds"] = runner.start()
                scope, _, _ = runner.cli("scope")
                live_path = Path(scope["live_path"])
                require(scope["task"] == runner.task and live_path.is_relative_to(directory), "scope is not the fixture's private task")
                stop = "Stop after closed/open stream checks and one accepted plus one rejected fixture submission"
                if options.stress:
                    stop += ", including one bounded 2MiB WebSocket receive burst"
                run, _, _ = runner.cli("evidence", "begin", "--intent", "Verify exact protocol bytes and independently confirmed local form outcomes", "--stop", stop)
                runner.base.extend(["--run", run["id"]])
                result["run_id"] = run["id"]
                with urllib.request.urlopen(f"http://127.0.0.1:{runner.state['port']}/json/version", timeout=5) as response:
                    version = json.load(response)
                result["chromium"] = {key: version.get(key) for key in ["Browser", "Protocol-Version", "V8-Version"]}
                origin = f"http://127.0.0.1:{server.server_port}"
                result["protocol"] = protocol_suite(runner, origin, run["id"], live_path)
                if options.stress:
                    result["stress"] = stress_suite(runner, origin, run["id"], Path(scope["data_dir"]), fixture)
                result["forms"] = form_suite(runner, origin, run["id"], fixture)
                server_state = fixture.snapshot()
                expected_modes = ["closed", "open", "stress"] if options.stress else ["closed", "open"]
                require(not server_state["errors"] and [entry["mode"] for entry in server_state["sockets"]] == expected_modes, "independent WebSocket server observed errors or missing messages")
                result["server_socket_observations"] = server_state["sockets"]
                result["passed"] = True
            finally:
                runner.stop()
                result["owned_browser_stopped"] = True
    except Exception as error:
        result["passed"] = False
        result["error"] = str(error)
    finally:
        fixture.stop.set()
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(result, indent=2) + "\n")
        print(json.dumps({"passed": result["passed"], "report": str(output), "error": result.get("error")}))
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
