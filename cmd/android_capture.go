package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
)

var (
	capturePort int
	captureBg   bool
)

var androidCaptureCmd = &cobra.Command{
	Use:   "capture",
	Short: "Start mitmproxy capture for Android traffic",
	Long: `Starts mitmproxy with the Android capture addon.

This is a convenience wrapper - runs:
  mitmdump -p <port> -s ~/.local/share/rep-cli/scripts/mitm_capture.py

The capture runs in foreground by default. Press Ctrl+C to stop.

Examples:
  rep android capture              Start capture on port 8080
  rep android capture -p 8888      Use different port
  rep android capture --bg         Run in background`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Find the capture script
		home, _ := os.UserHomeDir()
		scriptPath := filepath.Join(home, ".local/share/rep-cli/scripts/mitm_capture.py")

		if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
			return fmt.Errorf("capture script not found at %s\nRun: rep android capture --install", scriptPath)
		}

		// Check if mitmdump is available
		if _, err := exec.LookPath("mitmdump"); err != nil {
			return fmt.Errorf("mitmdump not found. Install mitmproxy first:\n  brew install mitmproxy  # macOS\n  pip install mitmproxy   # pip")
		}

		// Build command
		args = []string{"-p", fmt.Sprintf("%d", capturePort), "--showhost", "-s", scriptPath}

		if captureBg {
			// Background mode
			cmd := exec.Command("mitmdump", args...)
			cmd.Stdout = nil
			cmd.Stderr = nil
			if err := cmd.Start(); err != nil {
				return fmt.Errorf("failed to start mitmdump: %w", err)
			}
			fmt.Printf("Started mitmdump in background (PID %d) on port %d\n", cmd.Process.Pid, capturePort)
			fmt.Println("Stop with: pkill -f mitmdump")
			return nil
		}

		// Foreground mode
		fmt.Printf("Starting Android capture on port %d...\n", capturePort)
		fmt.Println("Press Ctrl+C to stop")
		fmt.Println()

		mitm := exec.Command("mitmdump", args...)
		mitm.Stdout = os.Stdout
		mitm.Stderr = os.Stderr

		// Handle Ctrl+C gracefully
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

		go func() {
			<-sigChan
			fmt.Println("\nStopping capture...")
			mitm.Process.Signal(syscall.SIGTERM)
		}()

		return mitm.Run()
	},
}

var androidCaptureInstallCmd = &cobra.Command{
	Use:   "install-script",
	Short: "Install/update the capture script",
	RunE: func(cmd *cobra.Command, args []string) error {
		home, _ := os.UserHomeDir()
		scriptDir := filepath.Join(home, ".local/share/rep-cli/scripts")
		scriptPath := filepath.Join(scriptDir, "mitm_capture.py")

		if err := os.MkdirAll(scriptDir, 0755); err != nil {
			return err
		}

		// Embedded script content
		script := getCaptureScript()
		if err := os.WriteFile(scriptPath, []byte(script), 0644); err != nil {
			return err
		}

		fmt.Printf("Installed capture script to %s\n", scriptPath)
		return nil
	},
}

var androidCaptureStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop running capture",
	RunE: func(cmd *cobra.Command, args []string) error {
		out, err := exec.Command("pkill", "-f", "mitmdump").CombinedOutput()
		if err != nil {
			if len(out) == 0 {
				fmt.Println("No mitmdump process found")
				return nil
			}
			return err
		}
		fmt.Println("Stopped mitmdump")
		return nil
	},
}

func init() {
	androidCmd.AddCommand(androidCaptureCmd)
	androidCaptureCmd.AddCommand(androidCaptureInstallCmd)
	androidCaptureCmd.AddCommand(androidCaptureStopCmd)

	androidCaptureCmd.Flags().IntVarP(&capturePort, "port", "p", 8080, "Proxy port")
	androidCaptureCmd.Flags().BoolVar(&captureBg, "bg", false, "Run in background")
}

