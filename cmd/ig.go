package cmd

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

//go:embed assets/ig_hook.js
var igHookScript string

const (
	igPackage    = "com.instagram.android"
	igBinaryName = "Instagram"
)

var (
	igProxyPort     int
	igFridaBin      string
	igHostIP        string
	igSpawn         bool
	igSkipFrida     bool
	igExternalProxy string // "host:port" — skip mitmdump, route phone to this proxy (e.g. Burp)
	igNoSELinux     bool   // skip `setenforce 0` preflight (API 36 requires it)
	igFridaHost     string // e.g. "127.0.0.1:27043" — frida-server address via adb forward
	igListSurface   string
	igListDomain    string
	igListLimit     int
)

var igCmd = &cobra.Command{
	Use:   "ig",
	Short: "Instagram one-shot capture: mitmdump + adb proxy + Frida pinning bypass + spawn",
	Long: `Full Instagram capture pipeline in one command.

rep ig                            Start mitmdump, set phone proxy, attach Frida
                                  SSL pinning bypass to a running IG. Ctrl+C tears down.

rep ig --proxy 10.0.0.4:8080      Use Burp/Charles/etc. already listening there;
                                  don't start mitmdump. Phone is pointed at that proxy.

rep ig --frida-host 127.0.0.1:27043
                                  Connect to a port-forwarded frida-server (for
                                  renamed/stealth setups). Requires 'adb forward'
                                  already in place.

rep ig --spawn                    Spawn IG under Frida instead of attaching.
                                  On API 36 this often trips anti-debug — default is attach.

rep ig stop                       Teardown: remove phone proxy, kill mitmdump + frida.

rep ig list [--surface <name>] [--domain <host>] [-n <limit>]
                                  List IG requests with IG-aware filters.
                                  --surface matches X-Fb-Friendly-Name (e.g. clips_profile).

rep ig surface                    Count requests grouped by X-Fb-Friendly-Name.
rep ig body <id>                  Full request + response (gzip-decoded).

Prerequisites:
  - frida-server patched for API 36 running on device
    (https://github.com/sijan2/frida — v17.5.2-android16 release)
  - adb connected, device rooted (Magisk/su — needed for setenforce 0)
  - Burp/mitmproxy CA installed as a system CA
  - mitmdump on PATH (brew install mitmproxy) — only if not using --proxy
  - frida on PATH or $FRIDA_BIN (must be the API-36-patched version)

API 36 notes:
  - SELinux is auto-set permissive (blocks /memfd:frida-agent-64.so otherwise).
    Use --no-selinux to skip. Reverts on reboot.
  - Default mode is attach, not spawn. IG is launched via 'monkey' if not running.
`,
	RunE: runIGStart,
}

var igStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Teardown: unset proxy, kill mitmdump + frida",
	RunE:  runIGStop,
}

var igListCmd = &cobra.Command{
	Use:   "list",
	Short: "List IG requests (filtered to com.instagram.android)",
	RunE:  runIGList,
}

var igSurfaceCmd = &cobra.Command{
	Use:   "surface",
	Short: "Count IG requests grouped by X-Fb-Friendly-Name surface",
	RunE:  runIGSurface,
}

var igBodyCmd = &cobra.Command{
	Use:   "body <id-or-prefix>",
	Short: "Print full request+response for an IG request (gzip-decoded)",
	Args:  cobra.ExactArgs(1),
	RunE:  runIGBody,
}

