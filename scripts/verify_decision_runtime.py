#!/usr/bin/env python3
"""Exercise the decision runtime in a uniquely owned private Chromium profile.

REP_BINARY=/tmp/rep-runtime-new/rep REP_HOST_BINARY=/tmp/rep-runtime-new/rep-host \
  python3 scripts/verify_decision_runtime.py --output /tmp/runtime-results.json

--with-jev runs modest labeled real-provider comparisons; it never prints keys.
--baseline adds the preserved older binaries/extension as a separate browser arm.
Only loopback fixtures and processes created by this invocation are controlled.
"""
from __future__ import annotations

import argparse
import hashlib
import http.server
import json
import math
import os
from pathlib import Path
import random
import re
import shutil
import signal
import socket
import statistics
import subprocess
import tempfile
import threading
import time
import uuid
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
FIXTURES = Path(__file__).resolve().parent / "fixtures" / "decision-runtime"


def require(value, message):
    if not value:
        raise RuntimeError(message)


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        filename = "frame.html" if self.path.startswith("/frame.html") else "index.html"
        payload = (FIXTURES / filename).read_text()
        port = self.server.server_port
        payload = payload.replace("__TOP__", f"http://127.0.0.1:{port}").replace("__REMOTE__", f"http://localhost:{port}")
        if self.path.startswith("/selection"):
            payload = re.sub(r'<section aria-label="Frame controls">.*?</section>', "", payload)
        encoded = payload.encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(encoded)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        try:
            self.wfile.write(encoded)
        except (BrokenPipeError, ConnectionResetError):
            pass


class Runner:
    def __init__(self, binary, host, extension, directory, arm):
        self.directory = directory
        self.arm = arm
        self.binary, self.host, self.extension = map(str, (binary, host, extension))
        self.env = os.environ.copy()
        for key in ("REP_WORKSPACE", "REP_TASK", "REP_BRIDGE_DIR", "REPLIVE_PATH", "REPANDROID_PATH"):
            self.env.pop(key, None)
        self.env["XDG_DATA_HOME"] = str(directory / "data")
        self.env["NO_COLOR"] = "1"
        self.task = f"{arm}-{uuid.uuid4().hex[:10]}"
        self.base = [self.binary, "--workspace", "decision-runtime-bench", "--task", self.task]
        self.owner = "decision-runtime-bench/" + self.task
        self.state = None
        self.attempted_start = False
        self.last_error = None

    def cli(self, *args, success=True, timeout=90):
        started = time.perf_counter()
        result = subprocess.run(self.base + list(args) + ["--raw-json", "-j"], cwd=self.directory,
                                env=self.env, capture_output=True, timeout=timeout)
        elapsed = time.perf_counter() - started
        try:
            value, _ = json.JSONDecoder().raw_decode(result.stdout.decode().lstrip())
        except (ValueError, UnicodeError):
            value = None
        self.last_error = None
        if result.returncode:
            # Do not retain arbitrary provider/CLI text: fixed diagnostics are
            # enough to classify failures without risking credential output.
            stderr = result.stderr.decode(errors="replace")
            known = ["Jev DOM selection failed", "Jev returned an invalid DOM decision",
                     "Jev returned an invalid typed decision", "Jev model version changed",
                     "Jev state plus a question exceeds", "Jev request is invalid or exceeds",
                     "Jev request canceled or timed out", "Jev connection failed",
                     "Jev key is not configured", "Jev retry delay exceeds request deadline",
                     "context deadline exceeded", "frame_unavailable", "ambiguous_frame",
                     "stale_document", "stale_binding", "invalid_lease", "tab_busy"]
            classification = [message for message in known if message in stderr]
            classification += re.findall(r"Jev API returned HTTP \d{3}", stderr)
            error_code = (value.get("error") or {}).get("code") if isinstance(value, dict) and isinstance(value.get("error"), dict) else None
            self.last_error = {"exit_code": result.returncode, "code": error_code,
                               "classification": classification, "stderr_bytes": len(result.stderr),
                               "stderr_sha256": hashlib.sha256(result.stderr).hexdigest()}
        if success:
            require(result.returncode == 0 and value is not None,
                    f"{self.arm}: {' '.join(args[:2])} failed: {json.dumps(self.last_error)}")
        return value, elapsed, result.returncode

    def start(self):
        self.cli("scope")
        self.cli("summary", "--max-bytes", "4096")
        self.attempted_start = True
        self.state, elapsed, _ = self.cli("browser", "headless", "start", "--extension", self.extension,
                                        "--host", self.host)
        require(self.state.get("running") and self.state.get("connected"), "owned browser failed to connect")
        return elapsed

    def stop(self):
        if not self.attempted_start:
            return
        state, _, code = self.cli("browser", "headless", "stop", success=False)
        require(code == 0 and state and not state.get("running"), "owned browser cleanup failed")
        if self.state:
            bridge = Path(self.state["bridge_dir"])
            if bridge.name.startswith(f"rep-headless-{os.getuid()}-") and bridge.resolve().parent == Path("/tmp").resolve():
                shutil.rmtree(bridge, ignore_errors=True)
        self.attempted_start = False

    def registry(self):
        require(self.state is not None, "no owned browser state")
        matches = []
        for path in Path(self.state["bridge_dir"]).glob("*.json"):
            try:
                value = json.loads(path.read_text())
            except (OSError, ValueError):
                continue
            if value.get("parent_pid") == self.state["pid"] and value.get("extension_id") == self.state["extension_id"]:
                try:
                    os.kill(value["pid"], 0)
                except (KeyError, ProcessLookupError):
                    continue
                matches.append(value)
        require(len(matches) == 1, "owned bridge registry is missing or ambiguous")
        return matches[0]

    def rpc(self, method, params=None, success=True, timeout=35):
        registry = self.registry()
        request = {"id": uuid.uuid4().hex, "method": method, "params": params or {}, "timeout_ms": int(timeout * 1000)}
        started = time.perf_counter()
        with socket.socket(socket.AF_UNIX) as client:
            client.settimeout(timeout + 2)
            client.connect(registry["socket"])
            client.sendall(json.dumps(request).encode() + b"\n")
            with client.makefile("rb") as stream:
                response = json.loads(stream.readline(8 * 1024 * 1024))
        elapsed = time.perf_counter() - started
        require(response.get("id") == request["id"], "RPC response id mismatch")
        if success:
            require(not response.get("error"), f"{method}: {(response.get('error') or {}).get('code', 'rpc_error')}")
        return response.get("result"), elapsed, response.get("error")

    def restart_owned_host(self):
        registry = self.registry()
        parent = subprocess.run(["ps", "-o", "ppid=", "-p", str(registry["pid"])], capture_output=True, timeout=5)
        require(parent.returncode == 0 and int(parent.stdout.strip()) == self.state["pid"], "host ownership could not be verified immediately before restart")
        os.kill(registry["pid"], signal.SIGTERM)
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            try:
                current = self.registry()
                if current["pid"] != registry["pid"]:
                    self.rpc("bridge.ping")
                    return
            except (RuntimeError, OSError, ValueError):
                pass
            time.sleep(.1)
        raise RuntimeError("owned native host did not reconnect")

    def open(self, url):
        value, elapsed, _ = self.cli("browser", "open", url, "--browser", "headless", "--keep-tab", "--idle", "100ms")
        return value["tab_id"], elapsed

    def evaluate(self, tab, expression):
        value, elapsed, _ = self.cli("browser", "eval", expression, "--tab", str(tab), "--browser", "headless")
        wire = value.get("result", {})
        require(not wire.get("exceptionDetails"), "independent fixture evaluation raised")
        return wire.get("result", {}).get("value"), elapsed

    def plan(self, tab, url, steps, success=True):
        path = self.directory / "plan.json"
        path.write_text(json.dumps({"version": 1, "url": url, "steps": steps}))
        return self.cli("browser", "interact", str(path), "--tab", str(tab), "--browser", "headless", "--apply", success=success)


