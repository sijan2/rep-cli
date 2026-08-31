# Android Traffic Analysis - AI Agent Skills

This document describes how to use `rep android` commands for mobile app security analysis. Designed for AI agents to efficiently capture, filter, and analyze Android app traffic.

## Prerequisites

- Android device with root (Magisk) and proxy configured to host:8080
- mitmproxy installed on host
- Device certificate installed for HTTPS interception

## Quick Start

```bash
# 1. Install capture script (one-time)
rep android capture install-script

# 2. Start capture (from any directory!)
rep android capture

# 3. Use the app on device

# 4. Stop capture (Ctrl+C or new terminal)
rep android capture stop

# 5. Review traffic
rep android summary <package>

# 6. Configure filtering (AI decides what's noise)
rep android config skip <package> <pattern>

# 7. Analyze
rep android <package>
rep body <request-id>
```

## Commands Reference

---

### `rep android capture`

**Purpose:** Start mitmproxy capture without needing to know script paths.

**Install script (one-time):**
```bash
rep android capture install-script
```

**Start capture (foreground):**
```bash
rep android capture
# Press Ctrl+C to stop
```

**Start capture (background):**
```bash
rep android capture --bg
# Runs in background, use 'rep android capture stop' to stop
```

**Different port:**
```bash
rep android capture -p 8888
```

**Stop capture:**
```bash
rep android capture stop
```

---

### `rep android summary [package]`

**Purpose:** AI-friendly traffic overview for first-pass analysis.

**Without package argument - shows all packages:**
```
┌─────────────────────────────────────────────────────────────┐
│                    Android Traffic Summary                   │
└─────────────────────────────────────────────────────────────┘

PACKAGES:
  com.netflix.mediaclient                         14 reqs   7 domains [CONFIGURED]
  com.spotify.music                               23 reqs   5 domains
  com.amazon.dee.app                               2 reqs   1 domains

Use: rep android summary <package>
```

**With package argument - detailed breakdown:**
```
┌─────────────────────────────────────────────────────────────┐
│  com.netflix.mediaclient                                    │
├─────────────────────────────────────────────────────────────┤
│  Total Requests: 14       Unique Domains: 7                │
└─────────────────────────────────────────────────────────────┘

METHODS:
  POST     9
  GET      5

STATUS:
  2xx      11
  1xx      3

DOMAINS:
  ┌────────┬──────────────────────────────────────────────────────┐
  │ Reqs   │ Domain                                               │
  ├────────┼──────────────────────────────────────────────────────┤
  │      4 │ android16.prod.cloud.netflix.com                     │
  │      3 │ android16.prod.ftl.netflix.com                       │
  │      1 │ sessions.bugsnag.com                                 │
  └────────┴──────────────────────────────────────────────────────┘

ENDPOINTS:
  ┌────────┬────────┬─────────────────────────────────────────────┐
  │ Reqs   │ Method │ Path                                        │
  ├────────┼────────┼─────────────────────────────────────────────┤
  │      6 │ POST   │ /graphql                                   *│
  │      2 │ GET    │ /wolfboot                                  *│
  │      1 │ POST   │ /android/7.64/api                          │
  └────────┴────────┴─────────────────────────────────────────────┘
  * = has auth headers (4 endpoints)

CURRENT CONFIG:
  skip: [nflxvideo.net /playapi/android/event bugsnag.com]

NEXT STEPS:
  rep android config skip com.netflix.mediaclient <noise-pattern>
  rep android config keep com.netflix.mediaclient <api-pattern>
  rep android list netflix
```

**AI Decision Points:**
- High-frequency domains with generic paths → likely telemetry/CDN
- Endpoints with `*` (auth headers) → valuable API endpoints
- Domains containing: video, cdn, analytics, crash, event → noise candidates

---

### `rep android [package]`

**Purpose:** List captured requests for a package.

**Basic usage:**
```bash
rep android netflix
```

**Output:**
```
Package: com.netflix.mediaclient (14/14 requests)

[h_dc8a3d7dd0a729c5] GET https://android16.push.prod.netflix.com/ws → 101
[h_8de02cd6e2c7e5b6] POST https://android16.prod.ftl.netflix.com/graphql?method=POST&response... → 200
[h_1d39ca354d659f41] POST https://android16.prod.cloud.netflix.com/graphql → 200

Use 'rep body <id>' for full response body
```