func init() {
	rootCmd.AddCommand(igCmd)
	igCmd.AddCommand(igStopCmd)
	igCmd.AddCommand(igListCmd)
	igCmd.AddCommand(igSurfaceCmd)
	igCmd.AddCommand(igBodyCmd)

	igCmd.Flags().IntVarP(&igProxyPort, "port", "p", 8080, "Proxy port (mitmdump listens here, or the port your external proxy is on)")
	igCmd.Flags().StringVar(&igFridaBin, "frida", "", "Path to patched frida binary (default: $FRIDA_BIN or `which frida`)")
	igCmd.Flags().StringVar(&igHostIP, "host", "", "Mac LAN IP the phone will proxy through (default: auto-detect)")
	igCmd.Flags().BoolVar(&igSpawn, "spawn", false, "Spawn IG under Frida instead of attaching (spawn often trips anti-debug on API 36)")
	igCmd.Flags().BoolVar(&igSkipFrida, "no-frida", false, "Skip Frida entirely — only start mitmdump + proxy")
	igCmd.Flags().StringVar(&igExternalProxy, "proxy", "", "Use an external proxy (host:port, e.g. Burp at 10.0.0.4:8080) instead of starting mitmdump")
	igCmd.Flags().BoolVar(&igNoSELinux, "no-selinux", false, "Skip `setenforce 0` on device (API 36 SELinux blocks frida-agent memfd — only skip if handled another way)")
	igCmd.Flags().StringVar(&igFridaHost, "frida-host", "", "frida-server address via adb forward (e.g. 127.0.0.1:27043). Omit to use USB transport.")

	igListCmd.Flags().StringVar(&igListSurface, "surface", "", "Filter by X-Fb-Friendly-Name (partial match, e.g. clips_profile)")
	igListCmd.Flags().StringVar(&igListDomain, "domain", "", "Filter by domain (partial match, e.g. graph.instagram.com)")
	igListCmd.Flags().IntVarP(&igListLimit, "limit", "n", 50, "Max requests to show")
}

// ---- START -------------------------------------------------------

