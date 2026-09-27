#!/usr/bin/env python3
"""Verify native packets and synthetic WebRTC media in private local fixtures.

REP_BINARY=/tmp/rep-native/rep REP_HOST_BINARY=/tmp/rep-native/rep-host \
  python3 scripts/verify_native_capture.py --output /tmp/native-capture.json

No camera/microphone, external peers, elevated privileges or interface changes.
Live packet checks use only two owned loopback UDP ports. Permission failures
remain explicitly unavailable; constructed offline PCAP checks are separate.
"""
from __future__ import annotations

import argparse
import array
import hashlib
import http.server
import json
import math
import os
from pathlib import Path
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
import urllib.request

from verify_decision_runtime import ROOT, Runner, require
from verify_protocol_evidence import OwnedHostRSS, operation_pair, verify_capture_link
from verify_transport_capture import byte_identity, decode_payload, saved_session

FIXTURE = Path(__file__).parent / "fixtures" / "native-media" / "index.html"


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        data = FIXTURE.read_bytes() if urllib.parse.urlparse(self.path).path == "/media" else b""
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(data)


def probe_media(path, kind, ffprobe, ffmpeg):
    result = subprocess.run([ffprobe, "-v", "error", "-count_frames", "-show_streams", "-show_format", "-of", "json", str(path)], capture_output=True, timeout=20)
    require(result.returncode == 0, "ffprobe could not parse the exported native media container: " + result.stderr.decode(errors="replace")[:1024])
    value = json.loads(result.stdout)
    streams = value.get("streams", [])
    require(len(streams) == 1 and streams[0].get("codec_type") == kind, "media artifact does not contain exactly its declared track kind")
    track = streams[0]
    require(int(track.get("nb_read_frames", "0")) > 0, "exported media track contains no decoded frames")
    if kind == "video":
        require(track.get("width") == 320 and track.get("height") == 180, "synthetic video dimensions changed")
    else:
        require(int(track.get("sample_rate", "0")) > 0 and track.get("channels", 0) > 0, "synthetic audio has no valid sample format")
    summary = {"format": value.get("format", {}).get("format_name"), "codec": track.get("codec_name"), "kind": kind,
            "decoded_frames": int(track["nb_read_frames"]), "width": track.get("width"), "height": track.get("height"),
            "sample_rate": track.get("sample_rate"), "channels": track.get("channels"), "full_decode": True}
    if kind == "audio":
        pcm = subprocess.run([ffmpeg, "-v", "error", "-i", str(path), "-map", "0:0", "-ac", "1", "-ar", "48000", "-f", "f32le", "-"], capture_output=True, timeout=20)
        require(pcm.returncode == 0 and not pcm.stderr.strip() and 0 < len(pcm.stdout) <= 8 * 1024 * 1024 and len(pcm.stdout) % 4 == 0, "audio PCM decode failed or exceeded its fixture bound")
        samples = array.array("f");samples.frombytes(pcm.stdout)
        if sys.byteorder != "little":
            samples.byteswap()
        rms = math.sqrt(sum(value * value for value in samples) / len(samples))
        require(math.isfinite(rms) and rms > 0.001, "recorded synthetic oscillator audio is silent")
        first_active = next((index for index, value in enumerate(samples) if abs(value) > 0.005), len(samples))
        tone = samples[first_active + 4800:-4800]  # Exclude startup/end encoder transients.
        require(len(tone) >= 4800, "recorded oscillator has too little active audio for an independent tone check")
        crossings = sum(left <= 0 < right for left, right in zip(tone, tone[1:]))
        frequency = crossings * 48000 / (len(tone) - 1)
        require(430 <= frequency <= 450, "decoded synthetic audio does not retain its expected 440 Hz oscillator tone")
        summary.update(pcm_samples=len(samples), pcm_rms=rms, tone_estimate_hz=frequency, tone_samples=len(tone),
                       decoded_pcm_sha256=hashlib.sha256(pcm.stdout).hexdigest())
    else:
        # Preserve the native demuxer time base. FFmpeg's default output encoder
        # time base can round distinct variable-rate frames onto the same DTS.
        decoded = subprocess.run([ffmpeg, "-v", "error", "-i", str(path), "-map", "0:0", "-fps_mode", "passthrough",
                                  "-enc_time_base:v", "demux", "-c:v", "rawvideo", "-pix_fmt", "yuv420p", "-f", "rawvideo", "-"], capture_output=True, timeout=20)
        require(decoded.returncode == 0 and not decoded.stderr.strip(), "exported video decoder reported errors: " + decoded.stderr.decode(errors="replace")[:1024])
        frame_bytes = 320 * 180 * 3 // 2
        require(0 < len(decoded.stdout) <= 32 * 1024 * 1024 and len(decoded.stdout) == frame_bytes * summary["decoded_frames"], "decoded video byte/frame count differs from ffprobe")
        require(decoded.stdout[:frame_bytes] != decoded.stdout[-frame_bytes:], "synthetic moving video did not preserve changing frames")
        summary.update(decoded_frame_bytes=len(decoded.stdout), decoded_frames_sha256=hashlib.sha256(decoded.stdout).hexdigest())
    return summary


