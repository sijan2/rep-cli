#!/usr/bin/env python3
"""Verify opt-in WebRTC/WebTransport API observations in owned local Chromium.

REP_BINARY=/tmp/rep-transports/rep REP_HOST_BINARY=/tmp/rep-transports/rep-host \
  python3 scripts/verify_transport_capture.py --webtransport \
    --quic-python /path/to/python-with-aioquic --output /tmp/transports.json

WebRTC uses two local peer connections without media or external ICE servers.
Optional HTTP/3 echo uses aioquic in the selected interpreter and a temporary
ECDSA certificate pinned by the page. No global dependencies or trust changes.
"""
from __future__ import annotations

import argparse
import asyncio
import base64
import datetime
import hashlib
import http.server
import ipaddress
import json
import os
from pathlib import Path
import select
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
import urllib.request
import uuid

from verify_decision_runtime import ROOT, Runner, require
from verify_protocol_evidence import OwnedHostRSS, verify_capture_link

FIXTURE = Path(__file__).parent / "fixtures" / "transports" / "index.html"
RTC_LEFT = ["rtc text café 雪".encode(), bytes([0, 255, 1, 128, 42]), bytes([10, 11, 12, 13]), bytes([254, 253, 0, 127])]
RTC_RIGHT = [b"rtc receipt", bytes([3, 0, 255, 99])]
DATAGRAM = bytes([0, 255, 1, 128, 77])
UNI = "uni café 雪".encode()
BIDI = bytes([0, 13, 10, 255, 42, 7, 8, 9])
LARGE = bytes(index % 251 for index in range(32768))
SERVER_BIDI = "server bidi café 雪".encode()
SERVER_REPLY = b"server bidi receipt"
SERVER_ACK = b"server bidi accepted"
STRESS_MESSAGES, STRESS_MESSAGE_BYTES = 1024, 1024
COMPLETE_MODES = {"closed", "incoming", "stress"}


def stress_payload(index):
    return index.to_bytes(4, "big") + bytes([index % 251]) * (STRESS_MESSAGE_BYTES - 4)


def byte_identity(value):
    return {"bytes": len(value), "sha256": hashlib.sha256(value).hexdigest()}


