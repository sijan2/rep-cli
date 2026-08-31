"""
ULTIMATE Android traffic capture - Streaming UID lookup

Uses a persistent ADB connection with streaming updates from device.
Achieves <20ms latency for port->UID lookups.

Architecture:
1. Spawns `adb shell` subprocess that streams /proc/net/tcp continuously
2. Background thread parses stream and updates port->UID map in real-time
3. mitmproxy hooks use the always-fresh map with zero latency
"""
import json
import os
import hashlib
import subprocess
import threading
import time
from pathlib import Path
from mitmproxy import http, ctx

ANDROID_PATH = os.environ.get("REPANDROID_PATH", os.path.expanduser("~/.local/share/rep-cli/android.json"))
Path(ANDROID_PATH).parent.mkdir(parents=True, exist_ok=True)

# Shared state
port_to_uid = {}
uid_to_package = {}
stream_process = None
running = True

def load_uid_packages():
    """Load UID->package mapping once at startup"""
    global uid_to_package
    try:
        result = subprocess.run(
            ["adb", "shell", "pm list packages -U"],
            capture_output=True, text=True, timeout=10
        )
        for line in result.stdout.strip().split('\n'):
            if 'uid:' in line:
                parts = line.split()
                pkg = parts[0].replace('package:', '')
                uid = int(parts[1].replace('uid:', ''))
                uid_to_package[uid] = pkg
        ctx.log.info(f"Loaded {len(uid_to_package)} packages")
    except Exception as e:
        ctx.log.error(f"Failed to load packages: {e}")

def stream_reader():
    """Read streaming updates from device"""
    global port_to_uid, stream_process, running
    
    # Start streaming process
    stream_process = subprocess.Popen(
        ["adb", "shell", "su -c 'while true; do cat /proc/net/tcp /proc/net/tcp6 2>/dev/null; echo END; sleep 0.02; done'"],
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        text=True,
        bufsize=1
    )
    
    current_batch = {}
    
    for line in stream_process.stdout:
        if not running:
            break
            
        line = line.strip()
        if line == "END":
            # Batch complete, update map atomically
            port_to_uid = current_batch.copy()
            current_batch = {}
            continue
        
        if not line or "local_address" in line:
            continue
            
        parts = line.split()
        if len(parts) < 8:
            continue
            
        try:
            local = parts[1]
            port = int(local.split(':')[1], 16)
            uid = int(parts[7])
            if uid >= 10000:
                current_batch[port] = uid
        except:
            pass

def get_package(port):
    """Get package for port - O(1) lookup"""
    uid = port_to_uid.get(port)
    if uid:
        return uid_to_package.get(uid, f"uid:{uid}")
    return None

# File I/O
def load_data():
    try:
        with open(ANDROID_PATH) as f:
            return json.load(f)
    except:
        return {"version": "3.0", "packages": {}}

def save_data(data):
    data["exported_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    with open(ANDROID_PATH, 'w') as f:
        json.dump(data, f, indent=2)

def extract_domain(url):
    try:
        from urllib.parse import urlparse
        return urlparse(url).netloc
    except:
        return ""

def build_id(flow):
    data = f"{flow.request.method}{flow.request.url}{flow.request.timestamp_start}"
    return hashlib.sha256(data.encode()).hexdigest()[:16]

def headers_to_dict(headers):
    result = {}
    for k, v in headers.items(multi=True):
        result.setdefault(k, []).append(v)
    return result

def request(flow: http.HTTPFlow):
    """Capture package at request time"""
    if flow.client_conn.peername:
        port = flow.client_conn.peername[1]
        pkg = get_package(port)
        if pkg:
            flow.metadata['package'] = pkg

def response(flow: http.HTTPFlow):
    req = flow.request
    res = flow.response
    domain = extract_domain(req.url)
    
    # Get package
    package = flow.metadata.get('package')
    if not package and flow.client_conn.peername:
        package = get_package(flow.client_conn.peername[1])
    if not package and 'x-android-package' in req.headers:
        package = req.headers['x-android-package']
    if not package:
        package = 'unknown'
    
    entry = {
        "id": build_id(flow),
        "method": req.method,
        "url": req.url,
        "headers": headers_to_dict(req.headers),
        "body": req.get_text(strict=False) or "",
        "response": {
            "status": res.status_code,
            "headers": headers_to_dict(res.headers),
            "body": res.get_text(strict=False) or ""
        },
        "timestamp": int(req.timestamp_start * 1000),
        "package": package,
        "domain": domain
    }
    
    data = load_data()
    if package not in data["packages"]:
        data["packages"][package] = {"package": package, "requests": [], "domains": [], "last_seen": 0}
    
    pkg_data = data["packages"][package]
    pkg_data["requests"].append(entry)
    pkg_data["last_seen"] = int(time.time() * 1000)
    if domain and domain not in pkg_data["domains"]:
        pkg_data["domains"].append(domain)
    
    save_data(data)
    ctx.log.info(f"[{package}] {req.method} {req.url[:60]}")

stream_thread = None

def load(loader):
    global stream_thread
    load_uid_packages()
    stream_thread = threading.Thread(target=stream_reader, daemon=True)
    stream_thread.start()
    ctx.log.info("Started streaming UID monitor (20ms updates)")

def done():
    global running, stream_process
    running = False
    if stream_process:
        stream_process.terminate()
