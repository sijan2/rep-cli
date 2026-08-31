"""
Android traffic capture — lean storage, mobile-safe.

Design goals:
- Media bodies (video/image/audio + HTTP 206 range) stored as metadata only.
  Never keep raw video bytes in the store; the URL + Range + size is enough
  to reason about CDN patterns and re-fetch on demand.
- Heavy fingerprint headers stripped (x-meta-zca, x-fb-session-*, etc.).
  These repeat identically on every IG request and add ~3–4 KB each.
- No base64 body duplication. Bodies stored once, as text. Large text bodies
  are gzip-compressed (base64-wrapped), tiny bodies stored inline.
- Writes are batched, not per-request, so 1000 requests don't rewrite the
  file 1000 times.
"""
import base64
import gzip
import json
import os
import subprocess
import threading
import time
from pathlib import Path
from urllib.parse import urlparse

from mitmproxy import http, ctx

DATA_DIR = os.path.expanduser("~/.local/share/rep-cli")
ANDROID_PATH = os.environ.get("REPANDROID_PATH", f"{DATA_DIR}/android.json")
CONFIG_PATH = f"{DATA_DIR}/android_config.json"
Path(DATA_DIR).mkdir(parents=True, exist_ok=True)

# ---- State -------------------------------------------------------

uid_to_package: dict[int, str] = {}
port_to_uid: dict[int, int] = {}
polling_active = True
app_config: dict = {}

# In-memory store. Flushed to disk periodically (see _flusher).
_data_lock = threading.Lock()
_data: dict = {"version": "3.1", "packages": {}}
_dirty = False

# No header stripping — every header is preserved for analysis.
# Savings come from: compact JSON, no body-b64 duplication, media
# placeholders for CDN bytes, batched writes.
BLOAT_HEADERS: set = set()

# Content types where storing the body is never useful and always huge.
MEDIA_CT_PREFIXES = ("image/", "video/", "audio/", "font/")

# ---- Config + UID mapping ---------------------------------------

def load_config():
    global app_config
    try:
        with open(CONFIG_PATH) as f:
            app_config = json.load(f)
    except Exception:
        app_config = {}

def load_uid_packages():
    global uid_to_package
    try:
        result = subprocess.run(
            ["adb", "shell", "pm list packages -U"],
            capture_output=True, text=True, timeout=10,
        )
        for line in result.stdout.strip().split("\n"):
            if "uid:" in line:
                parts = line.split()
                pkg = parts[0].replace("package:", "")
                uid = int(parts[1].replace("uid:", ""))
                uid_to_package[uid] = pkg
        ctx.log.info(f"Loaded {len(uid_to_package)} packages")
    except Exception as e:
        ctx.log.warn(f"Failed to load packages: {e}")

def poll_proc_net_tcp():
    global port_to_uid
    while polling_active:
        try:
            result = subprocess.run(
                ["adb", "shell", "su -c 'cat /proc/net/tcp /proc/net/tcp6 2>/dev/null'"],
                capture_output=True, text=True, timeout=2,
            )
            new_mapping = {}
            for line in result.stdout.strip().split("\n"):
                parts = line.split()
                if len(parts) < 8 or parts[0] == "sl":
                    continue
                try:
                    local_port = int(parts[1].split(":")[1], 16)
                    uid = int(parts[7])
                    if uid >= 10000:
                        new_mapping[local_port] = uid
                except Exception:
                    pass
            port_to_uid = new_mapping
        except Exception:
            pass
        time.sleep(0.1)

def get_package_for_port(port: int):
    uid = port_to_uid.get(port)
    if uid:
        return uid_to_package.get(uid, f"uid:{uid}")
    return None

def should_capture(package: str, url: str):
    if package not in app_config:
        return True, False
    cfg = app_config[package]
    for pat in cfg.get("skip_patterns", []):
        if pat in url:
            return False, True
    keep = cfg.get("keep_patterns", [])
    if keep and not any(p in url for p in keep):
        return False, True
    return True, False

# ---- Helpers ----------------------------------------------------

def fnv1a64(s: str) -> str:
    h = 0xcbf29ce484222325
    for c in s:
        h ^= ord(c)
        h = (h * 0x100000001b3) & 0xffffffffffffffff
    return format(h, "016x")

def build_id(flow: http.HTTPFlow) -> str:
    ts = int(flow.request.timestamp_start * 1000)
    return f"h_{fnv1a64(f'|{ts}|{flow.request.method}|{flow.request.url}')}"

def lean_headers(headers) -> dict:
    """Return header dict stripped of known-bloat fingerprint headers."""
    out: dict = {}
    for k, v in headers.items(multi=True):
        if k.lower() in BLOAT_HEADERS:
            continue
        out.setdefault(k, []).append(v)
    return out

def smart_body(content: bytes | None, content_type: str) -> tuple[str, str | None]:
    """
    Body policy:
      - media (video/image/audio/font): metadata placeholder only
      - small text (<4 KB): store as-is
      - large text (>=4 KB): gzip+base64 if compression helps >20%, else truncate
      - unknown binary: short placeholder
    Returns (body_field_value, encoding_tag_or_None).
    """
    if not content:
        return "", None
    ct = (content_type or "").lower()
    size = len(content)

    if any(ct.startswith(p) for p in MEDIA_CT_PREFIXES):
        return f"[MEDIA: {size} bytes, {ct.split(';')[0]}]", None

    try:
        text = content.decode("utf-8")
    except UnicodeDecodeError:
        try:
            text = content.decode("latin-1")
        except Exception:
            return f"[BINARY: {size} bytes]", None

    if size < 4000:
        return text, None

    # Always compress + store for large text bodies. Never truncate — losing
    # the tail of a 50 KB clips_items response would drop the video_subtitles_uri
    # and next_min_id fields for stream_comments. Disk cost of base64(gzip) is
    # typically ~40–70% of raw size, well worth it for full-fidelity analysis.
    try:
        compressed = gzip.compress(content, compresslevel=6)
        return base64.b64encode(compressed).decode("ascii"), f"gzip:{size}"
    except Exception:
        # Extreme fallback only — should effectively never hit.
        return text, None

