"""
mitmproxy addon - outputs Android traffic to android.json grouped by package
Uses multiple strategies for package identification:
1. X-Android-Package header (most accurate)
2. Domain-to-package mapping (learned + known patterns)
3. Real-time /proc/net/tcp UID lookup via ADB
"""
import json
import os
import hashlib
import subprocess
import time
import re
from pathlib import Path
from mitmproxy import http, ctx

ANDROID_PATH = os.environ.get("REPANDROID_PATH", os.path.expanduser("~/.local/share/rep-cli/android.json"))
Path(ANDROID_PATH).parent.mkdir(parents=True, exist_ok=True)

# Known domain patterns -> package
DOMAIN_PATTERNS = {
    # Music/Media
    r'spotify': 'com.spotify.music',
    r'scdn\.co': 'com.spotify.music',
    r'spotifycdn': 'com.spotify.music',
    r'youtube': 'com.google.android.youtube',
    r'netflix': 'com.netflix.mediaclient',
    r'audible': 'com.audible.application',
    # Social
    r'twitter\.com': 'com.twitter.android',
    r'twimg\.com': 'com.twitter.android',
    r'x\.com': 'com.twitter.android',
    r'facebook\.com': 'com.facebook.katana',
    r'fbcdn': 'com.facebook.katana',
    r'instagram\.com': 'com.instagram.android',
    r'cdninstagram': 'com.instagram.android',
    r'whatsapp': 'com.whatsapp',
    r'snapchat': 'com.snapchat.android',
    r'tiktok': 'com.zhiliaoapp.musically',
    r'linkedin': 'com.linkedin.android',
    # Food
    r'chipotle': 'com.chipotle.ordering',
    r'doordash': 'com.dd.doordash',
    r'grubhub': 'com.grubhub.android',
    r'ubereats': 'com.ubercab.eats',
    # Travel
    r'airbnb': 'com.airbnb.android',
    r'muscache': 'com.airbnb.android',
    r'kayak': 'com.kayak.android',
    r'uber\.com': 'com.ubercab',
    r'lyft\.com': 'com.lyft.android',
    # Finance
    r'venmo': 'com.venmo',
    r'cashapp|cash\.app': 'com.squareup.cash',
    r'robinhood': 'com.robinhood.android',
    r'coinbase': 'com.coinbase.android',
    # Health/Fitness
    r'whoop': 'com.whoop.android',
    r'strava': 'com.strava',
    r'fitbit': 'com.fitbit.FitbitMobile',
    # Shopping
    r'amazon\.com': 'com.amazon.mShop.android.shopping',
    r'slack': 'com.Slack',
}

# Learned mappings (domain -> package) from X-Android-Package headers
learned_domains = {}

# UID -> package cache
uid_to_package = {}

def load_data():
    try:
        with open(ANDROID_PATH) as f:
            data = json.load(f)
            # Load learned domains
            global learned_domains
            learned_domains = data.get('_learned_domains', {})
            return data
    except:
        return {"version": "1.0", "packages": {}, "_learned_domains": {}}

def save_data(data):
    data["exported_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    data["_learned_domains"] = learned_domains
    with open(ANDROID_PATH, 'w') as f:
        json.dump(data, f, indent=2)

def get_uid_packages():
    """Get UID to package mapping from device (cached)"""
    global uid_to_package
    if uid_to_package:
        return uid_to_package
    try:
        result = subprocess.run(
            ["adb", "shell", "pm list packages -U"],
            capture_output=True, text=True, timeout=5
        )
        for line in result.stdout.strip().split('\n'):
            if 'uid:' in line:
                parts = line.split()
                pkg = parts[0].replace('package:', '')
                uid = int(parts[1].replace('uid:', ''))
                uid_to_package[uid] = pkg
        ctx.log.info(f"Loaded {len(uid_to_package)} package UIDs")
    except Exception as e:
        ctx.log.warn(f"Failed to get packages: {e}")
    return uid_to_package

def guess_package_from_domain(domain):
    """Guess package from domain using patterns and learned mappings"""
    domain_lower = domain.lower()
    
    # Check learned mappings first
    if domain in learned_domains:
        return learned_domains[domain]
    
    # Check known patterns
    for pattern, pkg in DOMAIN_PATTERNS.items():
        if re.search(pattern, domain_lower):
            return pkg
    
    return None

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
    
    # Strategy 1: X-Android-Package header (most reliable)
    package = None
    if 'x-android-package' in req.headers:
        package = req.headers['x-android-package']
        # Learn this domain mapping
        if domain and domain not in learned_domains:
            learned_domains[domain] = package
            ctx.log.info(f"Learned: {domain} -> {package}")
    
    # Strategy 2: Domain pattern matching
    if not package:
        package = guess_package_from_domain(domain)
    
    # Strategy 3: User-Agent hints
    if not package:
        ua = req.headers.get('user-agent', '').lower()
        if 'spotify' in ua:
            package = 'com.spotify.music'
        elif 'twitter' in ua:
            package = 'com.twitter.android'
    
    # Fallback
    if not package:
        package = 'unknown'
    
    entry = {
        "id": build_id(flow),
        "original_id": f"android_{int(time.time()*1000)}",
        "method": req.method,
        "url": req.url,
        "page_url": "",
        "resource_type": "xhr",
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
        "package": package,
        "domain": domain
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

# Initialize UID cache on load
def load(loader):
    get_uid_packages()
