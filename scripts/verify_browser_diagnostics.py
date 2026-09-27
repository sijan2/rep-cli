#!/usr/bin/env python3
"""Verify owned native Chromium diagnostics against synthetic loopback traffic.

REP_BINARY=/tmp/rep-native/rep python3 scripts/verify_browser_diagnostics.py \
  --quic-python /path/to/private-aioquic-venv/bin/python --output /tmp/report.json

The selected QUIC interpreter needs aioquic; this interpreter needs
websocket-client. Native tshark and ffmpeg/ffprobe perform independent decoding.
No page collector, extension, proxy, trust-store change, camera or microphone.
"""
from __future__ import annotations

import argparse
import array
import base64
from collections import Counter, defaultdict
import hashlib
import http.server
import json
import math
import os
from pathlib import Path
import re
import select
import shutil
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
import urllib.request
import uuid
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[1]
MEDIA_FIXTURE = ROOT / "scripts/fixtures/native-media/index.html"
TRANSPORT_SCRIPT = ROOT / "scripts/verify_transport_capture.py"
PAYLOAD = b"rep-native-owned-quic-fixture-20260927\x00\xff\x80\x01" + bytes(range(64))


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def identity(data):
    return {"bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()}


def private_write(path, data):
    with path.open("xb") as destination:
        os.chmod(path, 0o600)
        destination.write(data)


def stop(process):
    if process is None or process.poll() is not None:
        return
    process.send_signal(signal.SIGINT)
    try:
        process.wait(timeout=12)
    except subprocess.TimeoutExpired:
        process.terminate()
        try:
            process.wait(timeout=4)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=4)


def read_json_url(url):
    with urllib.request.urlopen(url, timeout=2) as response:
        data = response.read(1024 * 1024 + 1)
    require(len(data) <= 1024 * 1024, "owned CDP HTTP response exceeds fixture bound")
    return json.loads(data)


def cdp_call(endpoint, method, params):
    import websocket

    parsed = urllib.parse.urlparse(endpoint)
    require(parsed.scheme == "ws" and parsed.hostname in ("127.0.0.1", "localhost"), "CDP must belong to the loopback fixture")
    connection = websocket.create_connection(endpoint, timeout=15, suppress_origin=True)
    try:
        connection.send(json.dumps({"id": 1, "method": method, "params": params}))
        for _ in range(100):
            data = connection.recv()
            require(len(data) <= 1024 * 1024, "owned CDP response exceeds fixture bound")
            message = json.loads(data)
            if message.get("id") != 1:
                continue
            require("error" not in message, "owned CDP call failed: " + str(message.get("error")))
            return message.get("result", {})
        raise AssertionError("owned CDP call produced too many unrelated messages")
    finally:
        connection.close()


def evaluate(endpoint, expression):
    result = cdp_call(endpoint, "Runtime.evaluate", {"expression": expression, "awaitPromise": True, "returnByValue": True, "userGesture": True})
    require(not result.get("exceptionDetails"), "fixture JavaScript failed: " + str(result.get("exceptionDetails")))
    return result.get("result", {}).get("value")