func runIGStart(cmd *cobra.Command, args []string) error {
	if err := igPreflight(); err != nil {
		return err
	}

	// SELinux: on Android 16 (API 36), untrusted_app cannot read/write
	// /memfd:frida-agent-64.so, so the agent fails to load. `setenforce 0`
	// reverts on reboot. Skip with --no-selinux if you handle it elsewhere.
	if !igNoSELinux {
		if err := setSELinuxPermissive(); err != nil {
			fmt.Printf("[rep ig] warning: could not set SELinux permissive (%v). If frida attach fails, run: adb shell \"su -c 'setenforce 0'\"\n", err)
		} else {
			fmt.Println("[rep ig] SELinux set to permissive (reverts on reboot)")
		}
	}

	// Resolve proxy address — either external (user's Burp/Charles) or spin mitmdump.
	var proxyAddr string
	var mitm *exec.Cmd
	if igExternalProxy != "" {
		proxyAddr = igExternalProxy
		fmt.Printf("[rep ig] using external proxy: %s (mitmdump not started)\n", proxyAddr)
	} else {
		hostIP := igHostIP
		if hostIP == "" {
			ip, err := detectLANIP()
			if err != nil {
				return fmt.Errorf("could not auto-detect LAN IP: %w (pass --host or --proxy)", err)
			}
			hostIP = ip
		}
		if err := ensureMitmScript(); err != nil {
			return err
		}
		scriptPath := mitmScriptPath()
		fmt.Printf("[rep ig] host=%s  port=%d\n", hostIP, igProxyPort)
		fmt.Printf("[rep ig] starting mitmdump with %s\n", scriptPath)

		mitm = exec.Command("mitmdump", "-p", fmt.Sprintf("%d", igProxyPort), "--showhost",
			"--set", "termlog_verbosity=warn", "--set", "flow_detail=0",
			"-s", scriptPath)
		mitm.Stdout = os.Stdout
		mitm.Stderr = os.Stderr
		if err := mitm.Start(); err != nil {
			return fmt.Errorf("mitmdump: %w", err)
		}
		time.Sleep(1200 * time.Millisecond) // let it bind
		proxyAddr = fmt.Sprintf("%s:%d", hostIP, igProxyPort)
	}

	if err := setAndroidProxy(proxyAddr); err != nil {
		if mitm != nil {
			_ = mitm.Process.Signal(syscall.SIGTERM)
		}
		return fmt.Errorf("set proxy: %w", err)
	}
	fmt.Printf("[rep ig] phone proxy -> %s\n", proxyAddr)

	// Ensure IG is in the right state before frida connects.
	if !igSkipFrida {
		if igSpawn {
			_ = exec.Command("adb", "shell", "am", "force-stop", igPackage).Run()
		} else {
			// Attach mode: launch IG if not running so there's something to attach to.
			if pidof(igPackage) == "" {
				fmt.Println("[rep ig] IG not running — launching via monkey")
				_ = exec.Command("adb", "shell", "monkey", "-p", igPackage,
					"-c", "android.intent.category.LAUNCHER", "1").Run()
				time.Sleep(3500 * time.Millisecond)
			}
		}
	}

	// Write embedded Frida script to disk (a new one each run; do NOT overwrite user edits)
	hookPath := filepath.Join(os.TempDir(), fmt.Sprintf("rep-ig-hook-%d.js", os.Getpid()))
	if err := os.WriteFile(hookPath, []byte(igHookScript), 0600); err != nil {
		return err
	}
	defer os.Remove(hookPath)

	// Graceful teardown on Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	var frida *exec.Cmd
	if !igSkipFrida {
		fridaBin, err := resolveFrida()
		if err != nil {
			_ = unsetAndroidProxy()
			if mitm != nil {
				_ = mitm.Process.Signal(syscall.SIGTERM)
			}
			return err
		}

		// Transport: -H host:port (via adb forward) or -U (USB).
		var fridaArgs []string
		if igFridaHost != "" {
			fridaArgs = append(fridaArgs, "-H", igFridaHost)
		} else {
			fridaArgs = append(fridaArgs, "-U")
		}
		fridaArgs = append(fridaArgs, "-l", hookPath)
		if igSpawn {
			fridaArgs = append(fridaArgs, "-f", igPackage)
		} else {
			// Use package name for -n: display-name lookup ("Instagram") works over
			// USB transport but not over -H (remote server has no display-name map).
			fridaArgs = append(fridaArgs, "-n", igPackage)
		}
		fmt.Printf("[rep ig] %s %s\n", fridaBin, strings.Join(fridaArgs, " "))
		if igSpawn {
			fmt.Println("[rep ig] IG spawning — use the app now. Ctrl+C to stop.")
		} else {
			fmt.Println("[rep ig] attached to running IG — use the app now. Ctrl+C to stop.")
		}
		fmt.Println()

		frida = exec.Command(fridaBin, fridaArgs...)
		frida.Stdin = os.Stdin
		frida.Stdout = os.Stdout
		frida.Stderr = os.Stderr
		if err := frida.Start(); err != nil {
			_ = unsetAndroidProxy()
			if mitm != nil {
				_ = mitm.Process.Signal(syscall.SIGTERM)
			}
			return fmt.Errorf("frida: %w", err)
		}
	} else {
		fmt.Println("[rep ig] --no-frida: open IG manually. Ctrl+C to stop.")
	}

	// Wait for signal or frida exit
	done := make(chan error, 1)
	if frida != nil {
		go func() { done <- frida.Wait() }()
	}
	select {
	case <-sigChan:
		fmt.Println("\n[rep ig] tearing down…")
	case err := <-done:
		fmt.Printf("\n[rep ig] frida exited: %v\n", err)
	}

	if frida != nil && frida.Process != nil {
		_ = frida.Process.Signal(syscall.SIGTERM)
		_ = frida.Wait()
	}
	_ = unsetAndroidProxy()
	if mitm != nil && mitm.Process != nil {
		_ = mitm.Process.Signal(syscall.SIGTERM)
		_ = mitm.Wait()
	}
	fmt.Println("[rep ig] done.")
	return nil
}

// ---- STOP --------------------------------------------------------