# ---- Store ------------------------------------------------------

def _load_from_disk():
    try:
        with open(ANDROID_PATH) as f:
            loaded = json.load(f)
        if isinstance(loaded, dict) and "packages" in loaded:
            _data.update(loaded)
    except Exception:
        pass

def _add_entry(pkg: str, entry: dict, domain: str, method: str, path: str):
    global _dirty
    with _data_lock:
        packages = _data.setdefault("packages", {})
        if pkg not in packages:
            packages[pkg] = {"package": pkg, "requests": [], "domains": [], "endpoints": []}
        p = packages[pkg]
        p["requests"].append(entry)
        if domain and domain not in p["domains"]:
            p["domains"].append(domain)
        ep = f"{method} {path}"
        if ep not in p["endpoints"]:
            p["endpoints"].append(ep)
        _dirty = True

def _flush_to_disk():
    global _dirty
    with _data_lock:
        if not _dirty:
            return
        _data["exported_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        # Compact JSON — no indentation, no spaces between separators.
        tmp = ANDROID_PATH + ".tmp"
        with open(tmp, "w") as f:
            json.dump(_data, f, separators=(",", ":"))
        os.replace(tmp, ANDROID_PATH)
        _dirty = False

def _flusher():
    """Background thread: flush every 2 seconds if dirty."""
    while polling_active:
        time.sleep(2.0)
        try:
            _flush_to_disk()
        except Exception as e:
            ctx.log.warn(f"flush failed: {e}")

# ---- mitmproxy hooks --------------------------------------------

def request(flow: http.HTTPFlow):
    if flow.client_conn.peername:
        port = flow.client_conn.peername[1]
        pkg = get_package_for_port(port)
        if pkg:
            flow.metadata["package"] = pkg

def response(flow: http.HTTPFlow):
    req = flow.request
    res = flow.response

    package = flow.metadata.get("package", "unknown")
    if package == "unknown" and flow.client_conn.peername:
        port = flow.client_conn.peername[1]
        pkg = get_package_for_port(port)
        if pkg:
            package = pkg
    if package == "unknown" and "x-android-package" in req.headers:
        package = req.headers["x-android-package"]

    url = req.url
    method = req.method
    parsed = urlparse(url)
    domain = parsed.netloc
    path = parsed.path

    _, is_skipped = should_capture(package, url)

    req_ct = req.headers.get("content-type", "")
    res_ct = res.headers.get("content-type", "") if res else ""
    status = res.status_code if res else 0

    req_content = req.get_content(strict=False) or b""
    res_content = res.get_content(strict=False) if res else b""

    entry = {
        "id": build_id(flow),
        "method": method,
        "url": url,
        "domain": domain,
        "path": path,
        "status": status,
        "req_content_type": req_ct,
        "res_content_type": res_ct,
        "req_size": len(req_content),
        "res_size": len(res_content) if res_content else 0,
        "timestamp": int(req.timestamp_start * 1000),
        "package": package,
        "has_auth": any(
            h.lower() in ("authorization", "x-api-key", "cookie")
            for h in req.headers
        ),
        "req_headers": lean_headers(req.headers),
        "res_headers": lean_headers(res.headers) if res else {},
    }

    if is_skipped:
        entry["skipped"] = True
        entry["req_body"] = ""
        entry["res_body"] = f"[SKIPPED: {len(res_content)} bytes]"
    elif status == 206 or any(res_ct.lower().startswith(p) for p in MEDIA_CT_PREFIXES):
        # Range/streaming/media — never store bytes.
        entry["req_body"] = ""
        entry["res_body"] = f"[MEDIA: {len(res_content)} bytes, {res_ct.split(';')[0] or 'binary'}]"
    else:
        rb, re_enc = smart_body(req_content, req_ct)
        sb, se_enc = smart_body(res_content, res_ct)
        entry["req_body"] = rb
        entry["res_body"] = sb
        if re_enc:
            entry["req_body_encoding"] = re_enc
        if se_enc:
            entry["res_body_encoding"] = se_enc

    _add_entry(package, entry, domain, method, path)

    skip_tag = "[SKIP] " if is_skipped else ""
    ctx.log.info(f"{skip_tag}[{package}] {method} {path[:60]} -> {status}")

# ---- Lifecycle --------------------------------------------------

_poll_thread = None
_flush_thread = None

def load(loader):
    global _poll_thread, _flush_thread
    load_config()
    load_uid_packages()
    _load_from_disk()
    _poll_thread = threading.Thread(target=poll_proc_net_tcp, daemon=True)
    _poll_thread.start()
    _flush_thread = threading.Thread(target=_flusher, daemon=True)
    _flush_thread.start()

def done():
    global polling_active
    polling_active = False
    try:
        _flush_to_disk()
    except Exception:
        pass
