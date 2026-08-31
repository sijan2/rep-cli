package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/spf13/cobra"
)

// AppConfig holds filtering rules for an app
type AppConfig struct {
	SkipPatterns []string `json:"skip_patterns,omitempty"`
	KeepPatterns []string `json:"keep_patterns,omitempty"`
}

// AndroidConfig holds all app configs
type AndroidConfig map[string]AppConfig

var androidConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Configure Android app filtering (for AI agent)",
	Long: `Manage per-app filtering configuration.

The AI agent uses this to configure which requests to capture/skip per app.
Config is stored in ~/.local/share/rep-cli/android_config.json

Commands:
  rep android config show [package]     Show config for app(s)
  rep android config skip <pkg> <pattern>   Add skip pattern
  rep android config keep <pkg> <pattern>   Add keep pattern  
  rep android config remove <pkg> <pattern> Remove pattern
  rep android config clear <pkg>            Clear all config for app

Examples (AI agent would run these after reviewing traffic):
  rep android config skip com.netflix.mediaclient nflxvideo.net
  rep android config skip com.netflix.mediaclient /playapi/event
  rep android config keep com.netflix.mediaclient /graphql
  rep android config keep com.netflix.mediaclient /api/`,
}

var configShowCmd = &cobra.Command{
	Use:   "show [package]",
	Short: "Show app config",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadAndroidConfig()
		if err != nil {
			return err
		}

		if len(args) > 0 {
			query := strings.ToLower(args[0])
			// Find matching package (partial match)
			for pkg, c := range cfg {
				if strings.Contains(strings.ToLower(pkg), query) {
					out, _ := sonic.MarshalIndent(map[string]AppConfig{pkg: c}, "", "  ")
					fmt.Println(string(out))
					return nil
				}
			}
			fmt.Printf("No config for %s\n", args[0])
			return nil
		}

		if len(cfg) == 0 {
			fmt.Println("No app configs yet. AI agent will configure after reviewing traffic.")
			return nil
		}

		out, _ := sonic.MarshalIndent(cfg, "", "  ")
		fmt.Println(string(out))
		return nil
	},
}

var configSkipCmd = &cobra.Command{
	Use:   "skip <package> <pattern>",
	Short: "Add skip pattern (requests matching this are not captured)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return addPattern(args[0], args[1], "skip")
	},
}

var configKeepCmd = &cobra.Command{
	Use:   "keep <package> <pattern>",
	Short: "Add keep pattern (only requests matching these are captured)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return addPattern(args[0], args[1], "keep")
	},
}

var configRemoveCmd = &cobra.Command{
	Use:   "remove <package> <pattern>",
	Short: "Remove a pattern",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadAndroidConfig()
		if err != nil {
			return err
		}

		pkg, pattern := args[0], args[1]
		if c, ok := cfg[pkg]; ok {
			c.SkipPatterns = removeFromSlice(c.SkipPatterns, pattern)
			c.KeepPatterns = removeFromSlice(c.KeepPatterns, pattern)
			cfg[pkg] = c
			if err := saveAndroidConfig(cfg); err != nil {
				return err
			}
			fmt.Printf("Removed '%s' from %s\n", pattern, pkg)
		}
		return nil
	},
}

var configClearCmd = &cobra.Command{
	Use:   "clear <package>",
	Short: "Clear all config for an app",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadAndroidConfig()
		if err != nil {
			return err
		}

		pkg := args[0]
		delete(cfg, pkg)
		if err := saveAndroidConfig(cfg); err != nil {
			return err
		}
		fmt.Printf("Cleared config for %s\n", pkg)
		return nil
	},
}