def fixture_page(quic):
    config = {**quic, "url": quic["url"] + "?case=native-diagnostics&mode=closed", "payload": list(PAYLOAD)}
    script = """
<script>
const nativeFixtureConfig = __CONFIG__;
window.runNativeTransport = async () => {
  const config=nativeFixtureConfig;
  const transport=new WebTransport(config.url,{serverCertificateHashes:[{algorithm:'sha-256',value:new Uint8Array(config.certificate_hash)}]});
  await bounded(transport.ready,'native QUIC ready');
  const stream=await transport.createBidirectionalStream(), writer=stream.writable.getWriter();
  await writer.write(new Uint8Array(config.payload));await writer.close();
  const reader=stream.readable.getReader(), received=[];
  while(true){const chunk=await bounded(reader.read(),'native QUIC echo');if(chunk.done)break;received.push(...chunk.value);if(received.length>1024)throw new Error('oversized echo');}
  if(received.length!==config.payload.length||received.some((value,index)=>value!==config.payload[index]))throw new Error('QUIC echo differs');
  const sha=Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',new Uint8Array(received)))).map(value=>value.toString(16).padStart(2,'0')).join('');
  transport.close();await bounded(transport.closed,'native QUIC close');
  return {bytes:received.length,sha256:sha,protocol:'WebTransport over QUIC',closed:true};
};
window.nativeFixtureMetadata = async () => ({
  browser_observations:{webdriver:navigator.webdriver,user_agent:navigator.userAgent},
  offer:current.left.localDescription.sdp,answer:current.right.localDescription.sdp,
  codecs:Array.from((await current.left.getStats()).values()).filter(row=>row.type==='codec').map(row=>({payload_type:row.payloadType,mime_type:row.mimeType,clock_rate:row.clockRate,channels:row.channels,fmtp:row.sdpFmtpLine}))
});
</script>
""".replace("__CONFIG__", json.dumps(config))
    return MEDIA_FIXTURE.read_bytes() + script.encode()


class FixtureHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        data = self.server.page if urllib.parse.urlparse(self.path).path == "/native" else b""
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        try:
            self.wfile.write(data)
        except (BrokenPipeError, ConnectionResetError):
            pass


def find_artifact(manifest, directory, name):
    path = directory / name
    require(path.is_file(), "native capture did not retain " + name)
    require(path.stat().st_mode & 0o077 == 0, name + " is accessible outside the current user")
    data = path.read_bytes()
    matches = [item for item in manifest.get("artifacts", []) if Path(item.get("path", "")).name == name]
    require(len(matches) == 1, "manifest does not identify exactly one " + name)
    require(matches[0].get("bytes") == len(data) and matches[0].get("sha256") == hashlib.sha256(data).hexdigest(), "manifest artifact hash/length mismatch for " + name)
    return path


def verify_cleanup(manifest, bundle):
    require(bundle.stat().st_mode & 0o077 == 0, "native bundle directory is accessible outside current user")
    require(manifest.get("browser", {}).get("profile_removed") and not (bundle / "profile").exists(), "owned browser profile was not removed")
    require(not (bundle / "ready.json").exists(), "stale live readiness metadata remains after browser shutdown")
    for path in bundle.iterdir():
        require(path.is_file() and path.stat().st_mode & 0o077 == 0, "native retained artifact has unsafe type or permissions")
    pid, port = manifest.get("browser", {}).get("pid"), manifest.get("browser", {}).get("port")
    if pid:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            pass
        else:
            raise AssertionError("owned browser process remains after capture returned")
    if port:
        with socket.socket() as check:
            check.settimeout(1)
            require(check.connect_ex(("127.0.0.1", port)) != 0, "owned browser debugging endpoint remains available")
    return {"profile_removed": True, "ready_file_removed": True, "browser_process_stopped": True, "cdp_endpoint_closed": True, "private_files_and_directory": True}


