"""
mitmproxy addon - outputs traffic to live.json (same format as Chrome extension)
Usage: mitmdump -s mitm_to_live.py
"""
import json
import os
import hashlib
import time
from pathlib import Path
from mitmproxy import http, ctx

LIVE_PATH = os.environ.get("REPLIVE_PATH", os.path.expanduser("~/.local/share/rep-cli/live.json"))
Path(LIVE_PATH).parent.mkdir(parents=True, exist_ok=True)

requests_data = []

def build_id(flow: http.HTTPFlow) -> str:
    data = f"{flow.request.method}{flow.request.url}{flow.request.timestamp_start}"
    return hashlib.sha256(data.encode()).hexdigest()[:16]

def headers_to_dict(headers) -> dict:
    result = {}
    for k, v in headers.items(multi=True):
        result.setdefault(k, []).append(v)
    return result

def response(flow: http.HTTPFlow):
    req = flow.request
    res = flow.response
    
    entry = {
        "id": build_id(flow),
        "original_id": f"req_{len(requests_data) + 1}",
        "method": req.method,
        "url": req.url,
        "page_url": req.url,
        "resource_type": "",
        "initiator": flow.client_conn.peername[0] if flow.client_conn.peername else "",
        "headers": headers_to_dict(req.headers),
        "body": req.get_text(strict=False) or "",
        "response": {
            "status": res.status_code,
            "headers": headers_to_dict(res.headers),
            "body": res.get_text(strict=False) or ""
        },
        "response_encoding": res.headers.get("content-encoding", ""),
        "timestamp": int(req.timestamp_start * 1000),
        "package": getattr(flow, 'metadata', {}).get('package', 'unknown')
    }
    
    requests_data.append(entry)
    
    with open(LIVE_PATH, 'w') as f:
        json.dump({"requests": requests_data}, f)
    
    ctx.log.info(f"[{len(requests_data)}] {req.method} {req.url[:80]}")