**Flags:**

| Flag | Description | Example |
|------|-------------|---------|
| `--api` | Only API-type requests | `rep android netflix --api` |
| `-t, --type` | Filter by type: api, media, telemetry, static | `rep android netflix -t api` |
| `--errors` | Only 4xx/5xx responses | `rep android netflix --errors` |
| `--mutations` | Only POST/PUT/DELETE/PATCH | `rep android netflix --mutations` |
| `--domain` | Filter by domain substring | `rep android netflix --domain graphql` |
| `-m, --method` | Filter by HTTP method | `rep android netflix -m POST` |
| `-n, --limit` | Max requests to show (default 50) | `rep android netflix -n 10` |
| `--detail` | Show headers and body preview | `rep android netflix --detail` |
| `-o json` | JSON output for parsing | `rep android netflix -o json` |

**Type indicators in output:**
- `API` - JSON/XML API calls
- `MED` - Media/streaming
- `TEL` - Telemetry/analytics
- `STT` - Static assets

---

### `rep android config`

**Purpose:** Configure per-app filtering rules. AI agent uses this to eliminate noise.

#### `rep android config show [package]`

Show current configuration:
```bash
rep android config show
# Shows all app configs

rep android config show com.netflix.mediaclient
# Shows config for specific app
```

**Output:**
```json
{
  "com.netflix.mediaclient": {
    "skip_patterns": [
      "nflxvideo.net",
      "/playapi/android/event",
      "bugsnag.com"
    ]
  }
}
```

#### `rep android config skip <package> <pattern>`

Add a skip pattern - requests matching this URL pattern are NOT captured:
```bash
# Skip video streaming domain
rep android config skip com.netflix.mediaclient nflxvideo.net

# Skip telemetry endpoint
rep android config skip com.netflix.mediaclient /playapi/android/event

# Skip crash reporting
rep android config skip com.netflix.mediaclient bugsnag.com

# Skip analytics
rep android config skip com.spotify.music /gabo-receiver
```

#### `rep android config keep <package> <pattern>`

Add a keep pattern - ONLY requests matching these patterns are captured:
```bash
# Only capture GraphQL API
rep android config keep com.netflix.mediaclient /graphql

# Only capture specific API paths
rep android config keep com.example.app /api/v1
rep android config keep com.example.app /api/v2
```

**Note:** If keep patterns are defined, skip patterns are ignored. Request must match at least one keep pattern.

#### `rep android config remove <package> <pattern>`

Remove a pattern:
```bash
rep android config remove com.netflix.mediaclient bugsnag.com
```

#### `rep android config clear <package>`

Clear all config for an app:
```bash
rep android config clear com.netflix.mediaclient
```

---

### `rep android -p` / `rep android --packages`

**Purpose:** Quick overview of all captured packages.

```bash
rep android -p
```

**Output:**
```
Android Traffic: 3 packages

  com.netflix.mediaclient                    38 reqs    4 domains
  com.spotify.music                          71 reqs    9 domains
  com.google.android.gms                      5 reqs    1 domains

Use: rep android <package> to view requests
```

**With domains:**
```bash
rep android -p -d
```

**Output:**
```
Android Traffic: 3 packages

  com.netflix.mediaclient                    38 reqs    4 domains
    - android16.prod.ftl.netflix.com
    - android16.prod.cloud.netflix.com
    - occ-0-1327-1328.1.nflxso.net
  com.spotify.music                          71 reqs    9 domains
    - gue1-spclient.spotify.com
    - graph.facebook.com
```

---

### `rep body <request-id>`

**Purpose:** Get full request/response body for deep analysis.

**Get response body:**
```bash
rep body h_8de02cd6e2c7e5b6
```

**Get request body:**
```bash
rep body h_8de02cd6e2c7e5b6 --request
```

**JSON output:**
```bash
rep body h_8de02cd6e2c7e5b6 -o json
```