func runIGStop(cmd *cobra.Command, args []string) error {
	var errs []string
	if err := unsetAndroidProxy(); err != nil {
		errs = append(errs, "unset proxy: "+err.Error())
	} else {
		fmt.Println("[rep ig] phone proxy unset")
	}
	if out, err := exec.Command("pkill", "-f", "mitmdump").CombinedOutput(); err == nil {
		fmt.Println("[rep ig] killed mitmdump")
	} else if len(out) > 0 {
		errs = append(errs, "pkill mitmdump: "+string(out))
	}
	if out, err := exec.Command("pkill", "-f", "frida.*com.instagram.android").CombinedOutput(); err == nil {
		fmt.Println("[rep ig] killed frida IG session")
	} else if len(out) > 0 && strings.TrimSpace(string(out)) != "" {
		errs = append(errs, "pkill frida: "+string(out))
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// ---- LIST --------------------------------------------------------

func runIGList(cmd *cobra.Command, args []string) error {
	data, err := store.LoadAndroidData()
	if err != nil {
		return err
	}
	pkg := data.Packages[igPackage]
	if pkg == nil || len(pkg.Requests) == 0 {
		fmt.Printf("No Instagram traffic captured yet. Run `rep ig` first.\n")
		return nil
	}
	reqs := pkg.Requests

	// Apply filters
	var filtered []store.AndroidRequest
	surf := strings.ToLower(igListSurface)
	dom := strings.ToLower(igListDomain)
	for _, r := range reqs {
		if dom != "" && !strings.Contains(strings.ToLower(r.Domain), dom) {
			continue
		}
		if surf != "" {
			sv := strings.ToLower(igGetSurface(r))
			if !strings.Contains(sv, surf) {
				continue
			}
		}
		filtered = append(filtered, r)
	}

	// Newest first
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Timestamp > filtered[j].Timestamp
	})
	if len(filtered) > igListLimit {
		filtered = filtered[:igListLimit]
	}

	fmt.Printf("IG traffic: %d total  showing %d  (filters: surface=%q domain=%q)\n\n",
		len(reqs), len(filtered), igListSurface, igListDomain)
	for _, r := range filtered {
		surface := igGetSurface(r)
		tag := igClassify(r)
		ts := time.UnixMilli(r.Timestamp).Format("15:04:05")
		line := fmt.Sprintf("%s  %-3s  %-4d  %-14s  %-32s  %s",
			ts, r.Method, r.Status, truncate(tag, 14), truncate(surface, 32), r.Path)
		if r.HasAuth {
			line += " [auth]"
		}
		fmt.Printf("%s  %s\n", r.ID, line)
	}
	fmt.Println("\nUse: rep ig body <id>   (prefix OK — e.g. first 8-12 chars)")
	return nil
}

// ---- BODY --------------------------------------------------------

func runIGBody(cmd *cobra.Command, args []string) error {
	prefix := args[0]
	data, err := store.LoadAndroidData()
	if err != nil {
		return err
	}
	pkg := data.Packages[igPackage]
	if pkg == nil {
		return fmt.Errorf("no IG traffic captured yet")
	}
	var match *store.AndroidRequest
	count := 0
	for i := range pkg.Requests {
		if strings.HasPrefix(pkg.Requests[i].ID, prefix) {
			match = &pkg.Requests[i]
			count++
		}
	}
	if match == nil {
		return fmt.Errorf("no request with id prefix %q (try `rep ig list`)", prefix)
	}
	if count > 1 {
		return fmt.Errorf("prefix %q matched %d requests — supply more characters", prefix, count)
	}
	return printIGRequest(match)
}

func printIGRequest(r *store.AndroidRequest) error {
	fmt.Println("================================================================")
	fmt.Printf("ID       : %s\n", r.ID)
	fmt.Printf("Time     : %s\n", time.UnixMilli(r.Timestamp).Format(time.RFC3339))
	fmt.Printf("Method   : %s\n", r.Method)
	fmt.Printf("URL      : %s\n", r.URL)
	fmt.Printf("Status   : %d\n", r.Status)
	fmt.Printf("Surface  : %s\n", igGetSurface(*r))
	fmt.Printf("Class    : %s\n", igClassify(*r))
	fmt.Printf("Has auth : %v\n", r.HasAuth)
	fmt.Printf("Req size : %d\n", len(r.ReqBody))
	fmt.Printf("Res size : %d\n", len(r.ResBody))
	fmt.Println()
	fmt.Println("--- Request headers ---")
	printHeadersSorted(r.ReqHeaders)
	fmt.Println()
	fmt.Println("--- Request body ---")
	fmt.Println(safeTrim(r.GetReqBody(), 8000))
	fmt.Println()
	fmt.Println("--- Response headers ---")
	printHeadersSorted(r.ResHeaders)
	fmt.Println()
	fmt.Println("--- Response body ---")
	fmt.Println(safeTrim(r.GetResBody(), 16000))
	return nil
}