def media_case(runner, origin, data_dir, run_id, mode, ffprobe, ffmpeg, progress):
    opened, _, _ = runner.cli("browser", "open", origin + "/media", "--browser", "headless", "--keep-tab", "--idle", "100ms")
    tab = opened["tab_id"]
    options = ["--max-body", "128"] if mode == "bounded" else []
    memory = OwnedHostRSS(runner)
    memory.start()
    try:
        captured, elapsed, _ = runner.cli("browser", "action", "window.startMediaFixture()", "--tab", str(tab), "--browser", "headless",
                                          "--webrtc-media", "--user-gesture", "--timeout", "4s", "--settle", "1s", "--idle", "400ms", *options)
    finally:
        progress["owned_host_rss"] = memory.finish()
    progress.update(capture_seconds=elapsed, archive=captured.get("saved_hash_id"), capture_sha256=captured.get("capture_sha256"),
                    capture_completion={key: captured.get(key) for key in ("load_state", "timed_out", "pending_requests", "duration_ms")})
    verify_capture_link(runner, run_id, captured)
    before, _ = runner.evaluate(tab, "window.fixtureStats()")
    after, _ = runner.evaluate(tab, "new Promise(resolve=>setTimeout(()=>window.fixtureStats().then(resolve),500))")
    progress["originals_after_capture"] = {"first": before, "second": after}
    for state in (before, after):
        require(state.get("left_state") == "connected" and state.get("right_state") == "connected"
                and state.get("audio_context") == "running" and not state.get("closed"), "capture stop interrupted original peer connections or synthetic audio")
        require(len(state.get("tracks", [])) == 2 and len(state.get("received_tracks", [])) == 2
                and all(track["ready_state"] == "live" and track["enabled"] for track in state["tracks"] + state["received_tracks"]),
                "capture stop ended or disabled an original media track")
    require(all(after[key] > before[key] for key in ("audio_outbound_bytes", "audio_inbound_bytes", "video_outbound_bytes", "video_inbound_bytes"))
            and after["frames_decoded"] > before["frames_decoded"] and after["received_audio_rms"] > 0.001,
            "original media flow did not continue after recorder cleanup")
    session = saved_session(data_dir, captured)
    records = [request for request in session["requests"] if request.get("record_kind") == "webrtc_media"]
    progress["observed_records"] = [{"id": record["id"], "metadata": record["stream"].get("metadata"), "coverage": record["stream"]["capture"],
                                     "events": [{key: value for key, value in event.items() if key != "payload"} for event in record["stream"]["events"][:32]]} for record in records]
    require(len(records) == 4, f"expected four per-track media intervals, got {len(records)}")
    expected = {(direction, track["kind"], track["id"]) for direction, key in (("sent", "tracks"), ("received", "received_tracks")) for track in after[key]}
    seen, summaries, incomplete = set(), [], 0
    progress["streams"] = summaries
    for request in records:
        stream = request["stream"]
        metadata, coverage, events = stream.get("metadata", {}), stream["capture"], stream["events"]
        key = (metadata.get("direction"), metadata.get("media_kind"), metadata.get("track_id"))
        require(key in expected and key not in seen, "media interval lost its original track/direction identity")
        seen.add(key)
        require(stream.get("protocol") == "webrtc_media" and stream.get("source") == "browser_media_recorder"
                and stream.get("payload_semantics") == "reencoded_media" and coverage.get("scope") == "recorded_media_interval",
                "media evidence lost its recorder source, semantics or interval scope")
        require(metadata.get("parent_peer_id") and metadata.get("time_origin_ms", 0) > 0 and metadata.get("mime_type"), "media interval lacks peer, clock or MIME provenance")
        require([event["sequence"] for event in events] == list(range(1, len(events) + 1)), "media events lost sequence ordering")
        chunks = [event for event in events if event.get("kind") == "chunk"]
        require(chunks and [event.get("metadata", {}).get("chunk_index") for event in chunks] == list(range(1, len(chunks) + 1)), "native MediaRecorder chunks are missing or reordered")
        require(all(event.get("payload_encoding") == "base64" for event in chunks), "media bytes are not explicitly encoded as binary")
        payload = b"".join(decode_payload(event) for event in chunks)
        require(len(payload) == coverage["captured_bytes"], "media captured byte count differs from exact archived chunks")
        complete = coverage["state"] == "complete"
        incomplete += not complete
        if mode == "full":
            require(complete and events[-1]["kind"] == "closed" and not any(event.get("truncated") for event in chunks), "normal capture stop did not finalize a complete recorded interval")
            final = events[-1].get("metadata", {})
            require(final.get("recorder_finalized") is True and final.get("chunk_count") == len(chunks)
                    and final.get("observed_media_bytes") == len(payload), "complete interval lost the native recorder's final counts")
        info, _, _ = runner.cli("media", request["id"], "--saved", "latest")
        require(info.get("saved") == captured["saved_hash_id"], "media inspection retained a mutable archive alias")
        target = runner.directory / (mode + "-" + request["id"] + ".webm")
        exported, _, _ = runner.cli("media", request["id"], "--saved", captured["saved_hash_id"], "--save", str(target))
        require(target.read_bytes() == payload, "CLI media export differs from concatenated archived native chunks")
        require(target.stat().st_mode & 0o777 == 0o600 and exported.get("artifact_bytes") == len(payload)
                and exported.get("artifact_sha256") == hashlib.sha256(payload).hexdigest()
                and exported.get("chunks") == len(chunks) and exported.get("assembly_complete") == complete
                and exported.get("container_playability") == "not_verified", "media artifact descriptor misreported bytes, privacy, interval completeness or playback verification")
        _, operation = operation_pair(runner, run_id, exported)
        require(operation.get("status") == "completed" and operation.get("artifact_ids")
                and any(reference.get("kind") == "capture_record" and reference.get("id") == request["id"]
                        and reference.get("metadata", {}).get("saved") == captured["saved_hash_id"] for reference in operation.get("references", [])),
                "media export evidence omitted its imported artifact or exact capture reference")
        _, _, complete_code = runner.cli("media", request["id"], "--saved", captured["saved_hash_id"], "--require-complete", success=complete)
        require((complete_code == 0) == complete, "media require-complete ignored interval coverage")
        _, _, overwrite_code = runner.cli("media", request["id"], "--saved", captured["saved_hash_id"], "--save", str(target), success=False)
        require(overwrite_code != 0 and target.read_bytes() == payload, "media export overwrote an existing artifact")
        summary = {"id": request["id"], "track": metadata, "coverage": coverage, "chunks": len(chunks), **byte_identity(payload),
                   "cli_info": info, "cli_export": exported, "overwrite_rejected": True}
        summaries.append(summary)
        if complete:
            summary["playback_validation"] = probe_media(target, metadata["media_kind"], ffprobe, ffmpeg)
    require(seen == expected, "not all original track directions have a media interval")
    if mode == "bounded":
        require(incomplete > 0, "128-byte chunk cap did not disclose partial media coverage")
    progress.pop("observed_records", None)
    stopped, _ = runner.evaluate(tab, "window.stopMediaFixture()")
    require(stopped.get("closed") and stopped.get("left_state") == "closed" and stopped.get("right_state") == "closed", "owned fixture peer cleanup failed")
    progress["owned_media_stopped"] = True


