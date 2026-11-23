#!/usr/bin/env python3
"""Exercise representative reading, interaction, API and design journeys through rep.

Every browser operation is a rep subprocess. Fixtures bind loopback port 0;
browser state, configuration and artifacts live in a unique private /tmp slot.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import shlex
import shutil
import tempfile
import uuid

from agent_first_fixtures import FILE_BYTES, serve
from agent_first_runner import ROOT, Runner, interaction, lab, reference


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def follow(runner, result, verb="page"):
    """Follow observed continuation commands verbatim, retaining every result."""
    pages = [result]
    for _ in range(100):
        if result.get("complete"):
            return pages
        if not result.get("next"):
            break
        argv = shlex.split(result["next"])
        require(argv[0] == "rep" and argv[1] == verb, "unexpected continuation: " + result["next"])
        completed = runner.raw(argv[1:])
        value = json.loads(completed.stdout)
        runner.records.append({"binary": "rep", "argv": argv[1:], "exit": completed.returncode, "result": value})
        require(completed.returncode == 0, "continuation failed: " + str(value))
        result = value
        pages.append(result)
    if verb == "page" and result.get("tab"):
        runner.call("shot", result["tab"])
    raise AssertionError("read never became complete")


def reading(runner, fixture):
    page = runner.call("page", fixture.url + "/article")
    pages = follow(runner, page)
    text = "\n".join(p.get("untrusted", {}).get("text", "") for p in pages)
    require("two  spaces" in text and "Alpha" in text and "42" in text, "article lost code/table evidence")
    section = runner.call("page", page["tab"], "--section", "Method")
    require("Keep source text" in section["untrusted"]["text"], "section read missed its text")
    paper = runner.call("page", page["tab"], "/paper")
    paper = follow(runner, paper)[-1]
    links = [control for control in paper["untrusted"]["controls"] if control.get("role") == "link"]
    source = next(control for control in links if control.get("name") == "Source two")
    opened = runner.call("do", source["ref"])
    observed = runner.call("page", paper["tab"])
    observed = follow(runner, observed)[-1]
    require("Product 10" in observed["untrusted"]["text"], "observed source link did not open product table")
    long_page = runner.call("page", page["tab"], fixture.url + "/long")
    chunks = follow(runner, long_page)
    require(len(chunks) > 1, "long document did not exercise a continuation")
    whole = "\n".join(chunk["untrusted"].get("text", "") for chunk in chunks)
    require(all("Paragraph " + str(i) + ":" in whole for i in range(90)), "pagination omitted document paragraphs")
    return {"code_table_preserved": True, "section_read": True, "observed_source_link": True,
            "long_document_chunks": len(chunks), "complete_paragraphs": 90}


def network(runner, fixture):
    page = runner.call("page", fixture.url + "/api", "--capture")
    follow(runner, page)
    requests = runner.call("net", "type:api")
    rows = requests["untrusted"]["requests"]
    source = next(row for row in rows if row["path"] == "/api/items")
    handle = source["id"]
    body = runner.call("body", handle)
    require(body["complete"] and body["meta"]["source_archive"] == requests["meta"]["source_archive"], "body lost capture provenance")
    require("fixture-access-token" not in json.dumps(body), "default body exposed credential field")
    projected = runner.call("body", handle, "/data/0/price")
    require("42" in json.dumps(projected["untrusted"]), "body pointer did not select price")
    raw_path = runner.directory / "captured-response.json"
    runner.call("body", handle, "--out", str(raw_path))
    require(json.loads(raw_path.read_text())["access_token"] == "fixture-access-token", "raw captured body lost original token")
    exported_path = runner.directory / "captured-request.json"
    before_export = dict(fixture.counts)
    runner.call("export", handle, str(exported_path))
    exported = json.loads(exported_path.read_text())
    headers = {name.lower(): values for name, values in exported["record"]["headers"].items()}
    require(headers["authorization"] == ["Bearer fixture-auth-token"] and headers["x-csrf-token"] == ["fixture-csrf-token"], "request export lost original authentication headers")
    require(json.loads(base64.b64decode(exported["response_body"]["data"]))["access_token"] == "fixture-access-token", "request export lost raw response bytes")
    require(exported["source_archive"] == requests["meta"]["source_archive"] and exported["complete"], "request export lost source completeness/provenance")
    curl_path = runner.directory / "captured-request.sh"
    runner.call("export", handle, str(curl_path), "--format", "curl")
    require("fixture-auth-token" in curl_path.read_text() and "fixture-csrf-token" in curl_path.read_text(), "curl export lost authentication headers")
    require(fixture.counts == before_export, "export unexpectedly sent a request")
    replay = runner.call("replay", handle, "--set", "query.q=beta")
    require(replay["complete"] and "beta" in replay["untrusted"]["body"], "captured GET mutation failed")
    matrix = runner.call("replay", handle, "--matrix", 'query.q=["gamma","delta"]')
    require(matrix["complete"] and matrix["meta"]["summary"]["succeeded"] == 2, "GET matrix did not complete both variants")
    signed_in = runner.call("page", page["tab"], fixture.url + "/download", "--capture")
    follow(runner, signed_in)
    downloads = runner.call("net", signed_in["meta"]["capture"]["id"], "has:/file.pdf")
    file_handle = next(row["id"] for row in downloads["untrusted"]["requests"] if row["status"] == 200)
    path = runner.directory / "captured.pdf"
    saved = runner.call("download", file_handle, str(path))
    require(path.read_bytes() == FILE_BYTES and saved["meta"]["sha256"] == hashlib.sha256(FILE_BYTES).hexdigest(), "captured download differs from source bytes")
    require(path.stat().st_mode & 0o777 == 0o600, "download is not private")
    direct = runner.directory / "direct.pdf"
    runner.call("download", fixture.url + "/file.pdf", str(direct))
    require(direct.read_bytes() == FILE_BYTES, "URL download did not reuse browser authentication")
    return {"capture_net_body": True, "body_pointer": True, "raw_body_export": True,
            "raw_request_export": True, "curl_export_without_execution": True, "get_replay": True,
            "matrix_variants": 2, "captured_download": True, "authenticated_url_download": True}


def screenshots(runner, fixture):
    page = runner.call("page", fixture.url + "/reference", "--viewport", "mobile")
    follow(runner, page)
    shot = runner.call("shot", page["tab"], str(runner.directory / "mobile.png"))
    require(shot["meta"]["dimensions"]["width"] == 390, "sticky mobile viewport missing from screenshot")
    focused = runner.call("shot", page["tab"], str(runner.directory / "heading.png"), "--selector", "h1")
    require(focused["meta"]["dimensions"]["height"] > 0, "selector screenshot is empty")
    urls = runner.directory / "urls.txt"
    urls.write_text("\n".join(fixture.url + "/sweep/" + str(i) for i in range(3)))
    sweep = runner.call("shot", "--urls", "@" + str(urls), "--viewport", "mobile")
    require(sweep["complete"], "responsive sweep is incomplete")
    return {"sticky_mobile_viewport": True, "selector_screenshot": True, "responsive_sweep": True}


def thread(runner, fixture):
    page = runner.call("page", fixture.url + "/thread", "--capture")
    follow(runner, page)
    capture = page["meta"]["capture"]["id"]
    before = dict(fixture.counts)
    decoded = runner.call("records", capture)
    rows = decoded["untrusted"]["records"]
    require(len(rows) == 2 and "First retained comment" in json.dumps(rows) and "Second retained reply" in json.dumps(rows), "captured thread records are missing")
    require(not decoded["complete"] and decoded["meta"]["coverage"]["code"] == "frontier_pending", "pending thread continuation was labelled complete")
    require(decoded["meta"]["frontier"]["total"] == 1, "thread continuation was omitted")
    require(fixture.counts == before, "records fetched a continuation instead of decoding captured evidence")
    return {"captured_records": 2, "pending_continuation_reported": True, "no_implicit_requests": True}


def writes(runner, fixture):
    page = runner.call("page", fixture.url + "/form")
    follow(runner, page)
    captured = runner.call("eval", "fetch('/reject-write',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({value:0})}).then(r=>r.status)", page["tab"], "--capture")
    capture = captured["meta"]["capture"]["id"]
    traffic = runner.call("net", capture, "POST")
    handle = traffic["untrusted"]["requests"][0]["id"]
    before = fixture.counts.get("/reject-write", 0)
    denied = runner.call("replay", handle, "--matrix", "body.value=[1,2,3]", expected=1)
    require(denied["error"]["code"] == "write_required" and fixture.counts.get("/reject-write", 0) == before, "write replay dispatched before explicit write request")
    result = runner.call("replay", handle, "--matrix", "body.value=[1,2,3]", "--write")
    summary = result["meta"]["summary"]
    require(not result["complete"] and summary["attempted"] == 1 and summary["retries"] == 0 and summary["stopped"], "failed write did not stop the matrix")
    require(fixture.counts.get("/reject-write", 0) == before + 1, "write was repeated or later variants were dispatched")
    return {"explicit_write_gate": True, "stop_after_first_failure": True, "no_repeated_write": True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rep", type=Path, required=True)
    parser.add_argument("--extension", type=Path, default=ROOT.parent / "rep")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    report = {"passed": False, "scenarios": {}}
    with tempfile.TemporaryDirectory(prefix="rep-journeys-", dir="/tmp") as slot:
        for key, name in (("XDG_DATA_HOME", "data"), ("XDG_RUNTIME_DIR", "runtime")):
            path = Path(slot) / name
            path.mkdir(mode=0o700)
            os.environ[key] = str(path)
        os.environ["REP_PROFILE"] = "journey-" + uuid.uuid4().hex[:12]
        os.environ["REP_EXTENSION_PATH"] = str(args.extension.resolve())
        runner = Runner(args.rep)
        try:
            runner.start()
            with serve() as fixture:
                for name, scenario, needs_fixture in (("reading", reading, True), ("interaction", interaction, False),
                    ("network", network, True), ("thread", thread, True), ("reference", reference, False),
                    ("screenshots", screenshots, True), ("lab", lab, False), ("writes", writes, True)):
                    report["scenarios"][name] = scenario(runner, fixture if needs_fixture else fixture.url)
                    for tab in sorted(runner.tabs):
                        runner.call("close", tab)
            report["passed"] = True
        except Exception as error:
            report["failure"] = str(error)
            raise
        finally:
            retained = args.output.parent / (args.output.stem + "-artifacts") / runner.task
            report["artifacts"] = []
            for record in runner.records:
                if record["argv"][0] not in ("shot", "reference"):
                    continue
                meta = record["result"].get("meta", {})
                assets = meta.get("images", [meta.get("screenshot", meta)])
                for asset in assets:
                    if not isinstance(asset, dict) or not asset.get("path"):
                        continue
                    path = Path(asset["path"])
                    if not path.is_file():
                        continue
                    retained.mkdir(parents=True, exist_ok=True)
                    digest = hashlib.sha256(path.read_bytes()).hexdigest()
                    destination = retained / (digest + path.suffix)
                    shutil.copyfile(path, destination)
                    destination.chmod(0o600)
                    report["artifacts"].append({"path": str(destination), "sha256": digest, "source_command": record["argv"]})
            try:
                runner.close()
                report["cleanup"] = "private browser and loopback fixture server stopped"
            except Exception as error:
                report["passed"] = False
                report["cleanup_error"] = str(error)
                raise
            finally:
                report["calls"] = runner.records
                args.output.write_text(json.dumps(report, indent=2) + "\n")
                print(json.dumps({key: value for key, value in report.items() if key != "calls"}))


if __name__ == "__main__":
    main()