func printHeadersSorted(h map[string][]string) {
	if len(h) == 0 {
		fmt.Println("  (none)")
		return
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range h[k] {
			fmt.Printf("  %s: %s\n", k, v)
		}
	}
}

func safeTrim(s string, max int) string {
	if s == "" {
		return "(empty)"
	}
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n...[truncated %d bytes]", len(s)-max)
}

// ---- SURFACE -----------------------------------------------------

func runIGSurface(cmd *cobra.Command, args []string) error {
	data, err := store.LoadAndroidData()
	if err != nil {
		return err
	}
	pkg := data.Packages[igPackage]
	if pkg == nil {
		fmt.Println("No Instagram traffic yet.")
		return nil
	}

	type bucket struct {
		surface string
		count   int
		methods map[string]int
		domains map[string]int
	}
	m := map[string]*bucket{}
	total := 0
	for _, r := range pkg.Requests {
		s := igGetSurface(r)
		if s == "" {
			s = "(none)"
		}
		b := m[s]
		if b == nil {
			b = &bucket{surface: s, methods: map[string]int{}, domains: map[string]int{}}
			m[s] = b
		}
		b.count++
		b.methods[r.Method]++
		b.domains[r.Domain]++
		total++
	}
	var sorted []*bucket
	for _, b := range m {
		sorted = append(sorted, b)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })

	fmt.Printf("IG surfaces (X-Fb-Friendly-Name)  —  %d total requests\n\n", total)
	for _, b := range sorted {
		var methodParts []string
		for m, c := range b.methods {
			methodParts = append(methodParts, fmt.Sprintf("%s:%d", m, c))
		}
		sort.Strings(methodParts)
		topDom := ""
		topCount := 0
		for d, c := range b.domains {
			if c > topCount {
				topDom = d
				topCount = c
			}
		}
		fmt.Printf("  %5d  %-44s  %-30s  %s\n", b.count, truncate(b.surface, 44),
			strings.Join(methodParts, " "), truncate(topDom, 40))
	}
	fmt.Println("\nUse: rep ig list --surface <name>   to drill in")
	return nil
}

// ---- HELPERS -----------------------------------------------------

func igPreflight() error {
	if _, err := exec.LookPath("mitmdump"); err != nil {
		return fmt.Errorf("mitmdump not found. Install: brew install mitmproxy")
	}
	if _, err := exec.LookPath("adb"); err != nil {
		return fmt.Errorf("adb not found")
	}
	// device connected?
	out, err := exec.Command("adb", "get-state").Output()
	if err != nil || !strings.Contains(string(out), "device") {
		return fmt.Errorf("no adb device attached (adb get-state = %q)", strings.TrimSpace(string(out)))
	}
	return nil
}

func detectLANIP() (string, error) {
	// Prefer a non-loopback, non-link-local IPv4 on a typical LAN interface.
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil {
				continue
			}
			return ip4.String(), nil
		}
	}
	return "", fmt.Errorf("no non-loopback IPv4 found")
}

func resolveFrida() (string, error) {
	if igFridaBin != "" {
		return igFridaBin, nil
	}
	if env := os.Getenv("FRIDA_BIN"); env != "" {
		return env, nil
	}
	if p, err := exec.LookPath("frida"); err == nil {
		return p, nil
	}
	// Heuristic fallback
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, "code/learn/python-frida/.venv/bin/frida"),
		filepath.Join(home, ".local/share/rep-cli/venv/bin/frida"),
	} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("frida not on PATH. Set --frida or $FRIDA_BIN")
}