func getCaptureScript() string {
	return `"""
Android traffic capture - Minimal capture for AI classification
"""
import json
import os
import subprocess
import threading
import time
from pathlib import Path
from mitmproxy import http, ctx

DATA_DIR = os.path.expanduser("~/.local/share/rep-cli")
ANDROID_PATH = os.environ.get("REPANDROID_PATH", f"{DATA_DIR}/android.json")
CONFIG_PATH = f"{DATA_DIR}/android_config.json"
Path(DATA_DIR).mkdir(parents=True, exist_ok=True)

uid_to_package = {}
port_to_uid = {}
polling_active = True
app_config = {}

def load_config():
    global app_config
    try:
        with open(CONFIG_PATH) as f:
            app_config = json.load(f)
        ctx.log.info(f"Loaded config for {len(app_config)} apps")
    except:
        app_config = {}

def save_config():
    with open(CONFIG_PATH, 'w') as f:
        json.dump(app_config, f, indent=2)

def load_uid_packages():
    global uid_to_package
    try:
        result = subprocess.run(["adb", "shell", "pm list packages -U"],
                                capture_output=True, text=True, timeout=10)
        for line in result.stdout.strip().split('\n'):
            if 'uid:' in line:
                parts = line.split()
                pkg = parts[0].replace('package:', '')
                uid = int(parts[1].replace('uid:', ''))
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
                capture_output=True, text=True, timeout=2)
            new_mapping = {}
            for line in result.stdout.strip().split('\n'):
                parts = line.split()
                if len(parts) < 8 or parts[0] == 'sl':
                    continue
                try:
                    local_port = int(parts[1].split(':')[1], 16)
                    uid = int(parts[7])
                    if uid >= 10000:
                        new_mapping[local_port] = uid
                except:
                    pass
            port_to_uid = new_mapping
        except:
            pass
        time.sleep(0.05)

def get_package_for_port(port):
    uid = port_to_uid.get(port)
    if uid:
        return uid_to_package.get(uid, f"uid:{uid}")
    return None

def should_capture(package, url, method):
    """Returns (should_capture, is_skipped)"""
    if package not in app_config:
        return True, False
    cfg = app_config[package]
    for pattern in cfg.get('skip_patterns', []):
        if pattern in url:
            return False, True
    keep = cfg.get('keep_patterns', [])
    if keep:
        if any(p in url for p in keep):
            return True, False
        return False, True
    return True, False

def extract_domain(url):
    try:
        from urllib.parse import urlparse
        return urlparse(url).netloc
    except:
        return ""

def extract_path(url):
    try:
        from urllib.parse import urlparse
        return urlparse(url).path
    except:
        return ""

def fnv1a64(s):
    h = 0xcbf29ce484222325
    for c in s:
        h ^= ord(c)
        h = (h * 0x100000001b3) & 0xffffffffffffffff
    return format(h, '016x')

def build_id(flow):
    ts = int(flow.request.timestamp_start * 1000)
    return f"h_{fnv1a64(f'|{ts}|{flow.request.method}|{flow.request.url}')}"

def headers_to_dict(headers):
    result = {}
    for k, v in headers.items(multi=True):
        result.setdefault(k, []).append(v)
    return result

def get_body_preview(content, content_type, max_size=2000):
    if not content:
        return ""
    try:
        text = content.decode('utf-8')
        if len(text) > max_size:
            return text[:max_size] + f"...[truncated, {len(text)} total]"
        return text
    except:
        return f"[binary: {len(content)} bytes, {content_type}]"

def smart_body(content, content_type, is_response=True):
    """
    Smart body handling:
    - Small bodies (<5KB): store as-is
    - Large bodies (>=5KB): gzip compress + base64
    - Binary media: metadata only (images/video/audio)
    """
    import gzip
    import base64
    
    if not content:
        return "", None
    
    ct = (content_type or "").lower()
    size = len(content)
    
    # Explicit binary media - don't store (too large, not useful as text)
    media_types = ['image/', 'video/', 'audio/']
    if any(b in ct for b in media_types):
        return f"[MEDIA: {size} bytes, {ct.split(';')[0]}]", None
    
    # Try decode as text
    try:
        text = content.decode('utf-8')
    except UnicodeDecodeError:
        try:
            text = content.decode('latin-1')
        except:
            return f"[BINARY: {size} bytes]", None
    
    # Small body - store as-is
    if size < 5000:
        return text, None
    
    # Large body - compress with gzip
    try:
        compressed = gzip.compress(content, compresslevel=6)
        encoded = base64.b64encode(compressed).decode('ascii')
        ratio = len(compressed) / size
        # Only use compression if it actually helps (>20% reduction)
        if ratio < 0.8:
            return encoded, f"gzip:{size}"  # encoding field stores original size
        else:
            # Compression didn't help much, truncate instead
            return text[:20000] + f"\n[TRUNCATED: {size} bytes total]", None
    except:
        return text[:20000] + f"\n[TRUNCATED: {size} bytes total]", None

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

def request(flow):
    if flow.client_conn.peername:
        port = flow.client_conn.peername[1]
        pkg = get_package_for_port(port)
        if pkg:
            flow.metadata['package'] = pkg

def response(flow):
    req = flow.request
    res = flow.response
    
    package = flow.metadata.get('package', 'unknown')
    if package == 'unknown' and flow.client_conn.peername:
        port = flow.client_conn.peername[1]
        pkg = get_package_for_port(port)
        if pkg:
            package = pkg
    if package == 'unknown' and 'x-android-package' in req.headers:
        package = req.headers['x-android-package']
    
    url = req.url
    method = req.method
    domain = extract_domain(url)
    path = extract_path(url)
    
    # Check if matches skip pattern (for marking, not filtering)
    _, is_skipped = should_capture(package, url, method)
    
    req_ct = req.headers.get('content-type', '')
    res_ct = res.headers.get('content-type', '') if res else ''
    status = res.status_code if res else 0
    
    # Get raw sizes
    req_content = req.get_content(strict=False) or b''
    res_content = res.get_content(strict=False) if res else b''
    
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
        "has_auth": any(h.lower() in ['authorization', 'x-api-key', 'cookie'] for h in req.headers),
        "skipped": is_skipped,
    }
    
    # Smart body handling - capture everything
    if is_skipped:
        # Skipped: minimal info
        entry["req_body"] = ""
        entry["res_body"] = f"[SKIPPED: {len(res_content)} bytes]"
    elif status == 206:
        # Streaming chunk: metadata only
        entry["req_body"] = ""
        entry["res_body"] = f"[STREAMING: {len(res_content)} bytes]"
    else:
        # Normal: smart compress/store
        req_body, req_enc = smart_body(req_content, req_ct, False)
        res_body, res_enc = smart_body(res_content, res_ct, True)
        entry["req_body"] = req_body
        entry["res_body"] = res_body
        if req_enc:
            entry["req_body_encoding"] = req_enc
        if res_enc:
            entry["res_body_encoding"] = res_enc
    
    data = load_data()
    if package not in data["packages"]:
        data["packages"][package] = {"package": package, "requests": [], "domains": [], "endpoints": []}
    
    pkg_data = data["packages"][package]
    pkg_data["requests"].append(entry)
    
    if domain and domain not in pkg_data["domains"]:
        pkg_data["domains"].append(domain)
    
    endpoint = f"{method} {path}"
    if endpoint not in pkg_data.get("endpoints", []):
        pkg_data.setdefault("endpoints", []).append(endpoint)
    
    save_data(data)
    skip_tag = "[SKIP] " if is_skipped else ""
    ctx.log.info(f"{skip_tag}[{package}] {method} {path[:50]} -> {res.status_code if res else '?'}")

poll_thread = None

def load(loader):
    global poll_thread
    load_config()
    load_uid_packages()
    poll_thread = threading.Thread(target=poll_proc_net_tcp, daemon=True)
    poll_thread.start()

def done():
    global polling_active
    polling_active = False
`
}