// Summary command for AI to review before configuring
var androidSummaryCmd = &cobra.Command{
	Use:   "summary [package]",
	Short: "AI-friendly traffic overview (like web CLI)",
	Long: `Shows traffic summary for AI to review and configure filtering.

AI agent workflow:
1. Run: rep android summary <package>
2. Review domains/endpoints, identify noise
3. Configure: rep android config skip <package> <pattern>
4. Clear and recapture`,
	RunE: func(cmd *cobra.Command, args []string) error {
		data, err := loadAndroidDataRaw()
		if err != nil {
			return err
		}

		if len(data.Packages) == 0 {
			fmt.Println("No Android traffic captured yet.")
			return nil
		}

		if len(args) > 0 {
			return showPackageSummary(data, args[0])
		}

		// Show all packages overview
		fmt.Println()
		fmt.Println("┌─────────────────────────────────────────────────────────────┐")
		fmt.Println("│                    Android Traffic Summary                   │")
		fmt.Println("└─────────────────────────────────────────────────────────────┘")
		fmt.Println()

		var pkgList []string
		for pkg := range data.Packages {
			pkgList = append(pkgList, pkg)
		}
		sort.Slice(pkgList, func(i, j int) bool {
			return len(data.Packages[pkgList[i]].Requests) > len(data.Packages[pkgList[j]].Requests)
		})

		fmt.Println("PACKAGES:")
		for _, pkg := range pkgList {
			p := data.Packages[pkg]
			cfg, _ := loadAndroidConfig()
			status := ""
			if _, ok := cfg[pkg]; ok {
				status = " [CONFIGURED]"
			}
			fmt.Printf("  %-45s %4d reqs  %2d domains%s\n", pkg, len(p.Requests), len(p.Domains), status)
		}
		fmt.Println()
		fmt.Println("Use: rep android summary <package>")
		return nil
	},
}

func showPackageSummary(data *AndroidDataRaw, pkgQuery string) error {
	var pkg *AndroidPackageRaw
	var pkgName string
	for name, p := range data.Packages {
		if strings.Contains(strings.ToLower(name), strings.ToLower(pkgQuery)) {
			pkg = p
			pkgName = name
			break
		}
	}
	if pkg == nil {
		return fmt.Errorf("package not found: %s", pkgQuery)
	}

	// Group by domain and endpoint
	domains := make(map[string]int)
	endpoints := make(map[string]*epInfo)
	methods := make(map[string]int)
	statuses := make(map[int]int)

	for _, r := range pkg.Requests {
		domains[r.Domain]++
		methods[r.Method]++
		statuses[r.Status]++

		key := fmt.Sprintf("%s %s", r.Method, r.Path)
		if e, ok := endpoints[key]; ok {
			e.Count++
			if r.HasAuth {
				e.HasAuth = true
			}
		} else {
			endpoints[key] = &epInfo{
				Method:  r.Method,
				Path:    r.Path,
				Count:   1,
				HasAuth: r.HasAuth,
				AvgSize: r.ResSize,
			}
		}
	}

	// Print summary box
	fmt.Println()
	fmt.Println("┌─────────────────────────────────────────────────────────────┐")
	fmt.Printf("│  %-57s  │\n", pkgName)
	fmt.Println("├─────────────────────────────────────────────────────────────┤")
	fmt.Printf("│  Total Requests: %-5d    Unique Domains: %-3d              │\n", len(pkg.Requests), len(domains))
	fmt.Println("└─────────────────────────────────────────────────────────────┘")
	fmt.Println()

	// Methods
	fmt.Println("METHODS:")
	for m, c := range methods {
		fmt.Printf("  %-8s %d\n", m, c)
	}
	fmt.Println()

	// Status breakdown
	fmt.Println("STATUS:")
	statusRanges := make(map[string]int)
	for s, c := range statuses {
		key := fmt.Sprintf("%dxx", s/100)
		statusRanges[key] += c
	}
	for s, c := range statusRanges {
		fmt.Printf("  %-8s %d\n", s, c)
	}
	fmt.Println()

	// Domains table
	fmt.Println("DOMAINS:")
	fmt.Println("  ┌────────┬──────────────────────────────────────────────────────┐")
	fmt.Println("  │ Reqs   │ Domain                                               │")
	fmt.Println("  ├────────┼──────────────────────────────────────────────────────┤")

	var domainList []string
	for d := range domains {
		domainList = append(domainList, d)
	}
	sort.Slice(domainList, func(i, j int) bool {
		return domains[domainList[i]] > domains[domainList[j]]
	})

	for _, d := range domainList {
		domain := d
		if len(domain) > 52 {
			domain = domain[:49] + "..."
		}
		fmt.Printf("  │ %6d │ %-52s │\n", domains[d], domain)
	}
	fmt.Println("  └────────┴──────────────────────────────────────────────────────┘")
	fmt.Println()

	// Endpoints
	fmt.Println("ENDPOINTS:")
	fmt.Println("  ┌────────┬────────┬─────────────────────────────────────────────┐")
	fmt.Println("  │ Reqs   │ Method │ Path                                        │")
	fmt.Println("  ├────────┼────────┼─────────────────────────────────────────────┤")

	var epList []*epInfo
	for _, e := range endpoints {
		epList = append(epList, e)
	}
	sort.Slice(epList, func(i, j int) bool {
		return epList[i].Count > epList[j].Count
	})

	for i, e := range epList {
		if i >= 15 {
			fmt.Printf("  │        │        │ ... and %d more endpoints                   │\n", len(epList)-15)
			break
		}
		path := e.Path
		if len(path) > 43 {
			path = path[:40] + "..."
		}
		auth := ""
		if e.HasAuth {
			auth = "*"
		}
		fmt.Printf("  │ %6d │ %-6s │ %-43s%s│\n", e.Count, e.Method, path, auth)
	}
	fmt.Println("  └────────┴────────┴─────────────────────────────────────────────┘")
	if hasAuth := countAuthEndpoints(epList); hasAuth > 0 {
		fmt.Printf("  * = has auth headers (%d endpoints)\n", hasAuth)
	}
	fmt.Println()

	// Current config
	cfg, _ := loadAndroidConfig()
	if c, ok := cfg[pkgName]; ok {
		fmt.Println("CURRENT CONFIG:")
		if len(c.SkipPatterns) > 0 {
			fmt.Printf("  skip: %v\n", c.SkipPatterns)
		}
		if len(c.KeepPatterns) > 0 {
			fmt.Printf("  keep: %v\n", c.KeepPatterns)
		}
		fmt.Println()
	}

	// Next steps
	fmt.Println("NEXT STEPS:")
	fmt.Printf("  rep android config skip %s <noise-pattern>\n", pkgName)
	fmt.Printf("  rep android config keep %s <api-pattern>\n", pkgName)
	fmt.Println("  rep android list " + pkgQuery)
	fmt.Println()

	return nil
}