def percentile(values, quantile):
    if not values:
        return None
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, max(0, math.ceil(len(ordered) * quantile) - 1))]


def local_suite(runner, origin, progress):
    progress["passed"] = False
    url = origin + "/"
    tab, navigation = runner.open(url)
    ready, _ = runner.evaluate(tab, "new Promise((resolve,reject)=>{const start=Date.now();const timer=setInterval(()=>{if(['local','remote','deep'].every(k=>readyFrames[k])){clearInterval(timer);resolve(true)}else if(Date.now()-start>10000){clearInterval(timer);reject(Error('frame readiness timeout'))}},25)})")
    require(ready, "fixture frames were not ready")
    params = {"tab_id": tab, "owner": runner.owner, "kind": "controls"}
    snapshot, cold_observe, _ = runner.rpc("browser.observe", params)
    require(snapshot["coverage"]["unavailable_frames"] == 0, "fixture frame accessibility coverage incomplete")
    candidate = lambda name: next(item for item in snapshot["candidates"] if item["name"] == name)
    require(candidate("Closed shadow action"), "closed shadow control absent from AX projection")
    unchanged, validation, _ = runner.rpc("browser.validate", {**params, "generation": snapshot["generation"], "fingerprint": snapshot["fingerprint"]})
    require(unchanged["fresh"], "unchanged fixture marked stale")
    runner.evaluate(tab, "swapPrices();true")
    swapped, _, _ = runner.rpc("browser.validate", {**params, "generation": snapshot["generation"], "fingerprint": snapshot["fingerprint"]})
    require(not swapped["fresh"], "changed sibling product evidence was not detected")
    old_rows = [item.get("context_relations") for item in snapshot["candidates"] if item["name"] == "Buy"]
    new_rows = [item.get("context_relations") for item in swapped["snapshot"]["candidates"] if item["name"] == "Buy"]
    require(old_rows != new_rows, "product relationships did not reflect changed row prices")
    runner.evaluate(tab, "swapPrices();true")
    outcomes = progress["cases"] = []
    progress.update(navigation_seconds=navigation, cold_observation_seconds=cold_observe,
                    warm_validation_seconds=validation, candidate_count=len(snapshot["candidates"]), frame_count=snapshot["coverage"]["frames_read"])
    def backend_node(selector):
        document, _, _ = runner.rpc("browser.cdp", {"tab_id": tab, "method": "DOM.getDocument", "command_params": {"depth": 0}})
        node, _, _ = runner.rpc("browser.cdp", {"tab_id": tab, "method": "DOM.querySelector", "command_params": {"nodeId": document["result"]["root"]["nodeId"], "selector": selector}})
        described, _, _ = runner.rpc("browser.cdp", {"tab_id": tab, "method": "DOM.describeNode", "command_params": {"nodeId": node["result"]["nodeId"]}})
        return described["result"]["node"]["backendNodeId"]

    scoped_params = {**params, "frame_id": candidate("Name")["frame_id"], "root_backend_dom_node_id": backend_node("form")}
    scoped, scoped_seconds, _ = runner.rpc("browser.observe", scoped_params)
    require(any(item["name"] == "Name" for item in scoped["candidates"]) and not any(item["name"] == "Buy" for item in scoped["candidates"]), "declared form scope broadened to unrelated candidates")
    runner.evaluate(tab, "swapPrices();true")
    scoped_check, scoped_validation, _ = runner.rpc("browser.validate", {**scoped_params, "generation": scoped["generation"], "fingerprint": scoped["fingerprint"]})
    require(scoped_check["fresh"], "unrelated product changes invalidated the declared form scope")
    delta, _, _ = runner.rpc("browser.observe", {**scoped_params, "since": scoped["fingerprint"]})
    require(delta.get("full") is False and delta["delta"]["upserted"] == [] and delta["delta"]["removed"] == [], "unchanged declared scope did not return an empty delta")
    runner.evaluate(tab, "swapPrices();document.querySelector('#country').value='Canada';document.querySelector('#country').dispatchEvent(new Event('change'));true")
    changed_scope, _, _ = runner.rpc("browser.validate", {**scoped_params, "generation": scoped["generation"], "fingerprint": scoped["fingerprint"]})
    require(not changed_scope["fresh"] and any(item["name"] == "Province" for item in changed_scope["snapshot"]["candidates"]), "changed form subtree did not repair its candidate index")
    changed_delta, _, _ = runner.rpc("browser.observe", {**scoped_params, "since": scoped["fingerprint"]})
    require(changed_delta.get("full") is False and changed_delta["delta"]["removed"] and any(item["name"] == "Province" for item in changed_delta["delta"]["upserted"]), "form replacement delta omitted removed or inserted controls")
    runner.evaluate(tab, "document.querySelector('#country').value='United States';document.querySelector('#country').dispatchEvent(new Event('change'));true")
    outcomes.append({"case": "declared_form_scope", "seconds": scoped_seconds, "validation_seconds": scoped_validation,
                     "scope": scoped.get("scope"), "candidate_count": len(scoped["candidates"]), "status": "repaired_and_validated"})
    for kind in ("local", "remote", "deep"):
        target = candidate(kind.capitalize() + " action")
        steps = [{"id": kind, "action": "click", "target": {"name": kind.capitalize() + " action", "role": "button", "frame_id": target["frame_id"], "frame_url": target["frame_url"]},
                  "after": [{"target": {"selector": "#status", "frame_id": target["frame_id"], "frame_url": target["frame_url"]}, "text": "Completed " + kind}]}]
        report, elapsed, _ = runner.plan(tab, url, steps)
        readback, oracle = runner.evaluate(tab, "fixtureReadback()")
        require(report["status"] == "verified" and readback["outcomes"][kind] == 1, f"{kind}: independent frame outcome failed")
        outcomes.append({"case": kind + "_frame", "seconds": elapsed, "independent_verification_seconds": oracle, "verified_seconds": elapsed + oracle, "session_id_present": bool(target.get("session_id")), "status": report["status"]})
    remote = candidate("Remote action")
    report, elapsed, code = runner.plan(tab, url, [{"id": "preexisting-child-after", "action": "click", "target": {"selector": "#basic .buy"},
                                                  "after": [{"target": {"selector": "#status", "frame_id": remote["frame_id"], "frame_url": remote["frame_url"]}, "text": "Completed remote"}]}], success=False)
    readback, _ = runner.evaluate(tab, "fixtureReadback()")
    require(code != 0 and report and report["steps"][0].get("attempted") is False and report["steps"][0].get("changed") is False
            and readback["purchase"] == "No purchase", "already-satisfied child-frame postcondition allowed a root action")
    outcomes.append({"case": "preexisting_cross_frame_after", "seconds": elapsed, "status": "correctly_rejected", "reported": report})
    # Cover the remote iframe in its parent document, not its own document. A
    # child-only hit test would incorrectly allow the otherwise visible button.
    runner.evaluate(tab, "(()=>{const cover=document.createElement('div');cover.id='frame-cover';cover.style.cssText='position:absolute;inset:0;background:#999;z-index:999';document.querySelector('#remote-wrapper').append(cover);return true})()")
    target = candidate("Remote action")
    blocked, elapsed, code = runner.plan(tab, url, [{"id": "parent-overlay", "action": "click", "target": {"name": "Remote action", "role": "button", "frame_id": target["frame_id"], "frame_url": target["frame_url"]},
                                                    "after": [{"target": {"selector": "#status", "frame_id": target["frame_id"], "frame_url": target["frame_url"]}, "text": "Never expected"}]}], success=False)
    readback, _ = runner.evaluate(tab, "fixtureReadback()")
    require(code != 0 and readback["outcomes"]["remote"] == 1, "parent overlay did not block the remote action")
    require(blocked and blocked.get("status") == "failed" and blocked["steps"][0].get("attempted") is False and blocked["steps"][0].get("changed") is False,
            "predispatch ancestor guard rejection was incorrectly reported as an attempted action")
    runner.evaluate(tab, "document.querySelector('#frame-cover').remove();true")
    outcomes.append({"case": "cross_origin_parent_overlay", "seconds": elapsed, "status": "correctly_rejected", "reported": blocked})
    report, elapsed, _ = runner.plan(tab, url, [{"id": "shadow", "action": "click", "target": {"name": "Open shadow action", "role": "button"},
                                              "after": [{"target": {"selector": "#shadow-status"}, "text": "open completed"}]}])
    readback, _ = runner.evaluate(tab, "fixtureReadback()")
    require(report["status"] == "verified" and readback["outcomes"]["open"] == 1, "open shadow outcome failed")
    outcomes.append({"case": "open_shadow", "seconds": elapsed, "status": report["status"]})
    report, elapsed, code = runner.plan(tab, url, [{"id": "covered", "action": "click", "target": {"selector": "#covered"},
                                                  "after": [{"target": {"selector": "#covered-status"}, "text": "Clicked"}]}], success=False)
    readback, _ = runner.evaluate(tab, "fixtureReadback()")
    require(code != 0 and readback["outcomes"]["covered"] == 0, "occluded button was clicked")
    require(report and report.get("status") == "failed" and report["steps"][0].get("attempted") is False and report["steps"][0].get("changed") is False,
            "local occlusion was incorrectly reported as an attempted or changed action")
    outcomes.append({"case": "overlay", "seconds": elapsed, "status": "correctly_rejected", "reported": report})
    steps = [{"id": "country", "action": "choose", "target": {"selector": "#country"}, "values": ["Canada"]},
             {"id": "province", "action": "fill", "target": {"name": "Province", "role": "textbox"}, "value": "Ontario"}]
    report, elapsed, _ = runner.plan(tab, url, steps)
    readback, _ = runner.evaluate(tab, "fixtureReadback()")
    require(report["status"] == "verified" and readback["region_id"] == "province" and readback["fields"]["region"] == "Ontario", "dynamic field replacement failed")
    outcomes.append({"case": "dynamic_field_replacement", "seconds": elapsed, "status": report["status"]})
    values = {"name": "Fixture User", "email": "fixture@example.test", "phone": "5550100"}
    report, elapsed, _ = runner.plan(tab, url, [{"id": key, "action": "fill", "target": {"selector": "#" + key}, "value": value} for key, value in values.items()])
    readback, _ = runner.evaluate(tab, "fixtureReadback()")
    require(all(readback["fields"][key] == value for key, value in values.items()), "three exact fields did not match independent readback")
    outcomes.append({"case": "independent_exact_fields", "seconds": elapsed, "status": report["status"]})
    # Restart only the native host whose registry and immediate OS parent both
    # identify this invocation's private Chromium. Its browser remains alive.
    lease, _, _ = runner.rpc("browser.lease.acquire", {**params, "purpose": "execute"})
    runner.rpc("browser.lease.release", {**params, **lease})
    _, _, invalid = runner.rpc("browser.cdp", {**params, **lease, "method": "DOM.describeNode", "command_params": {"backendNodeId": 1}}, success=False)
    require(invalid and invalid.get("code") == "invalid_lease", "released execution lease remained usable")
    before, _, _ = runner.rpc("browser.observe", params)
    runner.restart_owned_host()
    reconnected, _, _ = runner.rpc("browser.observe", params)
    require(reconnected["generation"] != before["generation"], "host reconnect did not invalidate browser session handles")
    outcomes.append({"case": "host_reconnect", "status": "generation_invalidated"})
    before = reconnected
    runner.evaluate(tab, "location.reload();true")
    time.sleep(0.2)
    after, _, _ = runner.rpc("browser.observe", params)
    old_document = {item["frame_id"]: item["document_generation"] for item in before["candidates"]}
    require(any(item["document_generation"] != old_document.get(item["frame_id"]) for item in after["candidates"]), "navigation did not invalidate document generations")
    outcomes.append({"case": "document_generation", "status": "invalidated"})
    progress["passed"] = True
    return progress