**Output:**
```json
{
  "type": "response",
  "id": "h_8de02cd6e2c7e5b6",
  "method": "POST",
  "url": "https://android16.prod.ftl.netflix.com/graphql",
  "status": 200,
  "body": "{\"data\":{\"currentCountry\":{\"code\":\"US\"}}}",
  "headers": {
    "content-type": ["application/json"]
  }
}
```

---

## AI Agent Workflow

### Phase 1: Initial Capture

```bash
# Install script (one-time)
rep android capture install-script

# Start capture (works from any directory)
rep android capture

# OR run in background
rep android capture --bg

# Open target app on device
adb shell am start -n com.target.app/.MainActivity

# Wait for traffic (10-30 seconds of app usage)

# Stop capture
rep android capture stop  # or Ctrl+C if foreground
```

### Phase 2: Review & Classify

```bash
# See what packages were captured
rep android summary

# Deep dive into target app
rep android summary com.target.app
```

**AI should identify:**
1. **Noise domains** - CDN, analytics, crash reporting, ads
2. **API domains** - App's actual backend
3. **High-value endpoints** - Auth, user data, business logic

**Common noise patterns to skip:**
| Pattern | Type | Example Apps |
|---------|------|--------------|
| `video`, `stream`, `cdn` | Media | Netflix, YouTube |
| `analytics`, `metric`, `event` | Telemetry | All apps |
| `bugsnag`, `crashlytics`, `sentry` | Crash reporting | All apps |
| `amplitude`, `mixpanel`, `segment` | Analytics | All apps |
| `appsflyer`, `adjust`, `branch` | Attribution | All apps |
| `doubleclick`, `googlesyndication` | Ads | Free apps |
| `firebase`, `fcm` | Push notifications | All apps |

### Phase 3: Configure Filtering

```bash
# Skip identified noise
rep android config skip com.target.app analytics.example.com
rep android config skip com.target.app /v1/events
rep android config skip com.target.app crashlytics

# OR keep only valuable endpoints
rep android config keep com.target.app /api/
rep android config keep com.target.app /graphql
```

### Phase 4: Clean Recapture

```bash
# Clear old data
rm ~/.local/share/rep-cli/android.json

# Restart mitmproxy (reloads config)
pkill -f mitmdump
mitmdump -p 8080 -s scripts/android/mitm_capture.py

# Use app again - now only valuable traffic is captured
```

### Phase 5: Analyze

```bash
# List filtered requests
rep android com.target.app

# Focus on API calls
rep android com.target.app --api

# Look for errors
rep android com.target.app --errors

# Get specific request details
rep body h_abc123def456 -o json
```

---

## File Locations

| File | Purpose |
|------|---------|
| `~/.local/share/rep-cli/android.json` | Captured traffic data |
| `~/.local/share/rep-cli/android_config.json` | Per-app filtering config |
| `scripts/android/mitm_capture.py` | mitmproxy capture addon |

---

## Request ID Format

Request IDs use FNV-1a hash for stability:
- Format: `h_<16-char-hex>`
- Example: `h_8de02cd6e2c7e5b6`
- Deterministic: same request always gets same ID
- Use with `rep body <id>` to retrieve full content

---

## Tips for AI Agents

1. **Start broad, then filter** - Capture everything first, then configure skip patterns based on what you see.

2. **Use summary first** - `rep android summary <pkg>` gives you the full picture before diving into individual requests.

3. **Auth headers matter** - Endpoints marked with `*` in summary have auth headers - these are usually the valuable API calls.

4. **Config persists** - Once you configure an app, the config is saved. Future captures automatically use it.

5. **Partial matching** - Package names support partial matching: `rep android netflix` matches `com.netflix.mediaclient`.

6. **JSON for parsing** - Use `-o json` when you need to parse output programmatically.

7. **Body truncation** - Captured bodies are truncated to 2KB for efficiency. Use `rep body <id>` for full content.

8. **Status 101** - WebSocket upgrade responses. These are connection establishments, not API calls.

9. **Status 206** - Partial content (streaming). Usually media chunks - good candidate for skip patterns.

10. **Clear between sessions** - `rm ~/.local/share/rep-cli/android.json` to start fresh after configuring filters.