def serve_quic(directory):
    # API usage follows the primary aioquic HTTP/3 example and Chrome sample:
    # https://github.com/aiortc/aioquic/blob/main/examples/http3_server.py
    # https://github.com/GoogleChrome/samples/blob/gh-pages/webtransport/webtransport_server.py
    import aioquic
    from aioquic.asyncio import QuicConnectionProtocol, serve
    from aioquic.h3.connection import H3_ALPN, H3Connection
    from aioquic.h3.events import DatagramReceived, HeadersReceived, WebTransportStreamDataReceived
    from aioquic.quic.configuration import QuicConfiguration
    from aioquic.quic.connection import stream_is_unidirectional
    from aioquic.quic.events import ProtocolNegotiated, StreamDataReceived
    from cryptography import x509
    from cryptography.hazmat.primitives import hashes, serialization
    from cryptography.hazmat.primitives.asymmetric import ec
    from cryptography.x509.oid import NameOID

    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    key = ec.generate_private_key(ec.SECP256R1())
    now = datetime.datetime.now(datetime.timezone.utc)
    name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "Rep loopback fixture")])
    certificate = (x509.CertificateBuilder().subject_name(name).issuer_name(name).public_key(key.public_key())
                   .serial_number(x509.random_serial_number()).not_valid_before(now - datetime.timedelta(minutes=1))
                   .not_valid_after(now + datetime.timedelta(days=1))
                   .add_extension(x509.SubjectAlternativeName([x509.IPAddress(ipaddress.ip_address("127.0.0.1")), x509.DNSName("localhost")]), critical=False)
                   .sign(key, hashes.SHA256()))
    cert_path, key_path = directory / "certificate.pem", directory / "key.pem"
    cert_path.write_bytes(certificate.public_bytes(serialization.Encoding.PEM))
    key_path.write_bytes(key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()))
    cert_path.chmod(0o600)
    key_path.chmod(0o600)
    stats = {"aioquic": aioquic.__version__, "connections": 0, "observations": [], "errors": []}

    def persist():
        temporary = directory / "stats.tmp"
        temporary.write_text(json.dumps(stats))
        temporary.chmod(0o600)
        temporary.replace(directory / "stats.json")

    class EchoProtocol(QuicConnectionProtocol):
        def __init__(self, *args, **kwargs):
            super().__init__(*args, **kwargs)
            self.http = None
            self.buffers = {}
            self.case = ""
            self.mode = ""
            self.initiated = {}
            stats["connections"] += 1
            self.identity = stats["connections"]

        def quic_event_received(self, event):
            if isinstance(event, ProtocolNegotiated):
                self.http = H3Connection(self._quic, enable_webtransport=True)
            if self.http is None:
                return
            try:
                # aioquic 1.2 creates a server bidi header but does not retain its
                # reverse-half H3 association. Its reply is raw application data;
                # consume only the stream IDs created by this fixture endpoint.
                if isinstance(event, StreamDataReceived) and event.stream_id in self.initiated:
                    buffer = self.buffers.setdefault(event.stream_id, bytearray())
                    buffer.extend(event.data)
                    require(len(buffer) <= 1024, "server bidi receipt exceeds its bound")
                    if event.end_stream:
                        payload = bytes(self.buffers.pop(event.stream_id))
                        require(payload == SERVER_REPLY, "server-initiated bidi receipt differs")
                        stats["observations"].append({"case": self.case, "connection": self.identity, "kind": "server_bidi_received", **byte_identity(payload)})
                        self.http.send_datagram(self.initiated[event.stream_id], SERVER_ACK)
                        self.transmit()
                    persist()
                    return
                for observed in self.http.handle_event(event):
                    if isinstance(observed, HeadersReceived):
                        headers = dict(observed.headers)
                        path = headers.get(b":path", b"").decode()
                        require(headers.get(b":method") == b"CONNECT" and headers.get(b":protocol") == b"webtransport"
                                and path.startswith("/echo?"), "unexpected local HTTP/3 request")
                        query = urllib.parse.parse_qs(urllib.parse.urlparse(path).query)
                        self.case, self.mode = query.get("case", [""])[0], query.get("mode", [""])[0]
                        self.http.send_headers(observed.stream_id, [(b":status", b"200"), (b"sec-webtransport-http3-draft", b"draft02")])
                        self.transmit()
                    elif isinstance(observed, DatagramReceived):
                        require(len(observed.data) <= 1024, "fixture datagram exceeds its bound")
                        self.http.send_datagram(observed.stream_id, observed.data)
                        stats["observations"].append({"case": self.case, "connection": self.identity, "kind": "datagram", **byte_identity(observed.data)})
                        self.transmit()
                    elif isinstance(observed, WebTransportStreamDataReceived):
                        buffer = self.buffers.setdefault(observed.stream_id, bytearray())
                        buffer.extend(observed.data)
                        require(len(buffer) <= 65536, "fixture byte stream exceeds its bound")
                        if observed.stream_ended:
                            payload = bytes(self.buffers.pop(observed.stream_id))
                            unidirectional = stream_is_unidirectional(observed.stream_id)
                            reply = self.http.create_webtransport_stream(observed.session_id, is_unidirectional=True) if unidirectional else observed.stream_id
                            self._quic.send_stream_data(reply, payload, end_stream=True)
                            stats["observations"].append({"case": self.case, "connection": self.identity, "kind": "uni" if unidirectional else "bidi", **byte_identity(payload)})
                            if self.mode == "incoming" and not unidirectional:
                                initiated = self.http.create_webtransport_stream(observed.session_id, is_unidirectional=False)
                                self.initiated[initiated] = observed.session_id
                                self._quic.send_stream_data(initiated, SERVER_BIDI, end_stream=True)
                                stats["observations"].append({"case": self.case, "connection": self.identity, "kind": "server_bidi_sent", **byte_identity(SERVER_BIDI)})
                            self.transmit()
                persist()
            except Exception as error:
                stats["errors"].append(str(error))
                persist()
                self.close(error_code=1, reason_phrase="local fixture failed")

    async def run():
        configuration = QuicConfiguration(is_client=False, alpn_protocols=H3_ALPN, max_datagram_frame_size=65536)
        configuration.load_cert_chain(str(cert_path), str(key_path))
        server = await serve("127.0.0.1", 0, configuration=configuration, create_protocol=EchoProtocol)
        port = server._transport.get_extra_info("sockname")[1]
        persist()
        print(json.dumps({"url": f"https://127.0.0.1:{port}/echo", "certificate_hash": list(certificate.fingerprint(hashes.SHA256())),
                          "certificate_algorithm": "ECDSA P-256", "validity": "one day", "aioquic": aioquic.__version__}), flush=True)
        try:
            await asyncio.Future()
        finally:
            server.close()
    asyncio.run(run())