def model_suite(runner, origin, rounds, progress, baseline=False):
    progress.update(passed=False, records=[], summary={}, batch=None,
                    evidence_limit="Small labeled loopback fixtures and real provider calls; not broad real-site accuracy evidence.")
    tab, _ = runner.open(origin + "/selection")
    cases = [
        ("name", "Find the editable field labeled Name", "Name"),
        ("cheapest_basic", "Find the Buy button for the cheapest product", "basic"),
        ("cheapest_pro", "Find the Buy button for the cheapest product", "pro"),
        ("none", "Find a Print boarding pass button. If absent choose none.", None),
        ("shadow", "Find the button labeled Open shadow action", "Open shadow action"),
        ("closed_shadow", "Find the button labeled Closed shadow action", "Closed shadow action"),
    ]
    records = progress["records"]
    tasks = [(round_no, arm, case) for round_no in range(rounds) for arm in (["baseline"] if baseline else ["auto", "legacy"]) for case in cases]
    random.Random(7).shuffle(tasks)
    for round_no, arm, (case_id, goal, expected) in tasks:
        if case_id.startswith("cheapest"):
            prices = ["$10", "$20"] if expected == "basic" else ["$20", "$10"]
            runner.evaluate(tab, "(()=>{const prices=" + json.dumps(prices) + ";document.querySelector('#basic .price').textContent=prices[0];document.querySelector('#pro .price').textContent=prices[1];return true})()")
        args = ["jev", "select", "--browser", "headless", "--tab", str(tab), "--goal", goal, "--no-cache"]
        if not baseline:
            args += ["--strategy", arm]
        decision, elapsed, code = runner.cli(*args, success=False, timeout=150)
        if code != 0 or not isinstance(decision, dict):
            failure = {"round": round_no, "arm": arm, "case": case_id, "expected": expected,
                       "actual": None, "correct": False, "accepted": False, "status": "request_failed",
                       "seconds": elapsed, "provider_requests": None, "error": runner.last_error}
            records.append(failure)
            if not baseline:
                # Controlled fixture text only. Preserve enough public AX
                # evidence to reproduce packing errors without another call.
                try:
                    failure["fixture_snapshot"], _, _ = runner.rpc("browser.observe", {"tab_id": tab, "owner": runner.owner, "kind": "controls"})
                except Exception as error:
                    failure["snapshot_error"] = str(error)
            continue
        selected = decision.get("selected")
        actual = None
        if selected:
            if case_id.startswith("cheapest"):
                attachment, _, _ = runner.rpc("browser.attach", {"tab_id": tab})
                try:
                    resolved, _, _ = runner.rpc("browser.cdp", {"tab_id": tab, "method": "DOM.resolveNode", "command_params": {"backendNodeId": selected["backend_dom_node_id"]}})
                    inspected, _, _ = runner.rpc("browser.cdp", {"tab_id": tab, "method": "Runtime.callFunctionOn", "command_params": {"objectId": resolved["result"]["object"]["objectId"], "functionDeclaration": "function(){return {row:this.closest('tr')?.id||null,tag:this.tagName,text:this.textContent.trim()}}", "returnByValue": True}})
                    element = inspected["result"]["result"].get("value") or {}
                    actual = element.get("row") if element.get("tag") == "BUTTON" and element.get("text") == "Buy" else "wrong_control"
                finally:
                    if not attachment.get("already_attached"):
                        runner.rpc("browser.detach", {"tab_id": tab})
            else:
                actual = selected.get("name")
        accepted = decision.get("status") in ("selected", "no_match") and not decision.get("needs_review")
        records.append({"round": round_no, "arm": arm, "case": case_id, "expected": expected, "actual": actual,
                        "correct": actual == expected, "accepted": accepted, "status": decision.get("status"),
                        "confidence": decision.get("confidence"), "seconds": elapsed, "model": decision.get("model"),
                        "provider_requests": decision.get("timing", {}).get("requests", len(decision.get("decisions", []))),
                        "timing": decision.get("timing"), "usage": decision.get("usage")})
    summaries = progress["summary"]
    for arm in sorted({item["arm"] for item in records}):
        selected = [item for item in records if item["arm"] == arm]
        times = [item["seconds"] for item in selected]
        summaries[arm] = {"trials": len(selected), "correct": sum(item["correct"] for item in selected),
                          "accepted": sum(item["accepted"] for item in selected), "wrong_accepted": sum(item["accepted"] and not item["correct"] for item in selected),
                          "median_seconds": statistics.median(times), "p95_seconds": percentile(times, .95),
                          "provider_requests": sum(item["provider_requests"] or 0 for item in selected),
                          "failed_requests_unknown_count": sum(item["provider_requests"] is None for item in selected)}
    batch = None
    if not baseline:
        path = runner.directory / "goals.json"
        path.write_text(json.dumps({"version": 1, "goals": [{"id": key, "goal": "Find the editable field labeled " + key.capitalize()} for key in ["name", "email", "phone"]]}))
        value, elapsed, _ = runner.cli("browser", "select-batch", str(path), "--browser", "headless", "--tab", str(tab), "--no-cache")
        require(all(item["decision"].get("selected", {}).get("name") == item["id"].capitalize() for item in value["results"]), "independent model target batch failed labels")
        batch = progress["batch"] = {"seconds": elapsed, "results": [{"id": item["id"], "status": item["decision"]["status"], "timing": item["decision"].get("timing")} for item in value["results"]]}
        steps = [{"id": key, "batch": "profile", "action": "fill", "target": {"goal": "Find the editable field labeled " + key.capitalize()}, "value": val}
                 for key, val in [("name", "Batch Fixture"), ("email", "batch@example.test"), ("phone", "5550199")]]
        report, completed, _ = runner.plan(tab, origin + "/selection", steps)
        readback, oracle = runner.evaluate(tab, "fixtureReadback()")
        require(readback["fields"]["name"] == "Batch Fixture" and readback["fields"]["email"] == "batch@example.test" and readback["fields"]["phone"] == "5550199", "semantic batch action outcomes differed")
        batch["closed_loop"] = {"seconds": completed, "independent_verification_seconds": oracle, "verified_seconds": completed + oracle, "report": report, "independently_verified": True}
        report, elapsed, _ = runner.plan(tab, origin + "/selection", [{"id": "closed-shadow", "action": "click", "target": {"goal": "Find the button labeled Closed shadow action"},
                                                                       "after": [{"target": {"selector": "#shadow-status"}, "text": "closed completed"}]}])
        readback, oracle = runner.evaluate(tab, "fixtureReadback()")
        require(readback["outcomes"]["closed"] == 1 and report["status"] == "verified", "bound closed-shadow action outcome failed")
        batch["closed_shadow"] = {"seconds": elapsed, "independent_verification_seconds": oracle, "verified_seconds": elapsed + oracle, "independently_verified": True, "report": report}
        cache_records = []
        for _ in range(2):
            decision, elapsed, _ = runner.cli("jev", "select", "--browser", "headless", "--tab", str(tab),
                                              "--goal", "Find the editable Name field in the Profile form")
            require(decision.get("selected", {}).get("name") == "Name", "reused binding selected an incorrect field")
            cache_records.append({"seconds": elapsed, "cache_hit": decision.get("cache_hit"), "provider_requests": decision.get("timing", {}).get("requests"), "timing": decision.get("timing")})
        require(cache_records[-1]["cache_hit"] and cache_records[-1]["provider_requests"] == 0, "verified reuse did not eliminate provider inference")
        batch["verified_reuse"] = cache_records
        dependency_url = origin + "/selection?dependency=1"
        dependency_tab, _ = runner.open(dependency_url)
        dependent_steps = [
            {"id": "country", "batch": "country-region", "action": "choose", "target": {"goal": "Find the Country selection control"}, "values": ["Canada"]},
            {"id": "region", "batch": "country-region", "action": "fill", "target": {"goal": "Find the State or Province editable field for the currently selected country"}, "value": "Ontario"},
        ]
        report, elapsed, _ = runner.plan(dependency_tab, dependency_url, dependent_steps)
        readback, oracle = runner.evaluate(dependency_tab, "fixtureReadback()")
        require(report["status"] == "verified" and readback["region_id"] == "province" and readback["fields"]["region"] == "Ontario", "dependent semantic batch acted on a stale country-region binding")
        require(report.get("decision_requests", 0) >= 2, "dependent batch did not reselect after the field replacement")
        batch["dependent_reselection"] = {"seconds": elapsed, "independent_verification_seconds": oracle, "verified_seconds": elapsed + oracle, "independently_verified": True, "report": report}
        frame_url = origin + "/?frame-skip=1"
        frame_tab, _ = runner.open(frame_url)
        ready, _ = runner.evaluate(frame_tab, "new Promise((resolve,reject)=>{const start=Date.now();const timer=setInterval(()=>{if(['local','remote','deep'].every(k=>readyFrames[k])){clearInterval(timer);resolve(true)}else if(Date.now()-start>10000){clearInterval(timer);reject(Error('frame readiness timeout'))}},25)})")
        require(ready, "semantic frame-scope fixture was not ready")
        # The root deliberately satisfies the child condition. URL-only target
        # scope must resolve before evaluating an unscoped skip predicate.
        runner.evaluate(frame_tab, "(()=>{const status=document.createElement('output');status.id='status';status.textContent='Completed remote';document.body.prepend(status);return true})()")
        frame_target = {"goal": "Find the button labeled Remote action", "frame_url": origin.replace("127.0.0.1", "localhost") + "/frame.html?kind=remote"}
        skip_step = {"id": "child-skip", "action": "click", "target": frame_target,
                     "skip_if": [{"target": {"selector": "#status"}, "text": "Completed remote"}],
                     "after": [{"target": {"selector": "#status"}, "text": "Completed remote"}]}
        first, first_seconds, _ = runner.plan(frame_tab, frame_url, [skip_step])
        readback, first_oracle = runner.evaluate(frame_tab, "fixtureReadback()")
        require(first["steps"][0]["status"] == "verified" and readback["outcomes"]["remote"] == 1,
                "URL-scoped child condition incorrectly used the matching root state")
        second, second_seconds, _ = runner.plan(frame_tab, frame_url, [skip_step])
        readback, second_oracle = runner.evaluate(frame_tab, "fixtureReadback()")
        require(second["steps"][0]["status"] == "skipped" and second["steps"][0].get("attempted") is False and readback["outcomes"]["remote"] == 1,
                "URL-scoped semantic skip condition did not use child state")
        batch["frame_url_conditions"] = {"seconds": first_seconds + second_seconds, "independent_verification_seconds": first_oracle + second_oracle,
                                          "independently_verified": True, "performed": first, "skipped": second}
    else:
        # The old executor has no batch annotation. It receives the same goals,
        # values and explicit postconditions and resolves the three steps serially.
        steps = [{"id": key, "action": "fill", "target": {"goal": "Find the editable field labeled " + key.capitalize()}, "value": val}
                 for key, val in [("name", "Batch Fixture"), ("email", "batch@example.test"), ("phone", "5550199")]]
        report, completed, _ = runner.plan(tab, origin + "/selection", steps)
        readback, oracle = runner.evaluate(tab, "fixtureReadback()")
        require(report["status"] == "verified" and readback["fields"]["name"] == "Batch Fixture" and readback["fields"]["email"] == "batch@example.test" and readback["fields"]["phone"] == "5550199", "baseline semantic field outcomes differed")
        batch = progress["batch"] = {"closed_loop": {"seconds": completed, "independent_verification_seconds": oracle, "verified_seconds": completed + oracle, "report": report, "independently_verified": True}}
    progress["passed"] = all(item["status"] != "request_failed" for item in records)
    return progress


