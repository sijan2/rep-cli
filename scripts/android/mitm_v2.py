"""
Android traffic capture with device-side socket monitoring

Uses a daemon on the device that continuously maps ports to processes,
giving near-100% accurate package identification.

Requires: adb shell su -c 'nohup /data/local/tmp/socket_monitor.sh &'
"""
import json
import os
import hashlib
import subprocess
import threading
import time
import re
from pathlib import Path
from mitmproxy import http, ctx

ANDROID_PATH = os.environ.get("REPANDROID_PATH", os.path.expanduser("~/.local/share/rep-cli/android.json"))
Path(ANDROID_PATH).parent.mkdir(parents=True, exist_ok=True)

# Caches
port_to_process = {}
process_to_package = {}
polling_active = True

def expand_process_name(proc_name):
    """Expand truncated process name to full package"""
    if proc_name in process_to_package:
        return process_to_package[proc_name]
    
    # Common patterns
    expansions = {
        'm.spotify.music': 'com.spotify.music',
        'stagram.android': 'com.instagram.android',
        'am.android:fbns': 'com.facebook.orca',
        '.gms.persistent': 'com.google.android.gms',
        'm.airbnb.android': 'com.airbnb.android',
        'm.whoop.android': 'com.whoop.android',
        'm.kayak.android': 'com.kayak.android',
        'm.twitter.android': 'com.twitter.android',
        'itter.android': 'com.twitter.android',
        'le.ordering': 'com.chipotle.ordering',
    }
    
    for pattern, pkg in expansions.items():
        if pattern in proc_name:
            process_to_package[proc_name] = pkg
            return pkg
    
    # Try pm list lookup
    try:
        suffix = proc_name.split('.')[-1] if '.' in proc_name else proc_name
        result = subprocess.run(
            ["adb", "shell", f"pm list packages 2>/dev/null | grep -i {suffix} | head -1"],
            capture_output=True, text=True, timeout=1
        )
        if result.stdout.strip():
            pkg = result.stdout.strip().replace('package:', '')
            process_to_package[proc_name] = pkg
            return pkg
    except:
        pass
    
    # Fallback: expand m. to com.
    if proc_name.startswith('m.'):
        pkg = 'com.' + proc_name[2:]
        process_to_package[proc_name] = pkg
        return pkg
    
    process_to_package[proc_name] = proc_name
    return proc_name

def poll_device_cache():
    """Read socket mappings from device cache file"""
    global port_to_process
    
    while polling_active:
        try:
            result = subprocess.run(
                ["adb", "shell", "su -c 'cat /data/local/tmp/socket_map.txt 2>/dev/null'"],
                capture_output=True, text=True, timeout=1
            )
            
            new_mapping = {}
            for line in result.stdout.strip().split('\n'):
                parts = line.split(None, 1)
                if len(parts) == 2:
                    try:
                        port = int(parts[0])
                        proc = parts[1].strip()
                        new_mapping[port] = proc
                    except:
                        pass
            
            if new_mapping:
                port_to_process = new_mapping
                
        except:
            pass
        
        time.sleep(0.1)  # Fast polling since device does the heavy lifting

def load_data():
    try:
        with open(ANDROID_PATH) as f:
            return json.load(f)
    except:
        return {"version": "2.0", "packages": {}}

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
    domain = extract_domain(req.url)
    
    # Get package from source port
    package = "unknown"
    source_port = None
    
    if flow.client_conn.peername:
        source_port = flow.client_conn.peername[1]
        
        if source_port in port_to_process:
            proc_name = port_to_process[source_port]
            package = expand_process_name(proc_name)
    
    # Fallback: X-Android-Package header
    if package == "unknown" and 'x-android-package' in req.headers:
        package = req.headers['x-android-package']
    
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

poll_thread = None

def load(loader):
    global poll_thread
    
    # Ensure device monitor is running
    subprocess.run(["adb", "shell", "su -c 'pkill -f socket_monitor || true'"], capture_output=True)
    subprocess.run(["adb", "shell", "su -c 'nohup /data/local/tmp/socket_monitor.sh > /dev/null 2>&1 &'"], capture_output=True)
    
    ctx.log.info("Started device socket monitor")
    poll_thread = threading.Thread(target=poll_device_cache, daemon=True)
    poll_thread.start()

def done():
    global polling_active
    polling_active = False
    subprocess.run(["adb", "shell", "su -c 'pkill -f socket_monitor'"], capture_output=True)
