"""
Android traffic capture with /proc/net/tcp UID lookup

This is the most accurate approach:
1. Read /proc/net/tcp to get port -> UID mapping
2. Use pre-cached UID -> package mapping
3. No timing issues since /proc/net/tcp is always current
"""
import json
import os
import subprocess
import threading
import time
from pathlib import Path
from mitmproxy import http, ctx

ANDROID_PATH = os.environ.get("REPANDROID_PATH", os.path.expanduser("~/.local/share/rep-cli/android.json"))
Path(ANDROID_PATH).parent.mkdir(parents=True, exist_ok=True)

# Smart limits - capture everything but keep it lightweight
MAX_BODY_SIZE = 10000  # 10KB - enough for AI to analyze
MAX_BINARY_PREVIEW = 0  # Don't store binary at all

# UID -> package (loaded once at startup)
uid_to_package = {}
# port -> UID (refreshed continuously from /proc/net/tcp)
port_to_uid = {}
polling_active = True

def load_uid_packages():
    """Load UID to package mapping from device"""
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
        ctx.log.info(f"Loaded {len(uid_to_package)} package UIDs")
    except Exception as e:
        ctx.log.warn(f"Failed to load packages: {e}")

def poll_proc_net_tcp():
    """Continuously read /proc/net/tcp to map ports to UIDs"""
    global port_to_uid
    
    while polling_active:
        try:
            # Read both tcp and tcp6
            result = subprocess.run(
                ["adb", "shell", "su -c 'cat /proc/net/tcp /proc/net/tcp6 2>/dev/null'"],
                capture_output=True, text=True, timeout=2
            )
            
            new_mapping = {}
            for line in result.stdout.strip().split('\n'):
                parts = line.split()
                if len(parts) < 8 or parts[0] == 'sl':
                    continue
                
                try:
                    # local_address is in format IP:PORT (hex)
                    local = parts[1]
                    local_port = int(local.split(':')[1], 16)
                    uid = int(parts[7])
                    if uid >= 10000:  # App UIDs start at 10000
                        new_mapping[local_port] = uid
                except:
                    pass
            
            port_to_uid = new_mapping
            
        except:
            pass
        
        time.sleep(0.05)  # 50ms polling - very fast

def get_package_for_port(port):
    """Get package name for a source port"""
    uid = port_to_uid.get(port)
    if uid:
        return uid_to_package.get(uid, f"uid:{uid}")
    return None

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

def fnv1a64(s: str) -> str:
    """FNV-1a 64-bit hash - matches Chrome extension"""
    FNV_OFFSET = 0xcbf29ce484222325
    FNV_PRIME = 0x100000001b3
    FNV_MASK = 0xffffffffffffffff
    h = FNV_OFFSET
    for c in s:
        h ^= ord(c)
        h = (h * FNV_PRIME) & FNV_MASK
    return format(h, '016x')

def build_id(flow: http.HTTPFlow) -> str:
    """Build stable ID matching Chrome extension format"""
    ts = int(flow.request.timestamp_start * 1000)
    raw = f"|{ts}|{flow.request.method}|{flow.request.url}"
    return f"h_{fnv1a64(raw)}"

def classify_request(req, res, domain: str) -> str:
    """Auto-classify request type based on signals. Returns: api|media|telemetry|static|unknown"""
    url = req.url.lower()
    ct = res.headers.get('content-type', '').lower() if res else ''
    method = req.method
    status = res.status_code if res else 0
    
    # Media/streaming signals
    if status == 206:  # Partial content = streaming
        return 'media'
    if any(x in ct for x in ['video/', 'audio/', 'octet-stream']):
        return 'media'
    if any(x in domain for x in ['video', 'stream', 'cdn', 'media', 'oca.', 'akamai', 'cloudfront', 'fastly']):
        return 'media'
    
    # Telemetry/analytics signals
    telemetry_patterns = ['event', 'track', 'log', 'metric', 'analytic', 'telemetry', 'crash', 
                          'measure', 'beacon', 'ping', 'heartbeat', 'report']
    if any(p in url for p in telemetry_patterns):
        return 'telemetry'
    telemetry_domains = ['amplitude', 'segment', 'mixpanel', 'firebase', 'appsflyer', 'adjust', 
                         'branch', 'kochava', 'singular', 'datadoghq', 'newrelic', 'sentry',
                         'crashlytics', 'bugsnag', 'instabug', 'fullstory', 'hotjar']
    if any(d in domain for d in telemetry_domains):
        return 'telemetry'
    
    # Static asset signals
    static_ext = ['.png', '.jpg', '.jpeg', '.gif', '.webp', '.svg', '.ico', '.woff', '.woff2', 
                  '.ttf', '.css', '.mp4', '.mp3', '.wav', '.webm']
    if any(url.endswith(ext) or f'{ext}?' in url for ext in static_ext):
        return 'static'
    if any(x in ct for x in ['image/', 'font/']):
        return 'static'
    
    # API signals (high value)
    if any(x in ct for x in ['json', 'xml', 'protobuf', 'grpc']):
        return 'api'
    if method in ['POST', 'PUT', 'DELETE', 'PATCH']:
        return 'api'
    if any(x in url for x in ['/api/', '/v1/', '/v2/', '/v3/', '/graphql', '/rest/', '/rpc/']):
        return 'api'
    
    return 'unknown'