class FixtureState:
    def __init__(self, quic):
        self.quic = quic
        self.lock = threading.Lock()
        self.cases = {}
        self.held = {}
        self.results = {}

    def register(self, protocol, mode):
        case = uuid.uuid4().hex
        config = {"case": case, "protocol": protocol, "mode": mode}
        if protocol == "webtransport":
            config["quic"] = {**self.quic, "url": self.quic["url"] + "?case=" + case + "&mode=" + mode}
        with self.lock:
            self.cases[case] = config
            self.held[case] = threading.Event()
        return case


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_):
        pass

    def handle(self):
        try:
            super().handle()
        except ConnectionResetError:
            pass  # Closing the owned browser may reset a held fixture request.

    def respond(self, data, content_type):
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        try:
            self.wfile.write(data)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        case = urllib.parse.parse_qs(parsed.query).get("case", [""])[0]
        if parsed.path == "/hold" and case in self.server.fixture.held:
            self.server.fixture.held[case].wait(12)
            self.respond(b"held until fixture finished", "text/plain")
        elif parsed.path == "/fixture" and case in self.server.fixture.cases:
            page = FIXTURE.read_text().replace("__FIXTURE_CONFIG__", json.dumps(self.server.fixture.cases[case]))
            self.respond(page.encode(), "text/html; charset=utf-8")
        else:
            self.respond(b"", "text/plain")

    def do_POST(self):
        parsed = urllib.parse.urlparse(self.path)
        case = urllib.parse.parse_qs(parsed.query).get("case", [""])[0]
        length = int(self.headers.get("Content-Length", "0"))
        require(parsed.path == "/release" and case in self.server.fixture.held and 0 < length <= 65536, "invalid local fixture report")
        result = json.loads(self.rfile.read(length))
        with self.server.fixture.lock:
            self.server.fixture.results[case] = result
        self.server.fixture.held[case].set()
        self.respond(b"{}", "application/json")


def decode_payload(event):
    encoding = event.get("payload_encoding", "utf-8")
    require(encoding in ("utf-8", "base64"), "archived API event has an unknown payload encoding")
    data = base64.b64decode(event.get("payload", ""), validate=True) if encoding == "base64" else event.get("payload", "").encode()
    require(len(data) == event.get("bytes", 0), "archived API event byte count differs from payload")
    return data


def verify_chunks(events, expected, allow_truncation=False):
    offset = 0
    for event in events:
        payload = decode_payload(event)
        observed = event.get("metadata", {}).get("observed_bytes")
        require(isinstance(observed, int) and observed >= len(payload), "API chunk lacks its original observed byte count")
        require(offset + observed <= len(expected) and payload == expected[offset:offset + len(payload)], "API chunk bytes or ordering differ from the known stream")
        require((allow_truncation or observed == len(payload)) and bool(event.get("truncated")) == (observed > len(payload)), "API chunk truncation is missing or incorrect")
        offset += observed
    require(offset == len(expected), "API chunk original byte counts lost or repeated stream data")


def saved_session(data_dir, capture):
    sessions = []
    with (data_dir / "sessions.jsonl").open("rb") as source:
        for raw in source:
            entry = json.loads(raw)
            session = entry.get("session") or {}
            if entry.get("action") == "session" and session.get("hash_id") == capture["saved_hash_id"]:
                sessions.append(session)
    require(len(sessions) == 1 and sessions[0].get("capture_session_id") == capture["session_id"]
            and sessions[0].get("capture_digest") == capture["capture_sha256"], "saved transport archive provenance is wrong")
    return sessions[0]


def verify_capture_preview(capture, session):
    descriptors = {item["id"]: item for item in capture.get("captured_requests", [])}
    states = {}
    for request in session["requests"]:
        stream = request.get("stream")
        if not stream:
            continue
        coverage = stream["capture"]
        states[coverage["state"]] = states.get(coverage["state"], 0) + 1
        expected = {"protocol": stream["protocol"], "source": stream["source"], "state": stream["state"],
                    "capture_state": coverage["state"], "captured_bytes": coverage["captured_bytes"],
                    "captured_events": coverage["captured_events"], "dropped_events": coverage["dropped_events"]}
        require(descriptors.get(request["id"], {}).get("stream") == expected, "immediate CLI stream descriptor differs from the sealed archive")
    require(capture.get("stream_capture_states") == states, "immediate CLI output lost protocol coverage states")
    warnings = capture.get("capture_warnings", [])
    require(len(warnings) <= 16 and all(len(value.encode()) <= 128 for value in warnings)
            and len(warnings) + capture.get("capture_warnings_omitted", -1) == capture.get("capture_warnings_total", -2), "CLI capture warning preview is unbounded or miscounted")
    stats = capture.get("capture_stats", {})
    require(stats.get("protocol_payloads") == 1 and stats.get("protocol_observer_reported_contexts", 0) >= 1
            and stats.get("protocol_observer_dropped_events", 0) == 0, "capture lost observer statistics or reported dropped API events")