def executable(path):
    found = shutil.which(str(path))
    require(found, "configured binary does not exist or is not executable")
    return Path(found).resolve()


def public_snapshot_diagnostics(runner, tab, scope=None):
    reads = []
    previous = None
    for index in range(8):
        if index:
            time.sleep(.25)
        snapshot, elapsed, _ = runner.rpc("browser.observe", {"tab_id": tab, "owner": runner.owner, "kind": "controls", **(scope or {})})
        candidates = {item["id"]: item for item in snapshot["candidates"]}
        changes = []
        if previous is not None:
            old = {item["id"]: item for item in previous["candidates"]}
            for candidate_id in sorted(set(old) | set(candidates)):
                before, after = old.get(candidate_id), candidates.get(candidate_id)
                if before == after:
                    continue
                fields = sorted(key for key in set(before or {}) | set(after or {}) if (before or {}).get(key) != (after or {}).get(key))
                changes.append({"id": candidate_id, "name": (after or before).get("name"),
                                "type": "added" if before is None else "removed" if after is None else "changed",
                                "fields": fields, "before": before, "after": after})
        reads.append({"seconds": elapsed, "generation": snapshot["generation"], "fingerprint": snapshot["fingerprint"],
                      "coverage": snapshot["coverage"], "candidate_count": len(candidates), "changes": changes,
                      "scope": snapshot.get("scope"),
                      "frame_identities": sorted({(item["frame_id"], item.get("session_id", ""), item.get("document_generation", "")) for item in candidates.values()}),
                      "candidate_order_changed": previous is not None and [item["id"] for item in previous["candidates"]] != [item["id"] for item in snapshot["candidates"]]})
        previous = snapshot
    return {"reads": reads, "last_three_fingerprints_equal": len({item["fingerprint"] for item in reads[-3:]}) == 1,
            "last_snapshot": previous, "gap_seconds": .25, "provider_requests": 0}