def headers_to_dict(headers) -> dict:
    result = {}
    for k, v in headers.items(multi=True):
        result.setdefault(k, []).append(v)
    return result

def is_binary_content(content_type: str) -> bool:
    """Check if content type indicates binary data"""
    if not content_type:
        return False
    ct = content_type.lower()
    text_types = ['text/', 'application/json', 'application/xml', 'application/javascript',
                  'application/x-www-form-urlencoded', 'multipart/form-data']
    return not any(t in ct for t in text_types)

def get_body(flow_part, is_request=True) -> tuple:
    """Get body and encoding, handling binary safely. Truncates large bodies."""
    import base64
    
    try:
        content = flow_part.get_content(strict=False)
        if not content:
            return "", None
        
        # Check content type
        ct = flow_part.headers.get('content-type', '')
        
        # Try to decode as text first
        try:
            text = content.decode('utf-8')
            # Check for binary indicators even if UTF-8 succeeded
            if '\x00' in text or is_binary_content(ct):
                # Binary - just note the size, don't store
                return f"[BINARY: {len(content)} bytes, {ct}]", None
            # Truncate large text bodies
            if len(text) > MAX_BODY_SIZE:
                return text[:MAX_BODY_SIZE] + f"\n[TRUNCATED: {len(text)} total bytes]", None
            return text, None
        except UnicodeDecodeError:
            # Binary content - just note the size
            return f"[BINARY: {len(content)} bytes, {ct}]", None
    except:
        return "", None

def request(flow: http.HTTPFlow):
    """Capture package at REQUEST time (before connection might close)"""
    if flow.client_conn.peername:
        port = flow.client_conn.peername[1]
        pkg = get_package_for_port(port)
        if pkg:
            flow.metadata['package'] = pkg

def response(flow: http.HTTPFlow):
    req = flow.request
    res = flow.response
    domain = extract_domain(req.url)
    
    # Auto-classify request type based on signals
    req_type = classify_request(req, res, domain)
    
    # Get package - prefer metadata captured at request time
    package = flow.metadata.get('package', 'unknown')
    
    # Fallback: try again at response time
    if package == 'unknown' and flow.client_conn.peername:
        port = flow.client_conn.peername[1]
        pkg = get_package_for_port(port)
        if pkg:
            package = pkg
    
    # Fallback: X-Android-Package header
    if package == 'unknown' and 'x-android-package' in req.headers:
        package = req.headers['x-android-package']
    
    # Get bodies - skip for media, truncate for others
    if req_type == 'media':
        req_body, res_body = "", f"[MEDIA: {res.headers.get('content-length', '?')} bytes]"
    else:
        req_body, _ = get_body(req, is_request=True)
        res_body, _ = get_body(res, is_request=False)
    
    entry = {
        "id": build_id(flow),
        "method": req.method,
        "url": req.url,
        "headers": headers_to_dict(req.headers),
        "body": req_body,
        "response": {
            "status": res.status_code,
            "headers": headers_to_dict(res.headers),
            "body": res_body
        },
        "timestamp": int(req.timestamp_start * 1000),
        "package": package,
        "domain": domain,
        "type": req_type  # api|media|telemetry|static|unknown
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
    load_uid_packages()
    poll_thread = threading.Thread(target=poll_proc_net_tcp, daemon=True)
    poll_thread.start()
    ctx.log.info("Started /proc/net/tcp polling")

def done():
    global polling_active
    polling_active = False