func setAndroidProxy(proxy string) error {
	return exec.Command("adb", "shell", "settings", "put", "global", "http_proxy", proxy).Run()
}

// setSELinuxPermissive requires root (Magisk/su). Safe to call unconditionally:
// if already permissive, the command is a no-op.
func setSELinuxPermissive() error {
	return exec.Command("adb", "shell", "su", "-c", "setenforce 0").Run()
}

func pidof(pkg string) string {
	out, err := exec.Command("adb", "shell", "pidof", pkg).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func unsetAndroidProxy() error {
	// Use :0 which Android treats as "no proxy" on most devices; fallback to delete.
	if err := exec.Command("adb", "shell", "settings", "put", "global", "http_proxy", ":0").Run(); err == nil {
		return nil
	}
	return exec.Command("adb", "shell", "settings", "delete", "global", "http_proxy").Run()
}

func mitmScriptPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local/share/rep-cli/scripts/mitm_capture.py")
}

func ensureMitmScript() error {
	p := mitmScriptPath()
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(getCaptureScript()), 0644)
}

// IG surface = X-Fb-Friendly-Name header (one of the most reliable fingerprints
// for which IG screen/action triggered the request). Falls back to analytics tag.
func igGetSurface(r store.AndroidRequest) string {
	h := headerFirst(r.ReqHeaders, "X-Fb-Friendly-Name")
	if h != "" {
		return h
	}
	// Fallback: parse X-Fb-Request-Analytics-Tags JSON for network_tags.surface
	tags := headerFirst(r.ReqHeaders, "X-Fb-Request-Analytics-Tags")
	if tags != "" {
		var parsed struct {
			NT struct {
				Surface string `json:"surface"`
			} `json:"network_tags"`
		}
		if err := json.Unmarshal([]byte(tags), &parsed); err == nil && parsed.NT.Surface != "" {
			return parsed.NT.Surface
		}
	}
	return ""
}

// igClassify: short tag for what kind of IG request this is.
func igClassify(r store.AndroidRequest) string {
	d := strings.ToLower(r.Domain)
	p := strings.ToLower(r.Path)
	ct := strings.ToLower(headerFirst(r.ResHeaders, "Content-Type"))
	switch {
	case strings.Contains(d, "graph.instagram.com") && strings.Contains(p, "pigeon_nest"):
		return "pigeon"
	case strings.Contains(d, "graph.instagram.com") || strings.Contains(d, "graph.facebook.com"):
		return "graph"
	case strings.Contains(d, "i.instagram.com") && strings.Contains(p, "graphql"):
		return "graphql"
	case strings.Contains(d, "i.instagram.com") && strings.Contains(p, "xdt_api"):
		return "xdt_api"
	case strings.Contains(d, "i.instagram.com"):
		return "api"
	case strings.Contains(d, "cdninstagram.com") && (strings.Contains(ct, "video") || strings.HasSuffix(p, ".mp4")):
		return "cdn-video"
	case strings.Contains(d, "cdninstagram.com") && (strings.Contains(ct, "image") || strings.HasSuffix(p, ".jpg") || strings.HasSuffix(p, ".webp") || strings.HasSuffix(p, ".heic")):
		return "cdn-image"
	case strings.Contains(d, "cdninstagram.com") && strings.HasSuffix(p, ".srt"):
		return "cdn-srt"
	case strings.Contains(d, "cdninstagram.com"):
		return "cdn-other"
	case strings.Contains(d, "edge-mqtt.facebook.com"):
		return "mqtt"
	case strings.Contains(d, "fbcdn.net"):
		return "fbcdn"
	}
	return ""
}

func headerFirst(hs map[string][]string, name string) string {
	if hs == nil {
		return ""
	}
	// Case-insensitive match.
	low := strings.ToLower(name)
	for k, vs := range hs {
		if strings.ToLower(k) == low && len(vs) > 0 {
			return vs[0]
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