def verify_rtc(records, mode, result):
    require(len(records) == 2, f"expected two observed RTC data channels, got {len(records)}")
    left = [stress_payload(index) for index in range(STRESS_MESSAGES)] if mode == "stress" else [LARGE] if mode == "truncated" else RTC_LEFT
    right_received = {"messages": STRESS_MESSAGES, **byte_identity(b"".join(left))} if mode == "stress" else [byte_identity(data) for data in left]
    require(result.get("left_received") == [byte_identity(data) for data in RTC_RIGHT]
            and result.get("right_received") == right_received, "independent RTC delivery hashes differ")
    require(result.get("peers_closed") == (mode != "open"), "RTC fixture did not establish its intended connection lifetime")
    remaining = [(left, RTC_RIGHT), (RTC_RIGHT, left)]
    for request in records:
        events = request["stream"]["events"]
        observed = {direction: [event for event in events if event.get("kind") == "message" and event.get("direction") == direction]
                    for direction in ("sent", "received")}
        matches = []
        for index, (sent, received) in enumerate(remaining):
            if len(observed["sent"]) != len(sent) or len(observed["received"]) != len(received):
                continue
            valid = True
            for direction, expected in (("sent", sent), ("received", received)):
                for event, value in zip(observed[direction], expected):
                    payload = decode_payload(event)
                    valid = valid and event.get("metadata", {}).get("observed_bytes") == len(value)
                    valid = valid and (payload == value if not event.get("truncated") else mode == "truncated" and value.startswith(payload) and 0 < len(payload) < len(value))
            if valid:
                matches.append(index)
        require(len(matches) == 1, "RTC accepted sends / receives / typed-view boundaries differ from fixture bytes")
        remaining.pop(matches[0])
    if mode == "truncated":
        truncated = [event for record in records for event in record["stream"]["events"] if event.get("truncated")]
        require(truncated and all(0 < event["bytes"] <= 16384 for event in truncated), "large RTC payload did not disclose its bounded prefix")


def verify_wt(records, mode, result, server_stats, case):
    require(len(records) == 1, f"expected one observed WebTransport session, got {len(records)}")
    expected_bidi = LARGE if mode == "truncated" else BIDI
    require(result.get("datagram") == byte_identity(DATAGRAM) and result.get("unidirectional") == byte_identity(UNI)
            and result.get("bidirectional") == byte_identity(expected_bidi), "independent WebTransport echo bytes differ")
    require(result.get("transport_closed") == (mode != "open"), "WebTransport fixture did not establish its intended connection lifetime")
    observations = [item for item in server_stats["observations"] if item["case"] == case]
    server_expected = [("datagram", DATAGRAM), ("uni", UNI), ("bidi", expected_bidi)]
    if mode == "incoming":
        server_expected += [("server_bidi_sent", SERVER_BIDI), ("server_bidi_received", SERVER_REPLY)]
        require(result.get("server_bidirectional") == {"received": byte_identity(SERVER_BIDI), "sent": byte_identity(SERVER_REPLY),
                "acknowledgement": byte_identity(SERVER_ACK), "reader": "byob", "view_bytes": 7}, "server-initiated bidi/BYOB result differs from its independent endpoint")
    require([(item["kind"], item["bytes"], item["sha256"]) for item in observations]
            == [(kind, len(data), hashlib.sha256(data).hexdigest()) for kind, data in server_expected],
            "independent QUIC receiver did not observe the expected datagram/uni/bidi bytes")
    events = records[0]["stream"]["events"]
    bidi_ids = {event["channel_id"] for event in events if event.get("kind") == "chunk" and event.get("channel_id", "").startswith("bidi-")}
    require(len(bidi_ids) == (2 if mode == "incoming" else 1), "bidirectional API stream lost its common channel identity")
    client_ids = []
    for channel in bidi_ids:
        sent = [event for event in events if event.get("kind") == "chunk" and event.get("channel_id") == channel and event.get("direction") == "sent"]
        if sent and sum(event.get("metadata", {}).get("observed_bytes", 0) for event in sent) == len(expected_bidi):
            client_ids.append(channel)
    require(len(client_ids) == 1, "client-created bidi stream cannot be identified by its fixture byte sequence")
    client_id = client_ids[0]
    for direction in ("sent", "received"):
        datagrams = [event for event in events if event.get("kind") == "message" and event.get("channel_id") == "datagrams" and event.get("direction") == direction]
        expected_datagrams = [DATAGRAM] + ([SERVER_ACK] if mode == "incoming" and direction == "received" else [])
        require([decode_payload(event) for event in datagrams] == expected_datagrams, "WebTransport datagram boundary or bytes changed")
        chunks = [event for event in events if event.get("kind") == "chunk" and event.get("direction") == direction]
        uni = [event for event in chunks if event.get("channel_id", "").startswith("uni-")]
        bidi = [event for event in chunks if event.get("channel_id") == client_id]
        verify_chunks(uni, UNI)
        if mode == "gap" and direction == "received":
            require(any(event.get("kind") == "gap" and event.get("reason") == "pipeTo_bypass" for event in events), "pipeTo consumption was not disclosed as a coverage gap")
        else:
            verify_chunks(bidi, expected_bidi, allow_truncation=mode == "truncated")
        if mode == "incoming":
            server_bidi = [event for event in chunks if event.get("channel_id") in bidi_ids - {client_id}]
            verify_chunks(server_bidi, SERVER_REPLY if direction == "sent" else SERVER_BIDI)
            if direction == "received":
                require(len(server_bidi) >= 3 and all(event["bytes"] <= 7 for event in server_bidi), "BYOB reader view boundaries were not retained")
    if mode == "truncated":
        truncated = [event for event in events if event.get("truncated")]
        require(truncated and all(0 < event["bytes"] <= 16384 for event in truncated), "large WebTransport payload did not disclose its bounded prefix")


