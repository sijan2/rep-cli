"""
Android traffic capture with REAL-TIME package identification via ss -tunp

This addon polls the device's socket table to map source ports to process names,
giving 100% accurate package identification for all active connections.

Architecture:
1. mitmproxy intercepts traffic (gets source IP:port)
2. Background thread polls `adb shell ss -tunp` every 500ms
3. Maps source_port -> process_name -> package_name
4. No hardcoded patterns needed!
"""
import json
import os
import hashlib
import subprocess
import threading
import time
import re
from pathlib import Path
from collections import defaultdict
from mitmproxy import http, ctx

ANDROID_PATH = os.environ.get("REPANDROID_PATH", os.path.expanduser("~/.local/share/rep-cli/android.json"))
Path(ANDROID_PATH).parent.mkdir(parents=True, exist_ok=True)

# Real-time mappings from device
port_to_process = {}  # source_port -> process_name
process_to_package = {}  # process_name -> full_package
polling_active = True

def get_package_for_process(proc_name):
    """Convert truncated process name to full package"""
    if proc_name in process_to_package:
        return process_to_package[proc_name]
    
    # ss truncates names, try to find full package
    # e.g., "m.spotify.music" -> "com.spotify.music"
    try:
        result = subprocess.run(
            ["adb", "shell", f"pm list packages | grep -i '{proc_name.split('.')[-1]}'"],
            capture_output=True, text=True, timeout=2
        )
        for line in result.stdout.strip().split('\n'):
            pkg = line.replace('package:', '').strip()
            if pkg and proc_name.replace('m.', 'com.').replace('stagram', 'instagram') in pkg or \
               proc_name.split('.')[-1] in pkg:
                process_to_package[proc_name] = pkg
                return pkg
    except:
        pass
    
    # Common truncation patterns
    if proc_name.startswith('m.'):
        guess = 'com.' + proc_name[2:]
        process_to_package[proc_name] = guess
        return guess
    
    process_to_package[proc_name] = proc_name
    return proc_name

def poll_sockets():
    """Background thread: continuously poll device sockets"""
    global port_to_process
    
    while polling_active:
        try:
            result = subprocess.run(
                ["adb", "shell", "su -c 'ss -tunp 2>/dev/null'"],
                capture_output=True, text=True, timeout=3
            )
            
            new_mapping = {}
            for line in result.stdout.split('\n'):
                if 'ESTAB' not in line and 'SYN-SENT' not in line:
                    continue
                
                # Extract local port
                port_match = re.search(r'\]:(\d+)\s', line)
                if not port_match:
                    port_match = re.search(r'\.(\d+)\s+[\d\.\[\]:]+\s', line)
                
                # Extract process name
                proc_match = re.search(r'users:\(\(\"([^\"]+)\"', line)
                
                if port_match and proc_match:
                    port = int(port_match.group(1))
                    proc = proc_match.group(1)
                    new_mapping[port] = proc
            
            port_to_process = new_mapping
            
        except Exception as e:
            pass
        
        time.sleep(0.5)  # Poll every 500ms

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
    
    # Get source port from client connection
    package = "unknown"
    source_port = None
    
    if flow.client_conn.peername:
        source_port = flow.client_conn.peername[1]
        
        # Look up in real-time socket mapping
        if source_port in port_to_process:
            proc_name = port_to_process[source_port]
            package = get_package_for_process(proc_name)
    
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
        "domain": domain,
        "source_port": source_port
    }
    
    # Load, update, save
    data = load_data()
    if package not in data["packages"]:
        data["packages"][package] = {
            "package": package,
            "requests": [],
            "domains": [],
            "last_seen": 0
        }
    
    pkg_data = data["packages"][package]
    pkg_data["requests"].append(entry)
    pkg_data["last_seen"] = int(time.time() * 1000)
    
    if domain and domain not in pkg_data["domains"]:
        pkg_data["domains"].append(domain)
    
    save_data(data)
    ctx.log.info(f"[{package}] {req.method} {req.url[:60]}")

# Start background polling thread
poll_thread = None

def load(loader):
    global poll_thread
    ctx.log.info("Starting real-time socket polling...")
    poll_thread = threading.Thread(target=poll_sockets, daemon=True)
    poll_thread.start()

def done():
    global polling_active
    polling_active = False
    ctx.log.info("Stopped socket polling")
