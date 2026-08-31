#!/bin/bash
# Android traffic capture with accurate package identification
# Uses /proc/net/tcp UID lookup for 100% accuracy
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HOST_IP=$(ipconfig getifaddr en0 2>/dev/null || ip route get 1 | awk '{print $7}')
PORT=${1:-8080}

echo "=== Android Traffic Capture (UID-based) ==="
echo "Proxy: $HOST_IP:$PORT"
echo "Output: ~/.local/share/rep-cli/android.json"
echo ""
echo "Configure device WiFi proxy to: $HOST_IP:$PORT"
echo "Press Ctrl+C to stop"
echo ""

mitmdump -p "$PORT" --showhost -s "$SCRIPT_DIR/mitm_uid.py"