def public_github_suite(runner, rounds, progress, baseline=False, diagnose=False, settled=False, scoped=False):
    """A fixed read-only page and three independently labeled link goals."""
    url = "https://github.com/browser-use/jev-ultrafast"
    cases = [("issues", "Find the repository Issues navigation tab", url + "/issues"),
             ("readme", "Find the README.md file link in the repository file list", url + "/blob/main/README.md"),
             ("docs", "Find the docs directory link in the repository file list", url + "/tree/main/docs")]
    progress.update(passed=False, page_url=url, records=[], summary={},
                    evidence_limit="Three read-only link goals on one public GitHub repository page; independent href labels. No clicks or task-completion claim.")
    declared_scopes = {"issues": {"roles": ["navigation"], "name": "Repository"},
                       "readme": {"roles": ["table", "layouttable"], "name": "Folders and files"},
                       "docs": {"roles": ["table", "layouttable"], "name": "Folders and files"}}
    if scoped:
        progress["declared_scopes"] = declared_scopes
        progress["scope_policy"] = "Caller explicitly restricts Issues to Repository navigation and file goals to Folders and files table. No automatic goal-based narrowing."
    tab, initial_navigation = runner.open(url)
    progress["initial_navigation_seconds"] = initial_navigation
    expected_urls = [item[2] for item in cases]
    tasks = [(round_no, arm, case) for round_no in range(rounds)
             for arm in (["baseline"] if baseline else ["auto"] if settled else ["auto", "legacy"]) for case in cases]
    random.Random(7).shuffle(tasks)
    for index, (round_no, arm, (case_id, goal, expected)) in enumerate(tasks):
        preparation_started = time.perf_counter()
        if index:
            runner.rpc("browser.cdp", {"tab_id": tab, "method": "Page.navigate", "command_params": {"url": url}})
        ready_expression = "new Promise(resolve=>{const expected=" + json.dumps(expected_urls) + ";const start=Date.now();const timer=setInterval(()=>{const hrefs=new Set([...document.querySelectorAll('a[href]')].map(a=>a.href));if(expected.every(h=>hrefs.has(h))){clearInterval(timer);resolve({ready:true,title:document.title,url:location.href})}else if(Date.now()-start>12000){clearInterval(timer);resolve({ready:false,title:document.title,url:location.href})}},100)})"
        ready, _ = runner.evaluate(tab, ready_expression)
        if not ready or not ready.get("ready") or ready.get("url", "").rstrip("/") != url:
            progress.update(unavailable=True, page_state=ready, error="Public page unavailable, changed, or challenged; optional arm stopped without retry.")
            return progress
        progress["page_title"] = ready.get("title")
        scope = None
        if scoped:
            full_settlement = public_snapshot_diagnostics(runner, tab)
            progress.setdefault("container_preparation", []).append({"case": case_id, **full_settlement})
            if not full_settlement["last_three_fingerprints_equal"]:
                progress.update(unsettled=True, error="Page did not settle before declared container resolution; provider call skipped.")
                return progress
            # Resolve the caller's predeclared AX container, never a candidate
            # inferred from the goal or selected by the provider.
            tree, _, _ = runner.rpc("browser.cdp", {"tab_id": tab, "method": "Page.getFrameTree", "command_params": {}})
            frame_id = tree["result"]["frameTree"]["frame"]["id"]
            ax, _, _ = runner.rpc("browser.cdp", {"tab_id": tab, "method": "Accessibility.getFullAXTree", "command_params": {"frameId": frame_id}})
            declaration = declared_scopes[case_id]
            matches = [node for node in ax["result"]["nodes"]
                       if str(node.get("role", {}).get("value", "")).lower() in declaration["roles"]
                       and node.get("name", {}).get("value") == declaration["name"] and node.get("backendDOMNodeId", 0) > 0]
            require(len(matches) == 1, "declared public-page AX container is unavailable or ambiguous")
            scope = {"frame_id": frame_id, "root_backend_dom_node_id": matches[0]["backendDOMNodeId"]}
        if diagnose or settled:
            diagnostic = public_snapshot_diagnostics(runner, tab, scope)
            progress.setdefault("observation_diagnostics", []).append({"case": case_id, **diagnostic})
            if diagnose:
                progress.update(passed=True, diagnostic_only=True, provider_requests=0)
                return progress
            if not diagnostic["last_three_fingerprints_equal"]:
                progress.update(unsettled=True, error="Full semantic fingerprints did not settle within eight bounded observations; provider call skipped.")
                return progress
        args = ["jev", "select", "--browser", "headless", "--tab", str(tab), "--goal", goal, "--no-cache"]
        if not baseline:
            args += ["--strategy", arm]
        if scope:
            args += ["--frame", scope["frame_id"], "--root-node", str(scope["root_backend_dom_node_id"])]
        preparation_seconds = time.perf_counter() - preparation_started
        decision, elapsed, code = runner.cli(*args, success=False, timeout=150)
        record = {"round": round_no, "arm": arm, "case": case_id, "expected_href": expected,
                  "actual_href": None, "correct": False, "accepted": False, "seconds": elapsed,
                  "preparation_seconds": preparation_seconds}
        if scope:
            record["scope"] = scope
        progress["records"].append(record)
        if code != 0 or not isinstance(decision, dict):
            record.update(status="request_failed", provider_requests=None, error=runner.last_error)
            continue
        selected = decision.get("selected")
        if selected:
            attachment, _, _ = runner.rpc("browser.attach", {"tab_id": tab})
            try:
                resolved, _, _ = runner.rpc("browser.cdp", {"tab_id": tab, "method": "DOM.resolveNode", "command_params": {"backendNodeId": selected["backend_dom_node_id"]}})
                inspected, _, _ = runner.rpc("browser.cdp", {"tab_id": tab, "method": "Runtime.callFunctionOn", "command_params": {"objectId": resolved["result"]["object"]["objectId"], "functionDeclaration": "function(){return {href:this.tagName==='A'?this.href:null,tag:this.tagName}}", "returnByValue": True}})
                record["actual_href"] = (inspected["result"]["result"].get("value") or {}).get("href")
            finally:
                if not attachment.get("already_attached"):
                    runner.rpc("browser.detach", {"tab_id": tab})
        record.update(correct=record["actual_href"] == expected,
                      accepted=decision.get("status") in ("selected", "no_match") and not decision.get("needs_review"),
                      status=decision.get("status"), confidence=decision.get("confidence"), model=decision.get("model"),
                      provider_requests=decision.get("timing", {}).get("requests", len(decision.get("decisions", []))),
                      timing=decision.get("timing"), usage=decision.get("usage"),
                      coverage=decision.get("coverage"))
    for arm in sorted({item["arm"] for item in progress["records"]}):
        records = [item for item in progress["records"] if item["arm"] == arm]
        times = [item["seconds"] for item in records]
        progress["summary"][arm] = {"trials": len(records), "correct": sum(item["correct"] for item in records),
                                     "accepted": sum(item["accepted"] for item in records),
                                     "wrong_accepted": sum(item["accepted"] and not item["correct"] for item in records),
                                     "median_seconds": statistics.median(times), "p95_seconds": percentile(times, .95),
                                     "provider_requests": sum(item["provider_requests"] or 0 for item in records),
                                     "failed_requests_unknown_count": sum(item["provider_requests"] is None for item in records)}
    progress["passed"] = all(item["status"] != "request_failed" for item in progress["records"])
    return progress


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--with-jev", action="store_true")
    parser.add_argument("--rounds", type=int, default=1)
    parser.add_argument("--baseline", action="store_true")
    parser.add_argument("--baseline-only", action="store_true", help="Run only the preserved baseline provider arm")
    parser.add_argument("--public-github", action="store_true", help="Separate read-only comparison on the public jev-ultrafast GitHub page")
    parser.add_argument("--public-diagnose", action="store_true", help="Read eight public-page snapshots without provider calls")
    parser.add_argument("--public-settled", action="store_true", help="Require three matching semantic fingerprints before each of three public-page auto choices")
    parser.add_argument("--public-scoped", action="store_true", help="Use explicitly declared Repository navigation and Folders and files scopes for the settled public choices")
    parser.add_argument("--baseline-binary", default="/tmp/rep-runtime-baseline-rep")
    parser.add_argument("--baseline-host", default="/tmp/rep-runtime-baseline-host")
    parser.add_argument("--baseline-extension", default="/tmp/rep-runtime-baseline-extension")
    parser.add_argument("--build", action="store_true", help="Build fresh CLI and host into the owned temporary directory")
    options = parser.parse_args()
    options.baseline = options.baseline or options.baseline_only
    options.public_settled = options.public_settled or options.public_scoped
    options.public_github = options.public_github or options.public_diagnose or options.public_settled
    require(1 <= options.rounds <= 10, "rounds must be between 1 and 10")
    require(not options.baseline or options.with_jev, "--baseline requires --with-jev")
    require(not options.public_github or options.with_jev or options.public_diagnose, "--public-github requires --with-jev")
    require(not (options.public_diagnose or options.public_settled) or not options.baseline, "public observation diagnostics require the current compact runtime")
    require(not (options.public_diagnose and options.public_settled), "choose diagnose or settled mode")
    options.output = options.output.resolve()
    require(not options.output.exists(), "output already exists; choose a new report path")
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.daemon_threads = True
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    result = {"schema": 1, "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "passed": False,
              "fixtures": "public GitHub page, read-only" if options.public_github else "loopback only", "with_jev": options.with_jev, "arms": {},
              "harness_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
              "fixture_sha256": hashlib.sha256(b"".join(path.read_bytes() for path in sorted(FIXTURES.glob('*.html')))).hexdigest(),
              "requested_model": os.environ.get("JEV_MODEL"),
              "baseline_provenance": "Preserved CLI HEAD 05fa215e6503683f5e196a9c3ebceb888cb65539; extension copied with pre-existing uncommitted changes before runtime edits.",
              "clock": "seconds measures CLI completion including executor verification; verified_seconds additionally includes independent renderer readback. Fixture preparation is excluded from per-case clocks.",
              "statistics": "p95 uses nearest rank; small fixture sample sizes do not establish tail latency or broad task accuracy."}
    try:
        with tempfile.TemporaryDirectory(prefix="rep-decision-runtime-", dir="/tmp") as temporary:
            directory = Path(temporary)
            if options.build:
                binary, host = directory / "rep", directory / "rep-host"
                for destination, package in [(binary, "."), (host, "./cmd/host")]:
                    built = subprocess.run(["go", "build", "-o", str(destination), package], cwd=ROOT, capture_output=True, timeout=180)
                    require(built.returncode == 0, f"build failed for {package}")
            elif not options.baseline_only:
                binary = executable(os.environ.get("REP_BINARY", "rep"))
                host = executable(os.environ.get("REP_HOST_BINARY", "rep-host"))
            extension = Path(os.environ.get("REP_EXTENSION_PATH", ROOT.parent / "rep")).resolve()
            origin = f"http://127.0.0.1:{server.server_port}"
            arms = [] if options.baseline_only else [("current", binary, host, extension)]
            if options.baseline:
                arms.append(("baseline", executable(options.baseline_binary), executable(options.baseline_host), Path(options.baseline_extension).resolve()))
            for name, cli_binary, host_binary, extension_path in arms:
                task_dir = directory / name
                task_dir.mkdir()
                frozen_extension = task_dir / "extension"
                shutil.copytree(extension_path, frozen_extension, ignore=shutil.ignore_patterns(".git", "node_modules", ".env", ".env.*", "coverage", "*.zip", "*.crx"))
                runner = Runner(cli_binary, host_binary, frozen_extension, task_dir, name)
                source_digest = hashlib.sha256()
                for path in sorted([*frozen_extension.rglob("*.js"), frozen_extension / "manifest.json"]):
                    source_digest.update(str(path.relative_to(frozen_extension)).encode() + b"\0" + path.read_bytes())
                arm = result["arms"][name] = {"task": runner.task, "cli_sha256": hashlib.sha256(Path(cli_binary).read_bytes()).hexdigest(),
                                               "host_sha256": hashlib.sha256(Path(host_binary).read_bytes()).hexdigest(),
                                               "extension_js_sha256": source_digest.hexdigest()}
                try:
                    arm["browser_start_seconds"] = runner.start()
                    with urllib.request.urlopen(f"http://127.0.0.1:{runner.state['port']}/json/version", timeout=5) as response:
                        browser_version = json.load(response)
                    arm["chromium"] = {key: browser_version.get(key) for key in ["Browser", "Protocol-Version", "V8-Version", "WebKit-Version"]}
                    if name == "current" and not options.public_github:
                        arm["local"] = {}
                        local_suite(runner, origin, arm["local"])
                    if options.with_jev or options.public_diagnose:
                        arm["jev"] = {}
                        try:
                            if options.public_github:
                                public_github_suite(runner, options.rounds, arm["jev"], baseline=name == "baseline", diagnose=options.public_diagnose, settled=options.public_settled, scoped=options.public_scoped)
                            else:
                                model_suite(runner, origin, options.rounds, arm["jev"], baseline=name == "baseline")
                        except Exception as error:
                            arm["jev"]["error"] = str(error)
                finally:
                    runner.stop()
                    arm["owned_browser_stopped"] = True
            result["passed"] = all(arm.get("jev", {"passed": True}).get("passed", False) for arm in result["arms"].values())
    except Exception as error:
        result["error"] = str(error)
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)
        options.output.parent.mkdir(parents=True, exist_ok=True)
        options.output.write_text(json.dumps(result, indent=2) + "\n")
        print(json.dumps({"passed": result["passed"], "report": str(options.output), "error": result.get("error")}))
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
