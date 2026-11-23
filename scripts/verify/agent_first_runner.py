"""Local subprocess gates for the split CLI; browser access is only through rep."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import uuid

from agent_first_fixtures import serve

ROOT = Path(__file__).resolve().parents[2]


class Runner:
    def __init__(self, binary):
        self.binary = Path(binary).resolve()
        self.env = dict(os.environ)
        for key in ("XDG_DATA_HOME", "XDG_RUNTIME_DIR", "REP_PROFILE", "REP_EXTENSION_PATH"):
            if not self.env.get(key):
                raise RuntimeError(f"set {key} to your private test slot")
        for key in ("REP_WORKSPACE", "REP_TASK", "REP_TASK_SESSION", "REP_BRIDGE_DIR", "REPLIVE_PATH", "REPANDROID_PATH", "JEV", "JEV_API_KEY", "TYPESAFE_API_KEY", "JEV_ENV_FILE"):
            self.env.pop(key, None)
        self.env["PATH"] = str(self.binary.parent) + os.pathsep + self.env.get("PATH", "")
        self.env["REP_BROWSER"] = "headless"
        self.task = "lab-verify-" + uuid.uuid4().hex[:12]
        self.records, self.tabs = [], set()
        self.temp = tempfile.TemporaryDirectory(prefix="lab-verify-", dir="/tmp")
        self.directory = Path(self.temp.name)
        self.env["XDG_CONFIG_HOME"] = str(self.directory / "config")

    def raw(self, args, binary=None):
        return subprocess.run([str(binary or self.binary), *args], env=self.env, cwd=self.directory,
                              text=True, capture_output=True, timeout=100)

    def call(self, *args, expected=0):
        argv = [*args, "--task", self.task]
        result = self.raw(argv)
        try:
            value = json.loads(result.stdout)
        except ValueError:
            raise AssertionError(f"{args[:2]} emitted invalid JSON (exit {result.returncode})") from None
        self.records.append({"binary": "rep", "argv": list(args),
                             "exit": result.returncode, "result": value})
        if result.returncode != expected:
            raise AssertionError(f"{args[:2]} exit {result.returncode}: {value}")
        if args and args[0] == "close" and len(args) > 1:
            self.tabs.discard(args[1])
        elif value.get("tab"):
            self.tabs.add(value["tab"])
        return value

    def start(self):
        # Read the built CLI's contract before the first browser operation.
        help_result = self.raw(["--help"])
        if help_result.returncode:
            raise AssertionError("the built rep --help failed")
        probe = self.raw("browser start --help".split())
        if probe.returncode != 0 or "rep browser start" not in probe.stdout:
            raise AssertionError("the checkout must support rep browser start")
        self.call("browser", "start")

    def close(self):
        errors = []
        for tab in sorted(self.tabs):
            try:
                self.call("close", tab)
            except Exception as error:
                errors.append(str(error))
        try:
            self.call("browser", "stop")
        except Exception as error:
            errors.append(str(error))
        self.temp.cleanup()
        if errors:
            raise AssertionError("private cleanup failed: " + "; ".join(errors))


def reference(runner, origin):
    result = runner.call("reference", origin + "/reference")
    assert result["tab"] and "Design reference" in result["untrusted"]["content"]
    screenshot = result["meta"]["screenshot"]
    path = Path(screenshot["path"])
    assert path.stat().st_mode & 0o777 == 0o600
    assert hashlib.sha256(path.read_bytes()).hexdigest() == screenshot["sha256"]
    snapshot = json.loads(Path(result["meta"]["snapshot_path"]).read_text())
    assert snapshot.get("content") and snapshot.get("styles")
    first = result
    for _ in range(100):
        if not result.get("next"):
            break
        argv = shlex.split(result["next"])
        assert argv[0:2] == ["rep", "reference"] and argv[2].startswith("p_")
        # The handle owns its task, so returned commands can run verbatim.
        completed = runner.raw(argv[1:])
        assert completed.returncode == 0
        result = json.loads(completed.stdout)
    assert result.get("complete") is True
    focus = runner.call("reference", first["tab"], "--selector", "h1")
    assert "Design reference" in focus["untrusted"]["content"]
    return {"private_screenshot": True, "sections_complete": True, "selector": True}


def interaction(runner, origin):
    page = runner.call("page", origin + "/form")
    tab = page["tab"]
    query = next(c["ref"] for c in page["untrusted"]["controls"] if c["role"] == "textbox")
    result = runner.call("do", query, "synthetic query", "--press", "Enter")
    assert result["status"] == "performed_and_verified"
    assert "Saved: synthetic query" in result["untrusted"]["text"]
    checked = runner.call("eval", "document.querySelector('[role=status]').textContent", tab)
    assert checked["untrusted"]["value"] == "Saved: synthetic query"
    plan = runner.directory / "plan.json"
    plan.write_text(json.dumps({"version": 1, "url": origin + "/form", "steps": [
        {"id": "fill", "action": "fill", "target": {"role": "textbox", "name": "Query"}, "value": "planned query", "replace": True},
        {"id": "submit", "action": "press", "target": {"role": "textbox", "name": "Query"}, "keys": ["Enter"],
         "after": [{"target": {"selector": "[role=status]"}, "text": "Saved: planned query"}]}
    ]}))
    planned = runner.call("do", "@" + str(plan), tab)
    assert "Saved: planned query" in planned["untrusted"]["text"]
    allowed = runner.call("cdp", "Page.getLayoutMetrics", tab)
    assert allowed["complete"]
    denied = runner.call("cdp", "Network.getAllCookies", tab, expected=1)
    assert denied["error"]["code"] == "cdp_forbidden"
    return {"fill_submit_delta": True, "plan": True, "eval": True, "guarded_cdp": True}


def lab(runner, origin):
    help_result = runner.raw(["lab", "--help"])
    assert help_result.returncode == 0
    key_file = runner.directory / "fake.env"
    key_file.write_text("JEV_API_KEY=synthetic-lab-fixture-key\n")
    key_file.chmod(0o600)
    configured = runner.call("lab", "jev", "config", "--env-file", str(key_file))
    assert configured["configured"] and configured["stores_key"] is False
    status = runner.call("lab", "jev", "status")
    assert status["configured"] and "synthetic-lab-fixture-key" not in json.dumps(status)
    page = runner.call("page", origin + "/frame")
    # The returned public handle supplies both task and exact browser profile.
    # No private registry access, numeric ID conversion or --browser is needed.
    argv = ["lab", "element", "observe", "--tab", page["tab"], "--kind", "all", "--frame-url", origin + "/form"]
    completed = runner.raw(argv)
    observed = json.loads(completed.stdout)
    runner.records.append({"binary": "rep", "argv": argv, "exit": completed.returncode, "result": observed})
    assert completed.returncode == 0, observed
    assert "Query" in json.dumps(observed) and observed.get("fingerprint")
    wrong_browser = runner.call("lab", "element", "observe", "--tab", page["tab"], "--browser", "dia", expected=2)
    assert wrong_browser["error"]["code"] == "invalid_argument"
    wrong_args = ["lab", "element", "observe", "--tab", page["tab"], "--task", runner.task + "-other"]
    wrong = runner.raw(wrong_args)
    wrong_task = json.loads(wrong.stdout)
    runner.records.append({"binary": "rep", "argv": wrong_args, "exit": wrong.returncode, "result": wrong_task})
    assert wrong.returncode == 1 and wrong_task["error"]["code"] == "owned_by_other_task"
    remote = origin.replace("127.0.0.1", "localhost") + "/form"
    runner.call("eval", "await new Promise(resolve => {const f=document.querySelector('iframe'); "
                "f.onload=()=>resolve(true); f.src=" + json.dumps(remote) + ";})", page["tab"])
    deep = runner.call("lab", "element", "observe", "--tab", page["tab"],
                      "--kind", "all", "--frame-url", remote)
    assert "Query" in json.dumps(deep) and deep.get("fingerprint")
    assert "synthetic-lab-fixture-key" not in json.dumps(runner.records)
    return {"jev_config_status": True, "public_tab_handle": True, "inferred_task_and_profile": True,
            "conflicting_task_browser_refused": True, "frame_observation": True,
            "cross_origin_frame": True, "credentials_private": True}


def main(scenario=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rep", default=os.environ.get("REP_BINARY", ROOT.parent / "bin" / "rep"))
    parser.add_argument("--output", "--out", type=Path)
    parser.add_argument("--scenario", choices=("reference", "interaction", "lab", "all"), default=scenario or "all")
    args = parser.parse_args()
    runner = Runner(args.rep)
    report = {"passed": False, "scenarios": {}, "profile": runner.env["REP_PROFILE"]}
    try:
        runner.start()
        with serve() as fixture:
            for name, gate in (("reference", reference), ("interaction", interaction), ("lab", lab)):
                if args.scenario in (name, "all"):
                    report["scenarios"][name] = gate(runner, fixture.url)
        report["passed"] = True
    finally:
        try:
            runner.close()
        except Exception:
            report["passed"] = False
            raise
        finally:
            report["calls"] = runner.records
            if args.output:
                args.output.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({key: value for key, value in report.items() if key != "calls"}))


if __name__ == "__main__":
    main()