def verify_stream_reads(runner, capture, records, mode):
    summaries = []
    for request in records:
        started = time.perf_counter()
        stream = request["stream"]
        require(stream.get("source") == "page_api" and stream["capture"].get("scope") == "instrumented_api_calls", "API evidence lacks its narrow collector scope")
        require(stream.get("clock") == "performance_now_seconds", "API clock domain is not explicit")
        provenance = stream.get("metadata", {})
        require(provenance.get("observer_trust") == "page_controlled" and provenance.get("execution_context_id", 0) > 0
                and provenance.get("execution_context_generation", 0) > 0 and provenance.get("time_origin_ms", 0) > 0,
                "API observation lacks its realm, generation, clock origin, or trust provenance")
        require(provenance.get("observer_limits", {}).get("maxMessageBytes") == (16384 if mode == "truncated" else 8 * 1024 * 1024),
                "archived API observer payload limit differs from the requested collector configuration")
        require("accepted_send_does_not_prove_remote_delivery" in stream["capture"].get("limitations", []), "API coverage omitted accepted-send limitations")
        events = stream["events"]
        require([event["sequence"] for event in events] == list(range(1, len(events) + 1)), "API event sequence has a gap or duplicate")
        complete = mode in COMPLETE_MODES
        expected_state = "complete" if complete else "partial"
        require(stream["capture"]["state"] == expected_state, f"{mode} API observation reported {stream['capture']['state']}")
        require(stream["capture"].get("captured_events") == len(events) and stream["capture"].get("observed_events") == len(events)
                and stream["capture"].get("dropped_events") == 0, "bounded local fixture lost API events")
        if complete:
            require(not any(event.get("kind") in ("gap", "error") or event.get("truncated") for event in events), "complete API fixture retained an error or undisclosed gap")
        if mode != "open":
            require(events[-1]["kind"] == "closed", "completed fixture lacks its terminal API close event")
        if mode == "open":
            require(any(event.get("kind") == "gap" and event.get("reason") == "capture_ended" for event in events), "open API connection lacks a capture-ended gap")
        info, _, _ = runner.cli("stream", request["id"], "--saved", capture["saved_hash_id"], "--info")
        require(info["capture"]["scope"] == "instrumented_api_calls" and info.get("source") == "page_api", "bounded reader lost API provenance")
        checked, _, code = runner.cli("stream", request["id"], "--saved", capture["saved_hash_id"], "--info", "--require-complete", success=complete)
        require((code == 0) == complete, "require-complete did not honor declared API scope and gaps")
        created = next((event for event in events if event.get("kind") == "created"), None)
        require(created is not None, "API fixture lacks its connection-created metadata event")
        _, _, code = runner.cli("stream", request["id"], "--saved", capture["saved_hash_id"], "--event", str(created["sequence"]), success=False)
        require(code != 0, "lifecycle metadata was incorrectly returned as a complete empty payload")
        selected = [event for event in events if event.get("kind") in ("message", "chunk") and event.get("bytes", 0) > 0]
        for event in selected:
            expected_stage = "delivered_value" if event.get("direction") == "received" else "accepted_send_argument" if stream["protocol"] == "webrtc" else "write_argument_snapshot"
            require(event.get("metadata", {}).get("payload_stage") == expected_stage, "payload evidence lost its API acceptance/delivery stage")
        require(stream["capture"].get("captured_bytes") == sum(event["bytes"] for event in selected)
                and stream["capture"].get("observed_bytes") == sum(event.get("metadata", {}).get("observed_bytes", event["bytes"]) for event in selected),
                "API stream byte counters disagree with its event payloads")
        # Save two exact events, preferring an oversized bounded prefix when present.
        chosen = [event for event in selected if event.get("truncated")][:1] or selected[:1]
        if selected and selected[-1] not in chosen:
            chosen.append(selected[-1])
        if mode == "stress":
            chosen.insert(1, selected[STRESS_MESSAGES // 2])
        pagination = None
        if mode == "stress":
            metadata, after, pages, page_seconds = [], -1, 0, 0
            while True:
                page, elapsed, _ = runner.cli("stream", request["id"], "--saved", capture["saved_hash_id"], "--after", str(after),
                                              "--events", "1000", "--max-bytes", "65536", "--require-complete")
                pages += 1
                page_seconds += elapsed
                require(page["next_after"] > after and pages <= 16, "RTC burst metadata pages did not make bounded progress")
                metadata.extend(page["events"])
                after = page["next_after"]
                if page["page_complete"]:
                    break
            require([event["sequence"] for event in metadata] == list(range(1, len(events) + 1))
                    and sum(event["bytes"] for event in metadata) == stream["capture"]["captured_bytes"], "bounded CLI pages lost RTC burst metadata")
            pagination = {"pages": pages, "seconds": page_seconds, "events": len(metadata)}
        artifacts = []
        for event in chosen:
            artifact, _, _ = runner.cli("stream", request["id"], "--saved", capture["saved_hash_id"], "--event", str(event["sequence"]), "--head", "0", "--save", "--max-bytes", "4096")
            path = Path(artifact["path"])
            payload = decode_payload(event)
            require(path.is_relative_to(runner.directory) and path.read_bytes() == payload
                    and artifact["artifact_sha256"] == hashlib.sha256(payload).hexdigest(), "exact selected API event artifact changed")
            require(artifact.get("view_complete") is True and artifact.get("payload_complete") == (not bool(event.get("truncated"))), "selected event artifact misreported byte completeness")
            artifacts.append({"sequence": event["sequence"], **byte_identity(payload)})
        summary = {"id": request["id"], "source": stream["source"], "clock": stream["clock"], "provenance": provenance, "coverage": stream["capture"],
                   "event_count": len(events), "artifacts": artifacts, "lifecycle_payload_selection_rejected": True,
                   "verification_seconds": time.perf_counter() - started}
        if mode == "stress":
            serialized = len(json.dumps(request, ensure_ascii=False, separators=(",", ":")).encode())
            require(serialized < 8 * 1024 * 1024, "RTC burst record exceeds half the native backlog bound")
            summary.update(metadata_pagination=pagination, serialized_record_bytes=serialized,
                           payload_messages=len(selected), payload_sequence=byte_identity(b"".join(decode_payload(event) for event in selected)))
        else:
            summary["payload_hashes"] = [byte_identity(decode_payload(event)) for event in selected]
        summaries.append(summary)
    return summaries


def run_case(runner, state, origin, data_dir, run_id, protocol, mode, quic_dir, progress):
    case = state.register(protocol, mode)
    archive_size_before = (data_dir / "sessions.jsonl").stat().st_size if (data_dir / "sessions.jsonl").exists() else 0
    memory = OwnedHostRSS(runner)
    memory.start()
    try:
        options = ["--max-body", "16384"] if mode == "truncated" else []
        capture, elapsed, _ = runner.cli("browser", "open", origin + "/fixture?case=" + case, "--browser", "headless", "--keep-tab",
                                       "--protocol-payloads", "--idle", "400ms", "--timeout", "15s", *options)
    finally:
        memory_report = memory.finish()
        progress["owned_host_rss"] = memory_report
    progress.update(capture_seconds=elapsed, archive=capture.get("saved_hash_id"), capture_sha256=capture.get("capture_sha256"))
    verify_capture_link(runner, run_id, capture)
    with state.lock:
        result = state.results.get(case)
    progress["independent_page_result"] = result
    session = saved_session(data_dir, capture)
    verify_capture_preview(capture, session)
    records = [request for request in session["requests"] if request.get("record_kind") == protocol and request.get("stream", {}).get("source") == "page_api"]
    progress["observed_records"] = [{"id": request["id"], "stream": {key: value for key, value in request["stream"].items() if key != "events"},
                                     "events": [{**{key: value for key, value in event.items() if key != "payload"}, "payload_identity": byte_identity(decode_payload(event))}
                                                for event in request["stream"]["events"][:32]]} for request in records]
    stats = json.loads((quic_dir / "stats.json").read_text()) if protocol == "webtransport" else None
    if not result or result.get("error"):
        progress["fixture_readback"], _ = runner.evaluate(capture["tab_id"], "({stage:window.fixtureStage,result:window.fixtureResult,status:document.querySelector('#status')?.textContent})")
        if stats:
            progress["quic_observations"] = [item for item in stats["observations"] if item["case"] == case]
            progress["quic_errors"] = stats["errors"]
        require(False, "local transport fixture did not complete: " + json.dumps(progress["fixture_readback"]))
    if protocol == "webrtc":
        verify_rtc(records, mode, result)
    else:
        require(not stats["errors"], "local QUIC fixture failed: " + json.dumps(stats["errors"]))
        verify_wt(records, mode, result, stats, case)
    streams = verify_stream_reads(runner, capture, records, mode)
    lifecycle = [request for request in session["requests"] if request.get("record_kind") == "webtransport" and request.get("stream", {}).get("source") == "cdp_lifecycle"]
    for request in lifecycle:
        require(request["stream"]["capture"]["state"] != "complete", "CDP lifecycle metadata implied complete payload capture")
    if mode == "open":
        runner.evaluate(capture["tab_id"], "window.closeFixture();true")
    progress.pop("observed_records", None)
    summary = {"protocol": protocol, "mode": mode, "capture_seconds": elapsed, "archive": capture["saved_hash_id"],
            "capture_sha256": capture["capture_sha256"], "streams": streams, "independent_page_result": result,
            "cdp_lifecycle_records": len(lifecycle), "owned_host_rss": memory_report,
            "capture_completion": {key: capture.get(key) for key in ("load_state", "timed_out", "pending_requests", "duration_ms")},
            "capture_stats": capture.get("capture_stats"), "capture_warnings": capture.get("capture_warnings"),
            "stream_capture_states": capture.get("stream_capture_states")}
    if mode == "stress":
        summary.update(messages=STRESS_MESSAGES, message_bytes=STRESS_MESSAGE_BYTES,
                       expected_received_sequence=byte_identity(b"".join(stress_payload(index) for index in range(STRESS_MESSAGES))),
                       archive_growth_bytes=(data_dir / "sessions.jsonl").stat().st_size - archive_size_before,
                       limits="One 1MiB RTC burst, observed at both local endpoints plus two receipt messages. Capture time includes navigation, ICE setup, idle, collection, archive/evidence writes and RSS sampling. Send-to-receiver time includes receiver hashing. No uninstrumented baseline, sustained throughput, wire-rate or aggregate browser memory claim.")
    return summary


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--webtransport", action="store_true", help="Require local HTTP/3 datagram, uni and bidi echo checks")
    parser.add_argument("--stress", action="store_true", help="Also verify one 1,024-message RTC burst of 1,024-byte messages at both endpoints")
    parser.add_argument("--only-case", help="Run one selected protocol:mode while diagnosing a fixture failure")
    parser.add_argument("--quic-python", default=os.environ.get("REP_QUIC_PYTHON", sys.executable), help="Interpreter with aioquic and cryptography; no dependencies are installed")
    parser.add_argument("--serve-quic", type=Path, help=argparse.SUPPRESS)
    options = parser.parse_args()
    if options.serve_quic:
        serve_quic(options.serve_quic.resolve())
        return 0
    require(options.output is not None, "--output is required")
    output = options.output.resolve()
    require(not output.exists(), "output already exists; choose a new report path")
    binary, host = (shutil.which(os.environ.get(key, fallback)) for key, fallback in (("REP_BINARY", "rep"), ("REP_HOST_BINARY", "rep-host")))
    require(binary and host, "REP_BINARY and REP_HOST_BINARY must name executables")
    extension = Path(os.environ.get("REP_EXTENSION_PATH", ROOT.parent / "rep")).resolve()
    report = {"schema": 1, "passed": False, "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "with_model": False, "cases": [], "fixtures": "owned loopback WebRTC and optional HTTP/3 WebTransport only",
              "harness_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(), "fixture_sha256": hashlib.sha256(FIXTURE.read_bytes()).hexdigest(),
              "test_data_retention": "Temporary browser profile, task archives, certificates and payload files are removed after verification; this report retains hashes, coverage and results.",
              "limits": "Checks accepted API calls and delivered application bytes in the instrumented page. It does not establish wire coverage, other realms, sustained throughput, or total browser memory."}
    try:
        with tempfile.TemporaryDirectory(prefix="rep-transports-", dir="/tmp") as temporary:
            directory = Path(temporary)
            quic_process, quic_log, server, runner = None, None, None, None
            try:
                quic, quic_dir = None, directory / "quic"
                if options.webtransport:
                    quic_log = (directory / "quic.stderr").open("wb")
                    quic_process = subprocess.Popen([options.quic_python, str(Path(__file__).resolve()), "--serve-quic", str(quic_dir)], stdout=subprocess.PIPE, stderr=quic_log)
                    ready, _, _ = select.select([quic_process.stdout], [], [], 15)
                    require(ready, "local QUIC fixture did not start within 15 seconds")
                    raw = quic_process.stdout.readline()
                    require(raw, "local QUIC fixture failed to start; selected interpreter needs aioquic and cryptography")
                    quic = json.loads(raw)
                    report["quic"] = {key: value for key, value in quic.items() if key != "certificate_hash"}
                    report["quic"]["certificate_sha256"] = bytes(quic["certificate_hash"]).hex()
                state = FixtureState(quic)
                server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
                server.fixture, server.daemon_threads = state, True
                threading.Thread(target=server.serve_forever, daemon=True).start()
                frozen = directory / "extension"
                shutil.copytree(extension, frozen, ignore=shutil.ignore_patterns(".git", "node_modules", ".env", ".env.*", "coverage", "*.zip", "*.crx"))
                digest = hashlib.sha256()
                for path in sorted([*frozen.rglob("*.js"), frozen / "manifest.json"]):
                    digest.update(str(path.relative_to(frozen)).encode() + b"\0" + path.read_bytes())
                report["extension_js_sha256"] = digest.hexdigest()
                runner = Runner(Path(binary).resolve(), Path(host).resolve(), frozen, directory, "transport-capture")
                runner.base = [runner.binary, "--workspace", "transport-capture", "--task", runner.task]
                runner.owner = "transport-capture/" + runner.task
                report.update(task=runner.task, cli_sha256=hashlib.sha256(Path(binary).read_bytes()).hexdigest(), host_sha256=hashlib.sha256(Path(host).read_bytes()).hexdigest())
                report["browser_start_seconds"] = runner.start()
                with urllib.request.urlopen(f"http://127.0.0.1:{runner.state['port']}/json/version", timeout=5) as response:
                    version = json.load(response)
                report["chromium"] = {key: version.get(key) for key in ["Browser", "Protocol-Version", "V8-Version"]}
                scope, _, _ = runner.cli("scope")
                require(Path(scope["data_dir"]).is_relative_to(directory), "transport evidence scope escaped the owned directory")
                run, _, _ = runner.cli("evidence", "begin", "--intent", "Verify opt-in local transport API observations", "--stop", "Stop after exact normal, truncated, and explicit-gap fixtures; close all owned resources")
                runner.base.extend(["--run", run["id"]])
                report["run_id"] = run["id"]
                origin = f"http://127.0.0.1:{server.server_port}"
                cases = [("webrtc", mode) for mode in ("closed", "open", "truncated")]
                if options.stress:
                    cases += [("webrtc", "stress")]
                if options.webtransport:
                    cases += [("webtransport", mode) for mode in ("closed", "open", "gap", "truncated", "incoming")]
                if options.only_case:
                    cases = [(protocol, mode) for protocol, mode in cases if protocol + ":" + mode == options.only_case]
                    require(cases, "--only-case must match a case enabled by --webtransport/--stress")
                    report["only_case"] = options.only_case
                for protocol, mode in cases:
                    progress = {"protocol": protocol, "mode": mode}
                    report["cases"].append(progress)
                    progress.update(run_case(runner, state, origin, Path(scope["data_dir"]), run["id"], protocol, mode, quic_dir, progress))
                report["passed"] = True
            finally:
                cleanup_errors = []
                if runner:
                    try:
                        runner.stop()
                        report["owned_browser_stopped"] = True
                    except Exception as error:
                        cleanup_errors.append("owned browser: " + str(error))
                if server:
                    try:
                        for event in server.fixture.held.values():
                            event.set()
                        server.shutdown()
                        server.server_close()
                    except Exception as error:
                        cleanup_errors.append("owned HTTP fixture: " + str(error))
                if quic_process:
                    try:
                        quic_process.terminate()
                        try:
                            quic_process.wait(timeout=5)
                        except subprocess.TimeoutExpired:
                            quic_process.kill()
                            quic_process.wait(timeout=5)
                        report["owned_quic_server_stopped"] = True
                    except Exception as error:
                        cleanup_errors.append("owned QUIC fixture: " + str(error))
                    finally:
                        quic_process.stdout.close()
                if quic_log:
                    quic_log.close()
                if cleanup_errors:
                    raise RuntimeError("; ".join(cleanup_errors))
    except Exception as error:
        report["passed"] = False
        report["error"] = str(error)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"passed": report["passed"], "report": str(output), "error": report.get("error")}))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