def verify_termination_cases(args, rep, env, directory):
    results = []
    for mode in ("byte_limit", "cancelled"):
        bundle = directory / mode
        command = [rep, "browser", "native-capture", "about:blank", "--output", str(bundle), "--duration", "30s", "--webrtc-rtp", "--headless"]
        if args.browser_binary:
            command += ["--binary", args.browser_binary]
        if mode == "byte_limit":
            command += ["--max-log-bytes", "1024"]
        process = subprocess.Popen(command, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            if mode == "cancelled":
                deadline = time.monotonic() + 20
                while not (bundle / "ready.json").exists() and time.monotonic() < deadline:
                    require(process.poll() is None, "cancellation fixture exited before readiness")
                    time.sleep(0.05)
                require((bundle / "ready.json").exists(), "cancellation fixture did not become ready")
                process.send_signal(signal.SIGINT)
            stdout, stderr = process.communicate(timeout=25)
            require(len(stdout) <= 1024 * 1024, "termination fixture response exceeds bound")
            require((bundle / "manifest.json").exists(), "termination did not finalize a manifest: " + stderr.decode(errors="replace")[:1000])
            manifest = json.loads((bundle / "manifest.json").read_text())
            if mode == "byte_limit":
                require(manifest.get("stop_reason") == "log_byte_limit" and manifest.get("status") == "partial", "log bound did not produce an explicit partial capture")
                require(manifest.get("log", {}).get("retained_bytes") == 1024 and manifest.get("log", {}).get("dropped_bytes", 0) > 0 and (bundle / "browser.log").stat().st_size == 1024, "diagnostic log byte bound is not exact")
            else:
                require(process.returncode != 0 and manifest.get("status") == "cancelled" and manifest.get("stop_reason") == "cancelled", "SIGINT did not produce explicit cancellation")
            results.append({"case": mode, "status": manifest["status"], "stop_reason": manifest["stop_reason"], "exit_code": process.returncode,
                            "log": manifest.get("log"), "cleanup": verify_cleanup(manifest, bundle)})
        finally:
            stop(process)
    return results


def verify_quic(tshark, pcap, keys, port):
    common = [tshark, "-n", "-r", str(pcap), "-d", "udp.port==%d,quic" % port, "-T", "pdml"]

    def decoded_fields(use_keys):
        command = common + (["-o", "tls.keylog_file:" + str(keys)] if use_keys else [])
        result = subprocess.run(command, capture_output=True, timeout=30)
        require(result.returncode == 0, "native tshark QUIC decoding failed: " + result.stderr.decode(errors="replace")[:1024])
        require(len(result.stdout) <= 16 * 1024 * 1024, "fixture tshark output exceeds bound")
        packets = ET.fromstring(result.stdout)
        hits, numbers, decrypted = 0, set(), 0
        for packet in packets.findall("packet"):
            packet_number = None
            for field in packet.iter("field"):
                if field.get("name") == "frame.number":
                    packet_number = field.get("show")
                value = field.get("value", "")
                if field.get("name") == "quic.stream_data":
                    decrypted += 1
                if len(value) % 2 == 0 and re.fullmatch("[0-9a-fA-F]+", value):
                    raw = bytes.fromhex(value)
                    if PAYLOAD in raw:
                        hits += 1
                        numbers.add(packet_number)
        return {"payload_field_matches": hits, "matching_packets": len(numbers), "decrypted_stream_fields": decrypted}

    without_keys, with_keys = decoded_fields(False), decoded_fields(True)
    require(without_keys["matching_packets"] == 0, "QUIC payload unexpectedly visible without diagnostic secrets")
    require(with_keys["matching_packets"] >= 2, "key log did not decrypt both original QUIC payload and echoed reply")
    require(PAYLOAD not in pcap.read_bytes(), "synthetic plaintext unexpectedly appears in wire artifact")
    key_lines = keys.read_text().splitlines()
    labels = Counter(line.split()[0] for line in key_lines if line.strip() and not line.startswith("#"))
    require(labels.get("CLIENT_TRAFFIC_SECRET_0", 0) > 0 and labels.get("SERVER_TRAFFIC_SECRET_0", 0) > 0, "Chromium key log omitted QUIC application traffic secrets")
    return {"plaintext": identity(PAYLOAD), "without_keys": without_keys, "with_keys": with_keys, "secret_labels_only": dict(labels),
            "wire_ciphertext_preserved": True, "decryption_tool": "tshark", "certificate_validation": "WebTransport serverCertificateHashes; temporary ECDSA certificate"}


def rtp_payload(packet):
    require(len(packet) >= 12 and packet[0] >> 6 == 2, "invalid native RTP header")
    offset = 12 + (packet[0] & 15) * 4
    if packet[0] & 16:
        require(len(packet) >= offset + 4, "short native RTP extension")
        offset += 4 + int.from_bytes(packet[offset + 2:offset + 4], "big") * 4
    padding = packet[-1] if packet[0] & 32 else 0
    require(offset <= len(packet) - padding and (not packet[0] & 32 or padding > 0), "invalid native RTP extension or padding length")
    return packet[offset:len(packet) - padding if padding else len(packet)]


def ogg_page(packet, serial, sequence, granule, flags):
    segments = [255] * (len(packet) // 255) + [len(packet) % 255]
    require(len(segments) <= 255, "fixture Opus packet exceeds one Ogg page")
    page = bytearray(b"OggS\x00" + bytes([flags]) + struct.pack("<QII", granule, serial, sequence) + b"\x00" * 4 + bytes([len(segments)]) + bytes(segments) + packet)
    crc = 0
    for byte in page:
        crc ^= byte << 24
        for _ in range(8):
            crc = ((crc << 1) ^ (0x04C11DB7 if crc & 0x80000000 else 0)) & 0xffffffff
    struct.pack_into("<I", page, 22, crc)
    return bytes(page)


def opus_samples(packet):
    require(packet, "empty fixture Opus packet")
    config, code = packet[0] >> 3, packet[0] & 3
    frame = [480, 960, 1920, 2880][config & 3] if config < 12 else ([480, 960][config & 1] if config < 16 else [120, 240, 480, 960][config & 3])
    require(code != 3 or len(packet) > 1, "short fixture Opus frame count")
    count = 1 if code == 0 else (2 if code in (1, 2) else packet[1] & 63)
    require(0 < frame * count <= 5760, "invalid fixture Opus duration")
    return frame * count


def vp8_payload(payload, is_red):
    if is_red:
        offset, redundant = 0, 0
        while offset < len(payload) and payload[offset] & 128:
            require(offset + 4 <= len(payload), "short RED header")
            redundant += ((payload[offset + 2] & 3) << 8) | payload[offset + 3]
            offset += 4
        require(offset < len(payload), "missing primary RED header")
        primary = payload[offset] & 127
        payload = payload[offset + 1 + redundant:]
        require(primary == 96, "fixture RED primary codec changed from VP8")
    require(payload, "empty VP8 descriptor")
    first, offset = payload[0], 1
    if first & 128:
        require(offset < len(payload), "short VP8 extension")
        flags = payload[offset]
        offset += 1
        if flags & 128:
            require(offset < len(payload), "short VP8 picture ID")
            offset += 2 if payload[offset] & 128 else 1
        if flags & 64:
            offset += 1
        if flags & 48:
            offset += 1
    require(offset < len(payload), "VP8 descriptor has no encoded frame bytes")
    return payload[offset:], bool(first & 16 and first & 15 == 0)


def decode_original_media(records, codecs, directory, ffmpeg, ffprobe):
    codec_map = {item["payload_type"]: item["mime_type"].lower() for item in codecs}
    selected = [item for item in records if item["direction"] == "incoming" and item["packet_kind"] == "rtp"]
    audio, video = [], []
    audio_ssrc, video_ssrc = set(), set()
    for row in selected:
        raw = base64.b64decode(row["data_base64"], validate=True)
        payload = rtp_payload(raw)
        if not payload:
            continue
        codec = codec_map.get(raw[1] & 127, "")
        if codec == "audio/opus":
            audio.append((int.from_bytes(raw[2:4], "big"), payload))
            audio_ssrc.add(raw[8:12])
        elif codec in ("video/vp8", "video/red"):
            encoded, starts = vp8_payload(payload, codec == "video/red")
            video.append((int.from_bytes(raw[2:4], "big"), int.from_bytes(raw[4:8], "big"), bool(raw[1] & 128), starts, encoded))
            video_ssrc.add(raw[8:12])
    require(len(audio_ssrc) == 1 and len(video_ssrc) == 1 and len(audio) > 50 and video, "fixture did not retain one original Opus and VP8 stream")
    require(all((right[0] - left[0]) % 65536 == 1 for left, right in zip(audio, audio[1:])), "native Opus stream has sequence gaps")
    pages = [ogg_page(b"OpusHead" + struct.pack("<BBHIhB", 1, 2, 0, 48000, 0, 0), 1, 0, 0, 2),
             ogg_page(b"OpusTags" + struct.pack("<I", 3) + b"Rep" + struct.pack("<I", 0), 1, 1, 0, 0)]
    granule = 0
    for index, (_, payload) in enumerate(audio):
        granule += opus_samples(payload)
        pages.append(ogg_page(payload, 1, index + 2, granule, 4 if index == len(audio) - 1 else 0))
    audio_path = directory / "original-opus.ogg"
    private_write(audio_path, b"".join(pages))
    frames, pending, previous_sequence, timestamp = [], [], None, None
    for sequence, stamp, marker, starts, encoded in video:
        if starts:
            require(not pending, "new VP8 frame began before previous marker")
            pending, timestamp = [], stamp
        else:
            require(pending and timestamp == stamp and (sequence - previous_sequence) % 65536 == 1, "native VP8 frame has a missing or reordered packet")
        pending.append(encoded)
        previous_sequence = sequence
        if marker:
            frames.append((timestamp, b"".join(pending)))
            pending = []
    require(not pending and len(frames) > 5 and not frames[0][1][0] & 1, "native VP8 recording does not contain complete frames beginning with a keyframe")
    first_stamp = frames[0][0]
    ivf = struct.pack("<4sHH4sHHIIII", b"DKIF", 0, 32, b"VP80", 320, 180, 90000, 1, len(frames), 0)
    ivf += b"".join(struct.pack("<IQ", len(frame), (stamp - first_stamp) % 2**32) + frame for stamp, frame in frames)
    video_path = directory / "original-vp8.ivf"
    private_write(video_path, ivf)
    result = {"collector_reencoded": False, "opus_packets": len(audio), "vp8_encoded_frames": len(frames), "depacketization": "strip RTP/RED/VP8 framing, preserve encoded bytes; add Ogg/IVF container headers", "streams": []}
    for kind, path in (("audio", audio_path), ("video", video_path)):
        probe = subprocess.run([ffprobe, "-v", "error", "-count_frames", "-show_streams", "-of", "json", str(path)], capture_output=True, timeout=20)
        require(probe.returncode == 0, "ffprobe failed to read original " + kind)
        streams = json.loads(probe.stdout).get("streams", [])
        require(len(streams) == 1 and int(streams[0].get("nb_read_frames", 0)) > 0, "original encoded media contains no decoded frames")
        stream = streams[0]
        command = [ffmpeg, "-v", "error", "-i", str(path)]
        command += ["-ac", "1", "-ar", "48000", "-f", "f32le", "-"] if kind == "audio" else ["-fps_mode", "passthrough", "-c:v", "rawvideo", "-pix_fmt", "yuv420p", "-f", "rawvideo", "-"]
        decoded = subprocess.run(command, capture_output=True, timeout=20)
        require(decoded.returncode == 0 and not decoded.stderr.strip(), "native encoded " + kind + " failed full decoder verification: " + decoded.stderr.decode(errors="replace")[:1000])
        require(0 < len(decoded.stdout) < 32 * 1024 * 1024, "decoded original media exceeds fixture byte bound")
        entry = {"kind": kind, "codec": stream.get("codec_name"), "decoded_frames": int(stream["nb_read_frames"]), "container": identity(path.read_bytes()), "decoded": identity(decoded.stdout)}
        if kind == "audio":
            samples = array.array("f")
            samples.frombytes(decoded.stdout)
            if sys.byteorder != "little":
                samples.byteswap()
            rms = math.sqrt(sum(sample * sample for sample in samples) / len(samples))
            require(rms > 0.001, "original Opus decodes to silence")
            tone = samples[4800:-4800]
            require(len(tone) > 4800, "too little decoded original Opus for oscillator verification")
            frequency = sum(left <= 0 < right for left, right in zip(tone, tone[1:])) * 48000 / (len(tone) - 1)
            require(430 <= frequency <= 450, "original Opus lost expected 440 Hz oscillator")
            entry.update(pcm_rms=rms, oscillator_hz=frequency)
        else:
            require(stream.get("width") == 320 and stream.get("height") == 180, "original VP8 dimensions differ")
            size = 320 * 180 * 3 // 2
            require(len(decoded.stdout) == size * len(frames) and decoded.stdout[:size] != decoded.stdout[-size:], "original VP8 frame count or moving pixels differ")
        result["streams"].append(entry)
    return result


def negotiated_codecs(metadata):
    codecs = {item["payload_type"]: item for item in metadata["codecs"]}
    kind = ""
    for line in metadata["answer"].splitlines():
        if line.startswith("m="):
            kind = line[2:].split()[0]
        matched = re.fullmatch(r"a=rtpmap:(\d+) ([^/]+)/([0-9]+)(?:/([0-9]+))?", line)
        if matched and kind in ("audio", "video"):
            payload_type = int(matched[1])
            codecs[payload_type] = {"payload_type": payload_type, "mime_type": kind + "/" + matched[2], "clock_rate": int(matched[3])}
            if matched[4]:
                codecs[payload_type]["channels"] = int(matched[4])
    return list(codecs.values())


def verify_rtp(raw_path, packets_path, codecs, directory, ffmpeg, ffprobe):
    raw_records = []
    pattern = re.compile(r"^([IO]) ([0-9:.]+) 0000 ((?:[0-9A-Fa-f]{2} )+)# RTP_DUMP\s*$")
    for line in raw_path.read_text(errors="replace").splitlines():
        if "# RTP_DUMP" not in line:
            continue
        matched = pattern.match(line)
        require(matched, "native raw RTP diagnostic line was truncated or malformed")
        packet = bytes.fromhex(matched[3])
        raw_records.append(("incoming" if matched[1] == "I" else "outgoing", packet))
    records = [json.loads(line) for line in packets_path.read_text().splitlines() if line.strip()]
    require(len(records) == len(raw_records) and len(records) > 200, "native RTP parser omitted raw logged datagrams")
    directions = defaultdict(Counter)
    kinds = Counter()
    for index, (record, (direction, raw)) in enumerate(zip(records, raw_records)):
        require(record.get("ordinal") == index + 1 and record.get("direction") == direction, "native RTP ordinal/direction differs from browser log")
        require(base64.b64decode(record["data_base64"], validate=True) == raw and record.get("packet_bytes") == len(raw) and record.get("packet_sha256") == hashlib.sha256(raw).hexdigest(), "native RTP parser altered original packet bytes")
        kind = "rtcp" if 192 <= raw[1] <= 223 else "rtp"
        require(record.get("packet_kind") == kind, "native RTP/RTCP classification differs")
        directions[direction][hashlib.sha256(raw).hexdigest()] += 1
        kinds[kind] += 1
    require(directions["incoming"] == directions["outgoing"], "native outgoing/incoming original packet multisets differ in local fixture")
    return {"records": len(records), "incoming": sum(directions["incoming"].values()), "outgoing": sum(directions["outgoing"].values()), "packet_kinds": dict(kinds),
            "raw_log_to_jsonl_all_bytes_identical": True, "outgoing_incoming_all_bytes_identical": True,
            "decoded_original_media": decode_original_media(records, codecs, directory, ffmpeg, ffprobe)}


def run(args, report):
    rep = str(Path(args.rep_binary).expanduser().resolve())
    tshark = args.tshark or shutil.which("tshark") or "/Applications/Wireshark.app/Contents/MacOS/tshark"
    ffmpeg, ffprobe = shutil.which("ffmpeg"), shutil.which("ffprobe")
    require(Path(rep).is_file() and Path(tshark).is_file() and ffmpeg and ffprobe, "rep, tshark, ffmpeg and ffprobe are required")
    report["rep_binary"] = {"path": rep, **identity(Path(rep).read_bytes())}
    source_paths = [Path(__file__), MEDIA_FIXTURE, TRANSPORT_SCRIPT, ROOT / "cmd/browser_native_capture.go"]
    source_paths.extend(sorted((ROOT / "internal/browserdiagnostics").glob("*.go")))
    source_paths.extend(sorted((ROOT / "internal/packetcapture").glob("*")))
    report["source_identities"] = [{"path": str(path.relative_to(ROOT)), **identity(path.read_bytes())} for path in source_paths if path.is_file()]
    report["tools"] = {}
    for name, command in (("tshark", [tshark, "--version"]), ("ffmpeg", [ffmpeg, "-version"]), ("ffprobe", [ffprobe, "-version"])):
        version = subprocess.run(command, capture_output=True, text=True, timeout=10)
        require(version.returncode == 0, name + " version check failed")
        report["tools"][name] = version.stdout.splitlines()[0]
    directory = Path(tempfile.mkdtemp(prefix="rep-native-browser-validation-"))
    report.update(directory=str(directory), browser_collectors={"extension": False, "protocol_payloads": False, "media_recorder": False}, fixture="owned loopback synthetic WebTransport and oscillator/canvas WebRTC")
    env = os.environ.copy()
    env.update(XDG_DATA_HOME=str(directory / "data"), XDG_CONFIG_HOME=str(directory / "config"), REP_WORKSPACE="native-diagnostics-validation", REP_TASK="verify-" + uuid.uuid4().hex[:12])
    for key in ("REPLIVE_PATH", "REPANDROID_PATH", "SSLKEYLOGFILE"):
        env.pop(key, None)
    for command in (("scope", "-j"), ("summary", "--max-bytes", "4096")):
        scoped = subprocess.run([rep, *command], env=env, capture_output=True, timeout=15)
        require(scoped.returncode == 0, "isolated fixture CLI scope setup failed")
    quic, capture, server = None, None, None
    quic_stderr = (directory / "quic.stderr").open("xb")
    os.chmod(quic_stderr.name, 0o600)
    try:
        quic_dir = directory / "quic"
        quic = subprocess.Popen([args.quic_python, str(TRANSPORT_SCRIPT), "--serve-quic", str(quic_dir)], stdout=subprocess.PIPE, stderr=quic_stderr)
        require(select.select([quic.stdout], [], [], 20)[0], "local QUIC fixture did not become ready")
        line = quic.stdout.readline(65537)
        require(len(line) <= 65536 and line, "local QUIC fixture did not return bounded readiness")
        config = json.loads(line)
        port = urllib.parse.urlparse(config["url"]).port
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), FixtureHandler)
        server.daemon_threads = True
        server.page = fixture_page(config)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        origin = "http://127.0.0.1:%d/native" % server.server_port
        bundle = directory / "capture"
        command = [rep, "browser", "native-capture", origin, "--output", str(bundle), "--duration", "15s", "--tls-keys", "--webrtc-rtp", "--interface", "lo0", "--filter", "udp port %d and host 127.0.0.1" % port, "--headless"]
        if args.browser_binary:
            command += ["--binary", args.browser_binary]
        capture = subprocess.Popen(command, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        deadline, ready, endpoint, loaded = time.monotonic() + 25, None, None, False
        while time.monotonic() < deadline:
            require(capture.poll() is None, "native capture exited before fixture readiness")
            if (bundle / "ready.json").exists():
                ready = json.loads((bundle / "ready.json").read_text())
                targets = read_json_url("http://127.0.0.1:%d/json/list" % ready["port"])
                target = next((item for item in targets if item.get("type") == "page" and item.get("url") == origin), None)
                if target:
                    endpoint = target["webSocketDebuggerUrl"]
                    if evaluate(endpoint, "typeof window.runNativeTransport === 'function' && typeof window.startMediaFixture === 'function'"):
                        loaded = True
                        break
            time.sleep(0.05)
        require(loaded, "native fixture page did not become available")
        report["browser_ready"] = {key: ready.get(key) for key in ("version", "pid", "port", "ready_at")}
        started = evaluate(endpoint, "Promise.all([window.startMediaFixture(), window.runNativeTransport()])")
        require(started[1]["sha256"] == identity(PAYLOAD)["sha256"], "browser received wrong QUIC echo")
        metadata = evaluate(endpoint, "window.nativeFixtureMetadata()")
        private_write(directory / "fixture-sdp.json", json.dumps(metadata).encode())
        report["codecs"] = negotiated_codecs(metadata)
        report["browser_observations"] = metadata["browser_observations"]
        time.sleep(3)
        stats = evaluate(endpoint, "window.fixtureStats()")
        require(stats["frames_decoded"] > 5 and stats["received_audio_rms"] > 0.001 and stats["left_state"] == "connected" and stats["right_state"] == "connected", "fixture did not decode flowing synthetic media")
        report["fixture_stats"] = stats
        evaluate(endpoint, "window.stopMediaFixture()")
        stdout, stderr = capture.communicate(timeout=30)
        require(capture.returncode == 0, "native capture failed: " + stderr.decode(errors="replace")[:2000])
        require(len(stdout) <= 1024 * 1024, "native capture CLI response exceeds fixture bound")
        manifest = json.loads((bundle / "manifest.json").read_text())
        require(manifest.get("status") == "completed" and manifest.get("stop_reason") == "duration", "native browser did not finalize its complete requested interval")
        require(manifest.get("log", {}).get("dropped_bytes") == 0 and manifest.get("key_log", {}).get("discarded_bytes") == 0 and not manifest.get("rtp", {}).get("truncated"), "native diagnostics reached a byte bound during the fixture")
        require(manifest.get("packet_capture", {}).get("truncated_packets") == 0, "wire fixture retained truncated packets")
        report["capture"] = {key: manifest.get(key) for key in ("version", "status", "stop_reason", "browser", "duration_ms", "packet_capture", "rtp", "limitations")}
        report["artifacts"] = manifest["artifacts"]
        report["cleanup"] = verify_cleanup(manifest, bundle)
        wire = find_artifact(manifest, bundle, "wire.pcap")
        keys = find_artifact(manifest, bundle, "tls.keys")
        raw_log = find_artifact(manifest, bundle, "browser.log")
        rtp = find_artifact(manifest, bundle, "webrtc-rtp.jsonl")
        report["quic"] = verify_quic(tshark, wire, keys, port)
        report["native_rtp"] = verify_rtp(raw_log, rtp, report["codecs"], directory, ffmpeg, ffprobe)
        server_stats = json.loads((quic_dir / "stats.json").read_text())
        require(not server_stats["errors"] and any(row.get("sha256") == identity(PAYLOAD)["sha256"] for row in server_stats["observations"]), "independent QUIC server did not confirm synthetic original bytes")
        report["server"] = server_stats
        report["termination_cases"] = verify_termination_cases(args, rep, env, directory)
        report["passed"] = True
    finally:
        stop(capture)
        stop(quic)
        quic_stderr.close()
        if server is not None:
            server.shutdown()
            server.server_close()
        report["owned_fixture_processes_stopped"] = all(process is None or process.poll() is not None for process in (capture, quic))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rep-binary", default=os.environ.get("REP_BINARY", shutil.which("rep") or "rep"))
    parser.add_argument("--browser-binary")
    parser.add_argument("--quic-python", required=True)
    parser.add_argument("--tshark")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    report = {"schema": 1, "passed": False, "verified_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}
    try:
        run(args, report)
    except Exception as error:
        report["error"] = str(error)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n")
    args.output.chmod(0o600)
    print(json.dumps({"passed": report["passed"], "report": str(args.output), "error": report.get("error")}))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