def checksum(data):
    padded = data + (b"\0" if len(data) % 2 else b"")
    value = sum(struct.unpack("!" + "H" * (len(padded) // 2), padded))
    while value >> 16:
        value = (value & 0xffff) + (value >> 16)
    return (~value) & 0xffff


def udp_packet(source_port, target_port, payload, sequence):
    addresses = socket.inet_aton("127.0.0.1") * 2
    length = 8 + len(payload)
    udp = struct.pack("!HHHH", source_port, target_port, length, 0)
    udp = udp[:6] + struct.pack("!H", checksum(addresses + struct.pack("!BBH", 0, 17, length) + udp + payload) or 0xffff) + payload
    header = struct.pack("!BBHHHBBH", 0x45, 0, 20 + len(udp), sequence, 0, 64, 17, 0) + addresses
    return header[:10] + struct.pack("!H", checksum(header)) + header[12:] + udp


def write_pcap(path, packets, endian="<", nanoseconds=False):
    magic = 0xa1b23c4d if nanoseconds else 0xa1b2c3d4
    data = bytearray(struct.pack(endian + "IHHIIII", magic, 2, 4, 0, 0, 65535, 0))
    for index, packet in enumerate(packets):
        frame = struct.pack(endian + "I", 2) + packet  # DLT_NULL producer-native AF_INET header.
        data.extend(struct.pack(endian + "IIII", 1700000000 + index, 123456000 if nanoseconds else 123456, len(frame), len(frame)))
        data.extend(frame)
    path.write_bytes(data)
    path.chmod(0o600)
    return byte_identity(data)


def packet_suite(runner, run_id, progress):
    source = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    target = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    source.bind(("127.0.0.1", 0));target.bind(("127.0.0.1", 0))
    try:
        source_port, target_port = source.getsockname()[1], target.getsockname()[1]
        payloads = [struct.pack("!BBHII", 0x80, 96, index, index * 960, 0x12345678) + bytes([index]) * 80 for index in range(4)]
        packets = [udp_packet(source_port, target_port, payload, index) for index, payload in enumerate(payloads)]
        interfaces, _, _ = runner.cli("packets", "interfaces")
        available = interfaces.get("interfaces", []) if isinstance(interfaces, dict) else interfaces
        loopback = [entry for entry in available if entry.get("name") == "lo0" and entry.get("loopback")]
        require(len(loopback) == 1, "native packet backend did not enumerate the owned loopback interface")
        progress["loopback_interface"] = loopback[0]
        progress["offline"] = []
        for name, endian, nano in (("little-microseconds", "<", False), ("big-nanoseconds", ">", True)):
            path = runner.directory / (name + ".pcap")
            record = {"source": "constructed PCAP, not a live wire capture", "packets": len(packets), "variant": name,
                      "ports": [source_port, target_port], "payloads": [byte_identity(value) for value in payloads], **write_pcap(path, packets, endian, nano)}
            progress["offline"].append(record)
            pages, descriptions, offset = [], [], 24
            while True:
                inspected, _, _ = runner.cli("packets", "inspect", str(path), "--offset", str(offset), "--limit", "2", "--max-scan-bytes", "1048576", "--max-bytes", "8192")
                require(inspected.get("payload_omitted") is True and inspected["next_offset"] > offset and len(pages) < 4, "native packet metadata pagination lost bounds or progress")
                require(inspected.get("format") == "classic_pcap"
                        and inspected.get("byte_order") == ("little_endian" if endian == "<" else "big_endian")
                        and inspected.get("timestamp_resolution") == ("nanoseconds" if nano else "microseconds")
                        and inspected.get("datalink", {}).get("id") == 0, "native packet inspection lost the file format or link provenance")
                pages.append(inspected);descriptions.extend(inspected["packets"]);offset = inspected["next_offset"]
                if not inspected["has_more"]:
                    break
            require(len(descriptions) == len(payloads) and offset == path.stat().st_size, "native offline inspection lost or repeated packets")
            for index, (descriptor, payload, packet) in enumerate(zip(descriptions, payloads, packets)):
                expected_timestamp = time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime(1700000000 + index)) + ".123456Z"
                require(descriptor.get("source_ip") == "127.0.0.1" and descriptor.get("destination_ip") == "127.0.0.1"
                        and descriptor.get("udp") == {"source_port": source_port, "destination_port": target_port,
                            "length": len(payload) + 8, "captured_payload_bytes": len(payload)}
                        and descriptor["captured_length"] == len(packet) + 4 and descriptor["original_length"] == len(packet) + 4
                        and not descriptor["truncated"] and descriptor.get("timestamp") == expected_timestamp,
                        "offline native UDP header/length/timestamp differs from the exact fixture packet")
                require(descriptor.get("candidates") and all(item.get("verified") is False for item in descriptor["candidates"]), "tentative RTP packet shape was presented as verified protocol content")
            record.update(pages=len(pages), inspected=descriptions, final_offset=offset)

        output = runner.directory / "loopback-live.pcap"
        packet_filter = f"udp and host 127.0.0.1 and (port {source_port} or port {target_port})"
        args = ["packets", "capture", "--interface", "lo0", "--filter", packet_filter, "--output", str(output), "--duration", "2s",
                "--max-packets", "100", "--max-bytes", "1048576", "--snaplen", "65535", "--buffer-bytes", "1048576", "--raw-json", "-j"]
        started = time.monotonic()
        process = subprocess.Popen(runner.base + args, cwd=runner.directory, env=runner.env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        sent, received, errors = [], [], []
        def send_owned():
            time.sleep(0.25)
            if process.poll() is not None:
                return
            try:
                target.settimeout(1)
                for payload in payloads:
                    source.sendto(payload, target.getsockname());sent.append(byte_identity(payload))
                    delivered, peer = target.recvfrom(2048)
                    require(delivered == payload and peer == source.getsockname(), "owned UDP receiver observed unexpected fixture bytes")
                    received.append(byte_identity(delivered));time.sleep(0.1)
            except Exception as error:
                errors.append(str(error))
        sender = threading.Thread(target=send_owned, daemon=True);sender.start()
        try:
            stdout, stderr = process.communicate(timeout=10)
            command_seconds = time.monotonic() - started
        except subprocess.TimeoutExpired:
            process.kill();process.communicate();raise RuntimeError("owned native capture exceeded its bounded deadline")
        finally:
            sender.join(timeout=3)
        sidecar = output.with_suffix(".metadata.json")
        require(sidecar.exists(), "native capture did not preserve attempt metadata")
        result = json.loads(sidecar.read_text())
        progress["live"] = {"capture": result, "exit_code": process.returncode, "sent": sent, "received": received, "sender_errors": errors,
                            "command_seconds": command_seconds, "stderr_bytes": len(stderr), "stderr_sha256": hashlib.sha256(stderr).hexdigest()}
        reported = json.loads(stdout)
        require(all(reported.get(key) == value for key, value in result.items()) and not reported.get("evidence_error"),
                "native capture stdout differs from retained metadata or failed to preserve run evidence")
        _, operation = operation_pair(runner, run_id, reported)
        require(operation.get("status") == ("failed" if process.returncode else "completed")
                and operation.get("verification") == "unverified" and len(operation.get("artifact_ids", [])) == 2,
                "native packet attempt did not retain its exact pending/completion outcome and two artifacts")
        artifacts = []
        expected_artifacts = {"pcap": output, "pcap_metadata": sidecar}
        for artifact_id in operation["artifact_ids"]:
            artifact, _, _ = runner.cli("evidence", "artifact", run_id, artifact_id)
            path = expected_artifacts.pop(artifact.get("kind"), None)
            require(path is not None and artifact.get("size") == path.stat().st_size
                    and artifact.get("sha256") == hashlib.sha256(path.read_bytes()).hexdigest()
                    and artifact.get("declared_coverage", {}).get("state") == result["coverage"]["state"],
                    "packet evidence artifact lost its file identity or capture coverage")
            verified, _, _ = runner.cli("evidence", "verify", run_id, artifact_id)
            require(verified.get("byte_integrity") == "verified" and verified.get("coverage") == "not_established_by_hash",
                    "packet evidence byte validation misreported observation coverage")
            artifacts.append(artifact)
        require(not expected_artifacts, "packet evidence omitted the PCAP or metadata artifact")
        progress["live"].update(evidence=reported["evidence"], evidence_operation=operation, evidence_artifacts=artifacts,
                                fixture_and_evidence_check_seconds=time.monotonic() - started)
        require(sidecar.stat().st_mode & 0o777 == 0o600 and output.stat().st_mode & 0o777 == 0o600, "native packet output privacy changed")
        if process.returncode:
            require(result.get("error_code") == "capture_permission_denied" and result.get("status") == "failed"
                    and result.get("coverage", {}).get("state") == "none" and not result.get("observation_started_at")
                    and result.get("artifact", {}).get("pcap_valid") is False and result.get("artifact", {}).get("bytes") == 0
                    and result.get("stats", {}).get("available") is False and output.stat().st_size == 0,
                    "native capture failed for an unexpected reason or misreported observation coverage: " + str(result.get("error")))
            progress["live"]["state"] = "unavailable"
            progress["live"]["reason"] = "BPF access denied; permissions and privileges were unchanged"
            return
        require(not errors and len(sent) == len(payloads) and received == sent, "owned UDP fixture did not establish expected sender/receiver bytes")
        raw = output.read_bytes()
        require(result["artifact"]["sha256"] == hashlib.sha256(raw).hexdigest() and result["artifact"]["bytes"] == len(raw), "native PCAP artifact identity differs from the saved file")
        endian = "<" if raw[:4] in (b"\xd4\xc3\xb2\xa1", b"\x4d\x3c\xb2\xa1") else ">"
        linktype = struct.unpack_from(endian + "I", raw, 20)[0]
        require(linktype in (0, 108), "live fixture expected a loopback link header")
        offset, observed = 24, []
        while offset < len(raw):
            _, _, captured_length, original_length = struct.unpack_from(endian + "IIII", raw, offset);offset += 16
            frame = raw[offset:offset + captured_length];offset += captured_length
            require(captured_length == original_length and len(frame) == captured_length, "live fixture packet was truncated")
            ipv4 = frame[4:];header = (ipv4[0] & 15) * 4
            require(ipv4[0] >> 4 == 4 and ipv4[9] == 17 and ipv4[12:20] == socket.inet_aton("127.0.0.1") * 2, "live capture included an unrelated network endpoint")
            udp = ipv4[header:];ports = struct.unpack_from("!HHH", udp)
            require(ports[:2] == (source_port, target_port), "live capture included an unrelated UDP port")
            payload = udp[8:ports[2]]
            require(payload in payloads, "live packet bytes differ from the synthetic sender")
            observed.append(payload)
        require(offset == len(raw) and all(payload in observed for payload in payloads), "live packet capture lost expected fixture messages")
        progress["live"].update(state="verified", packet_count=len(observed), duplicate_packets=len(observed) - len(payloads),
                                captured_payloads=[byte_identity(value) for value in observed])
    finally:
        source.close();target.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--packets-only", action="store_true")
    parser.add_argument("--media-only", action="store_true")
    parser.add_argument("--ffprobe", default=shutil.which("ffprobe") or "/opt/homebrew/bin/ffprobe")
    parser.add_argument("--ffmpeg", default=shutil.which("ffmpeg") or "/opt/homebrew/bin/ffmpeg")
    options = parser.parse_args()
    require(not (options.packets_only and options.media_only), "select at most one reduced verification mode")
    output = options.output.resolve()
    require(not output.exists(), "output already exists; choose a new report path")
    binary, host = (shutil.which(os.environ.get(key, fallback)) for key, fallback in (("REP_BINARY", "rep"), ("REP_HOST_BINARY", "rep-host")))
    require(binary and host, "REP_BINARY and REP_HOST_BINARY must name executables")
    extension = Path(os.environ.get("REP_EXTENSION_PATH", ROOT.parent / "rep")).resolve()
    report = {"schema": 1, "passed": False, "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
              "harness_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(), "fixture_sha256": hashlib.sha256(FIXTURE.read_bytes()).hexdigest(),
              "limits": "Synthetic local media is reencoded by Chromium MediaRecorder; it does not reproduce original codecs, encrypted RTP packets, or every source frame. Constructed offline PCAP and any live loopback capture are reported separately."}
    try:
        with tempfile.TemporaryDirectory(prefix="rep-native-capture-", dir="/tmp") as temporary:
            directory = Path(temporary)
            frozen = directory / "extension"
            shutil.copytree(extension, frozen, ignore=shutil.ignore_patterns(".git", "node_modules", ".env", ".env.*", "coverage", "*.zip", "*.crx"))
            digest = hashlib.sha256()
            for path in sorted([*frozen.rglob("*.js"), frozen / "manifest.json"]):
                digest.update(str(path.relative_to(frozen)).encode() + b"\0" + path.read_bytes())
            report["extension_js_sha256"] = digest.hexdigest()
            runner = Runner(Path(binary).resolve(), Path(host).resolve(), frozen, directory, "native-capture-verify-20260927")
            runner.base = [runner.binary, "--workspace", "native-capture-verify", "--task", runner.task]
            runner.owner = "native-capture-verify/" + runner.task
            report.update(task=runner.task, cli_sha256=hashlib.sha256(Path(binary).read_bytes()).hexdigest(), host_sha256=hashlib.sha256(Path(host).read_bytes()).hexdigest())
            server = None
            try:
                runner.cli("scope");runner.cli("summary", "--max-bytes", "4096")
                run, _, _ = runner.cli("evidence", "begin", "--intent", "Verify owned synthetic media intervals and bounded native packet artifacts", "--stop", "Stop after exact exports and local fixtures; release all owned media, browser, sockets and servers")
                runner.base.extend(["--run", run["id"]]);report["run_id"] = run["id"]
                if not options.media_only:
                    report["packets"] = {};packet_suite(runner, run["id"], report["packets"])
                if not options.packets_only:
                    report["browser_start_seconds"] = runner.start()
                    with urllib.request.urlopen(f"http://127.0.0.1:{runner.state['port']}/json/version", timeout=5) as response:
                        version = json.load(response)
                    report["chromium"] = {key: version.get(key) for key in ("Browser", "Protocol-Version", "V8-Version")}
                    scope, _, _ = runner.cli("scope")
                    require(Path(scope["data_dir"]).is_relative_to(directory), "fixture task data escaped its private directory")
                    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler);server.daemon_threads = True
                    threading.Thread(target=server.serve_forever, daemon=True).start()
                    report["media"] = []
                    for mode in ("full", "bounded"):
                        progress = {"mode": mode};report["media"].append(progress)
                        media_case(runner, f"http://127.0.0.1:{server.server_port}", Path(scope["data_dir"]), run["id"], mode, options.ffprobe, options.ffmpeg, progress)
                report["live_packet_capture_verified"] = report.get("packets", {}).get("live", {}).get("state") == "verified"
                report["passed"] = True
            finally:
                browser_started = runner.attempted_start
                try:
                    runner.stop();report["owned_browser_started"] = browser_started
                    if browser_started:
                        report["owned_browser_stopped"] = True
                finally:
                    if server:
                        server.shutdown();server.server_close()
    except Exception as error:
        report["passed"] = False;report["error"] = str(error)
    report["test_data_retention"] = "Temporary task archives, browser profile, media artifacts and packet fixtures are removed; this report retains hashes and validation results."
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"passed": report["passed"], "report": str(output), "error": report.get("error")}))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