type epInfo struct {
	Method  string
	Path    string
	Count   int
	HasAuth bool
	AvgSize int
}

func countAuthEndpoints(eps []*epInfo) int {
	count := 0
	for _, e := range eps {
		if e.HasAuth {
			count++
		}
	}
	return count
}

// Raw types for loading android.json
type AndroidRequestRaw struct {
	ID      string `json:"id"`
	Method  string `json:"method"`
	URL     string `json:"url"`
	Domain  string `json:"domain"`
	Path    string `json:"path"`
	Status  int    `json:"status"`
	ReqSize int    `json:"req_size"`
	ResSize int    `json:"res_size"`
	HasAuth bool   `json:"has_auth"`
}

type AndroidPackageRaw struct {
	Package   string              `json:"package"`
	Requests  []AndroidRequestRaw `json:"requests"`
	Domains   []string            `json:"domains"`
	Endpoints []string            `json:"endpoints"`
}

type AndroidDataRaw struct {
	Packages map[string]*AndroidPackageRaw `json:"packages"`
}

func loadAndroidDataRaw() (*AndroidDataRaw, error) {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".local/share/rep-cli/android.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var result AndroidDataRaw
	if err := sonic.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func getConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local/share/rep-cli/android_config.json")
}

func loadAndroidConfig() (AndroidConfig, error) {
	data, err := os.ReadFile(getConfigPath())
	if err != nil {
		return make(AndroidConfig), nil
	}
	var cfg AndroidConfig
	if err := sonic.Unmarshal(data, &cfg); err != nil {
		return make(AndroidConfig), nil
	}
	return cfg, nil
}

func saveAndroidConfig(cfg AndroidConfig) error {
	data, err := sonic.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(getConfigPath(), data, 0644)
}

func addPattern(pkg, pattern, patternType string) error {
	cfg, err := loadAndroidConfig()
	if err != nil {
		return err
	}

	c := cfg[pkg]
	if patternType == "skip" {
		if !contains(c.SkipPatterns, pattern) {
			c.SkipPatterns = append(c.SkipPatterns, pattern)
		}
	} else {
		if !contains(c.KeepPatterns, pattern) {
			c.KeepPatterns = append(c.KeepPatterns, pattern)
		}
	}
	cfg[pkg] = c

	if err := saveAndroidConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("Added %s pattern '%s' for %s\n", patternType, pattern, pkg)
	return nil
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func removeFromSlice(slice []string, item string) []string {
	var result []string
	for _, s := range slice {
		if s != item {
			result = append(result, s)
		}
	}
	return result
}

func init() {
	androidCmd.AddCommand(androidConfigCmd)
	androidCmd.AddCommand(androidSummaryCmd)

	androidConfigCmd.AddCommand(configShowCmd)
	androidConfigCmd.AddCommand(configSkipCmd)
	androidConfigCmd.AddCommand(configKeepCmd)
	androidConfigCmd.AddCommand(configRemoveCmd)
	androidConfigCmd.AddCommand(configClearCmd)
}
