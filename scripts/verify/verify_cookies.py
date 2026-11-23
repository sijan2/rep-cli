#!/usr/bin/env python3
"""Verify cookie/session roundtrips and raw result exports using only rep.

Uses synthetic loopback fixtures on port 0, unique private XDG paths/profile,
matching extension checkout, and the rep-host adjacent to the supplied binary.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import tempfile
import uuid

from agent_first_fixtures import FILE_BYTES, serve
from agent_first_runner import ROOT, Runner


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def artifact(path, expected=None):
    require(path.stat().st_mode & 0o777 == 0o600, "export is not mode 0600")
    value = json.loads(path.read_text())
    if expected is not None:
        require(value == expected, "raw export differs from original value")
    return value


def run(runner, origin):
    for words in ("cookies", "cookies list", "cookies export", "cookies import", "cookies delete", "eval", "cdp", "fetch"):
        help_result = runner.raw([*words.split(), "--help"])
        require(help_result.returncode == 0, words + " help unavailable")
    page = runner.call("page", origin + "/download")
    tab = page["tab"]
    for _ in range(8):
        if page.get("complete"):
            break
        require(page.get("next"), "incomplete page has no continuation")
        argv = shlex.split(page["next"])
        require(argv[0] == "rep", "unexpected continuation")
        done = runner.raw(argv[1:])
        require(done.returncode == 0, "page continuation failed")
        page = json.loads(done.stdout)
    if not page.get("complete"):
        runner.call("shot", tab)
        raise AssertionError("local fixture stayed incomplete; screenshot recorded")

    listed = runner.call("cookies", "list", tab)
    cookie = next(c for c in listed["untrusted"]["items"] if c["name"] == "fixture-session")
    require(cookie["httpOnly"], "fixture did not create HttpOnly cookie")
    require("fake-cookie" not in json.dumps(listed), "cookie list unexpectedly exposed value")
    invisible = runner.call("eval", "document.cookie", tab)
    require("fixture-session" not in invisible["untrusted"]["value"], "HttpOnly cookie visible to page JS")
    destination = runner.directory / "cookies.json"
    exported = runner.call("cookies", "export", tab, str(destination))
    raw = artifact(destination)
    require(any(c["name"] == "fixture-session" and c["value"] == "fake-cookie" and c["httpOnly"] for c in raw["cookies"]), "export lost HttpOnly cookie")
    require(hashlib.sha256(destination.read_bytes()).hexdigest() == exported["meta"]["artifact"]["sha256"], "export digest mismatch")
    existing = runner.call("cookies", "export", tab, str(destination), expected=1)
    require(existing.get("error"), "export overwrote existing file")

    denied = runner.raw(["cookies", "delete", tab, "fixture-session", "--task", runner.task + "-sibling"])
    denied_value = json.loads(denied.stdout)
    require(denied.returncode != 0 and denied_value.get("error", {}).get("code") == "owned_by_other_task", "sibling task mutated owned tab cookies")
    runner.records.append({"binary": "rep", "argv": ["cookies", "delete", tab, "fixture-session", "--task", runner.task + "-sibling"], "exit": denied.returncode, "result": denied_value})

    runner.call("cookies", "delete", tab, "fixture-session")
    absent = runner.call("cookies", "list", tab)
    require(not any(c["name"] == "fixture-session" for c in absent["untrusted"]["items"]), "cookie deletion had no effect")
    empty_path = runner.directory / "empty-cookies.json"
    runner.call("cookies", "export", tab, str(empty_path))
    require(artifact(empty_path)["cookies"] == [], "empty export is not an array")
    runner.call("cookies", "import", tab, "@" + str(empty_path))
    unauthorized = runner.call("fetch", origin + "/file.pdf")
    require(unauthorized["meta"]["variants"][0]["status"] == 401, "deleted session still authenticated")
    runner.call("cookies", "import", tab, "@" + str(destination))
    restored = runner.call("fetch", origin + "/file.pdf")
    require(restored["complete"] and restored["meta"]["variants"][0]["status"] == 200, "restored session did not authenticate")
    require(restored["untrusted"]["body"].encode() == FILE_BYTES, "authenticated response bytes changed")

    # A login can carry a root cookie plus an HttpOnly API-path cookie. Export
    # from the current /download URL must be able to collect both deliberately.
    path_source = runner.directory / "path-cookies.json"
    path_source.write_text(json.dumps([{"name": "api-session", "value": "path-cookie-value",
        "path": "/api", "httpOnly": True, "sameSite": "Strict"}]))
    runner.call("cookies", "import", tab, "@" + str(path_source))
    url_only = runner.call("cookies", "list", tab)
    require(not any(c["name"] == "api-session" for c in url_only["untrusted"]["items"]), "URL cookie read unexpectedly included another path")
    other = runner.call("page", tab, origin.replace("127.0.0.1", "localhost") + "/article")
    other_source = runner.directory / "other-host-cookie.json"
    other_source.write_text('[{"name":"other-host","value":"unrelated","path":"/"}]')
    runner.call("cookies", "import", other["tab"], "@" + str(other_source))
    runner.call("page", tab, origin + "/article")
    all_paths = runner.call("cookies", "list", tab, "--all-paths")
    require(any(c["name"] == "api-session" and c["path"] == "/api" for c in all_paths["untrusted"]["items"]), "all-paths omitted API cookie")
    require(not any(c["name"] == "other-host" for c in all_paths["untrusted"]["items"]), "all-paths included unrelated host")
    all_json = runner.directory / "all-path-cookies.json"
    runner.call("cookies", "export", tab, str(all_json), "--all-paths")
    require(any(c["name"] == "api-session" for c in artifact(all_json)["cookies"]), "JSON export omitted other path")
    netscape_path = runner.directory / "cookies.txt"
    netscape = runner.call("cookies", "export", tab, str(netscape_path), "--all-paths", "--format", "netscape")
    require(netscape_path.stat().st_mode & 0o777 == 0o600, "Netscape export is not mode 0600")
    require(netscape["meta"]["format"] == "netscape" and netscape["meta"]["rep_metadata_comments"], "Netscape format metadata missing")
    require("#HttpOnly_127.0.0.1\tFALSE\t/api\tFALSE\t0\tapi-session\tpath-cookie-value" in netscape_path.read_text(), "Netscape export lost core fields")
    runner.call("cookies", "delete", tab, "api-session", "--path", "/api")
    denied_path = runner.call("fetch", origin + "/api/cookie-auth")
    require(denied_path["meta"]["variants"][0]["status"] == 401, "API cookie delete had no effect")
    imported = runner.call("cookies", "import", tab, "@" + str(netscape_path))
    require(imported["meta"]["format"] == "netscape", "Netscape import format was not detected")
    authenticated = runner.call("fetch", origin + "/api/cookie-auth")
    require(authenticated["complete"] and authenticated["meta"]["variants"][0]["status"] == 200, "Netscape restore did not authenticate API path")
    restored_path = runner.call("cookies", "list", tab, "--all-paths")
    path_cookie = next(c for c in restored_path["untrusted"]["items"] if c["name"] == "api-session")
    require(path_cookie["httpOnly"] and path_cookie["sameSite"] == "Strict", "Netscape comments did not preserve browser attributes")

    # A standard full-browser Netscape jar can declare several hosts. The tab
    # chooses the profile; --all-domains must keep each row's original host.
    multi_jar = runner.directory / "multi-domain.txt"
    multi_jar.write_text("# Netscape HTTP Cookie File\n"
        "#HttpOnly_127.0.0.1\tFALSE\t/api\tFALSE\t0\tapi-session\tpath-cookie-value\n"
        "#HttpOnly_localhost\tFALSE\t/api\tFALSE\t0\tapi-session\tpath-cookie-value\n")
    runner.call("cookies", "delete", tab, "api-session", "--path", "/api")
    scope_error = runner.call("cookies", "import", tab, "@" + str(multi_jar), expected=2)
    require("--all-domains" in scope_error["error"]["message"], "multi-domain rejection omitted usable next option")
    full_import = runner.call("cookies", "import", tab, "@" + str(multi_jar), "--all-domains")
    require(full_import["meta"]["count"] == 2, "multi-domain import omitted a row")
    for site in (origin, origin.replace("127.0.0.1", "localhost")):
        site_auth = runner.call("fetch", site + "/api/cookie-auth")
        require(site_auth["complete"] and site_auth["meta"]["variants"][0]["status"] == 200, "multi-domain import retagged a host cookie")

    partition_path = runner.directory / "partition-cookies.json"
    partition_path.write_text(json.dumps([{"name": "partition-session", "value": "partition-value", "secure": True,
        "httpOnly": True, "sameSite": "None", "path": "/", "partitionKey": {
            "topLevelSite": "http://127.0.0.1", "hasCrossSiteAncestor": False}}]))
    runner.call("cookies", "import", tab, "@" + str(partition_path))
    partitioned = runner.call("cookies", "list", tab)
    partition_cookie = next(c for c in partitioned["untrusted"]["items"] if c["name"] == "partition-session")
    require(partition_cookie["partitionKey"]["topLevelSite"] == "http://127.0.0.1", "partition key changed on import")
    partition_jar = runner.directory / "partition-cookies.txt"
    runner.call("cookies", "export", tab, str(partition_jar), "--all-paths", "--format", "netscape")
    runner.call("cookies", "delete", tab, "partition-session", "--domain", partition_cookie["domain"], "--path", partition_cookie["path"], "--partition-key", json.dumps(partition_cookie["partitionKey"]))
    without_partition = runner.call("cookies", "list", tab)
    require(not any(c["name"] == "partition-session" for c in without_partition["untrusted"]["items"]), "partitioned cookie delete had no effect")
    runner.call("cookies", "import", tab, "@" + str(partition_jar))
    restored_partition = runner.call("cookies", "list", tab)
    imported_partition = next(c for c in restored_partition["untrusted"]["items"] if c["name"] == "partition-session")
    require(imported_partition["partitionKey"] == partition_cookie["partitionKey"], "Netscape comment lost partition key")
    runner.call("cookies", "delete", tab, "partition-session", "--domain", partition_cookie["domain"], "--path", partition_cookie["path"], "--partition-key", json.dumps(partition_cookie["partitionKey"]))

    # Invalid batches must not install the valid first row before rejecting later rows.
    invalid_path = runner.directory / "invalid-cookies.json"
    invalid_path.write_text(json.dumps([{"name": "partial-batch", "value": "should-not-exist"}, {"name": "invalid", "value": "x", "httpOnly": "bad"}]))
    runner.call("cookies", "import", tab, "@" + str(invalid_path), expected=2)
    after_invalid = runner.call("cookies", "list", tab)
    require(not any(c["name"] == "partial-batch" for c in after_invalid["untrusted"]["items"]), "invalid batch partially applied")

    runner.call("eval", "localStorage.setItem('access_token','fixture-storage-token'); true", tab)
    expression = "Object.fromEntries(Object.entries(localStorage))"
    normal = runner.call("eval", expression, tab)
    require("fixture-storage-token" not in json.dumps(normal), "normal eval did not fingerprint token")
    storage = runner.directory / "storage.json"
    runner.call("eval", expression, tab, "--out", str(storage))
    artifact(storage, {"access_token": "fixture-storage-token"})
    runner.call("eval", "localStorage.setItem('collision','ran'); true", tab, "--out", str(storage), expected=1)
    unchanged = runner.call("eval", "localStorage.getItem('collision')", tab)
    require(unchanged["untrusted"]["value"] is None, "collision ran mutating script")
    readonly = runner.directory / "readonly"
    readonly.mkdir(mode=0o500)
    try:
        failed = runner.call("eval", "localStorage.setItem('export-count',String(Number(localStorage.getItem('export-count')||0)+1)); true", tab, "--out", str(readonly / "result.json"), expected=1)
        require(failed["status"] == "performed" and not failed["complete"] and failed["error"]["retry"] == "do-not-retry", "late export failure misreported script outcome")
        count = runner.call("eval", "localStorage.getItem('export-count')", tab)
        require(count["untrusted"]["value"] == "1", "late export failure repeated or skipped script")
    finally:
        readonly.chmod(0o700)
    layout = runner.directory / "layout.json"
    runner.call("cdp", "Page.getLayoutMetrics", tab, "--out", str(layout))
    require("cssLayoutViewport" in artifact(layout), "CDP raw result missing layout")
    runner.call("eval", "document.cookie='nameless-cookie-value; Path=/'; true", tab)
    nameless = runner.call("cookies", "list", tab)
    require(any(c["name"] == "" for c in nameless["untrusted"]["items"]), "fixture did not create nameless cookie")
    nameless_jar = runner.directory / "nameless.txt"
    runner.call("cookies", "export", tab, str(nameless_jar), "--format", "netscape")
    runner.call("cookies", "delete", tab, "")
    runner.call("cookies", "import", tab, "@" + str(nameless_jar))
    restored_nameless = runner.call("cookies", "list", tab)
    require(any(c["name"] == "" for c in restored_nameless["untrusted"]["items"]), "nameless cookie did not roundtrip")
    return {"http_only_roundtrip": True, "auth_after_restore": True, "empty_roundtrip": True, "partitioned_import_delete": True,
            "all_paths_auth_export": True, "all_paths_host_isolation": True, "netscape_auth_roundtrip": True,
            "netscape_partition_roundtrip": True,
            "nameless_cookie_roundtrip": True,
            "multi_domain_jar_auth": True,
            "invalid_batch_atomic": True, "sibling_task_isolation": True, "eval_raw_export": True,
            "export_collision_before_execution": True, "late_export_failure_performed_once": True, "cdp_raw_export": True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rep", type=Path, required=True)
    parser.add_argument("--extension", type=Path, default=ROOT.parent / "rep")
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    report = {"passed": False}
    # macOS Unix-domain sockets cannot fit its long default /var/folders path.
    with tempfile.TemporaryDirectory(prefix="rep-cookie-verify-", dir="/tmp") as slot:
        private = Path(slot)
        for key, directory in (("XDG_DATA_HOME", "data"), ("XDG_RUNTIME_DIR", "runtime")):
            path = private / directory
            path.mkdir(mode=0o700)
            os.environ[key] = str(path)
        os.environ["REP_PROFILE"] = "cookie-verify-" + uuid.uuid4().hex[:12]
        os.environ["REP_EXTENSION_PATH"] = str(args.extension.resolve())
        runner = Runner(args.rep)
        try:
            runner.start()
            with serve() as fixture:
                report["checks"] = run(runner, fixture.url)
            report["passed"] = True
        except Exception as error:
            report["failure"] = str(error)
            report["startup_logs"] = {str(path.relative_to(private)): path.read_text(errors="replace")[-12000:]
                                      for path in private.rglob("browser.log")}
            if runner.records:
                next_command = runner.records[-1].get("result", {}).get("error", {}).get("next", "")
                if next_command.startswith("rep doctor"):
                    diagnostic = runner.raw(shlex.split(next_command)[1:])
                    report["diagnostic"] = {"exit": diagnostic.returncode, "stdout": diagnostic.stdout, "stderr": diagnostic.stderr}
            raise
        finally:
            try:
                runner.close()
                report["cleanup"] = "private browser and fixture server stopped"
            except Exception as error:
                report["passed"] = False
                report["cleanup_error"] = str(error)
                raise
            finally:
                report["calls"] = runner.records
                if args.output:
                    args.output.write_text(json.dumps(report, indent=2) + "\n")
                print(json.dumps({key: value for key, value in report.items() if key != "calls"}))


if __name__ == "__main__":
    main()
