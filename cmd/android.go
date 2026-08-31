package cmd

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var androidCmd = &cobra.Command{
	Use:   "android [package]",
	Short: "View Android traffic by package",
	Long: `View captured Android traffic grouped by app package.

Output modes match 'rep list':
  --line        One-line per request with ID (default, grep-friendly)
  --detail      Multi-line with headers/body preview
  -o json       Raw JSON for piping

Filters:
  --domain      Filter by domain (partial match)
  --method      Filter by HTTP method
  --errors      Only 4xx/5xx responses
  --mutations   Only POST/PUT/DELETE/PATCH
  --since       Only requests since duration (5m, 1h)

Examples:
  rep android -p                           List all packages
  rep android com.spotify.music            Show requests for Spotify
  rep android spotify --domain api         Only requests to *api* domains
  rep android spotify --errors             Only error responses
  rep android spotify --since 5m           Last 5 minutes only
  rep android spotify -o json              JSON output for piping
  rep body <id>                            Get full body for any request ID`,
	RunE: runAndroid,
}

var (
	androidListPackages bool
	androidLimit        int
	androidShowDomains  bool
	androidLine         bool
	androidDetail       bool
	androidErrors       bool
	androidMutations    bool
	androidDomain       string
	androidMethod       string
	androidAll          bool   // Show all including what would be skipped
	androidBudget       string // Token budget mode
	androidSince        string // Time filter
)

// Android primitive subcommands
var androidExtractCmd = &cobra.Command{
	Use:   "extract --pattern <regex> --from <field>",
	Short: "Extract patterns from Android traffic",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAndroidExtract()
	},
}

var androidSearchCmd = &cobra.Command{
	Use:   "search <pattern>",
	Short: "Search Android traffic content",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("search pattern required")
		}
		return runAndroidSearch(args[0])
	},
}

var androidStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show Android traffic statistics",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAndroidStats()
	},
}

var androidGroupCmd = &cobra.Command{
	Use:   "group --by <field>",
	Short: "Group Android requests by field",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAndroidGroup()
	},
}

var (
	androidExtractPattern string
	androidExtractFrom    string
	androidSearchIn       string
	androidGroupBy        string
	androidStatsCountBy   string
)

func init() {
	rootCmd.AddCommand(androidCmd)
	androidCmd.Flags().BoolVarP(&androidListPackages, "packages", "p", false, "List packages only")
	androidCmd.Flags().IntVarP(&androidLimit, "limit", "n", 50, "Max requests to show")
	androidCmd.Flags().BoolVarP(&androidShowDomains, "domains", "d", false, "Show domains per package")
	androidCmd.Flags().BoolVar(&androidLine, "line", true, "One-line output with ID (default)")
	androidCmd.Flags().BoolVar(&androidDetail, "detail", false, "Show request details")
	androidCmd.Flags().BoolVar(&androidErrors, "errors", false, "Only 4xx/5xx responses")
	androidCmd.Flags().BoolVar(&androidMutations, "mutations", false, "Only POST/PUT/DELETE/PATCH")
	androidCmd.Flags().StringVar(&androidDomain, "domain", "", "Filter by domain")
	androidCmd.Flags().StringVarP(&androidMethod, "method", "m", "", "Filter by method")
	androidCmd.Flags().BoolVar(&androidAll, "all", false, "Show all requests (ignore skip/keep config)")
	androidCmd.Flags().StringVar(&androidBudget, "budget", "compact", "Token budget: minimal, compact, standard, full")
	androidCmd.Flags().StringVar(&androidSince, "since", "", "Only requests since duration (e.g., 5m, 1h, 30s)")

	// Add primitive subcommands
	androidCmd.AddCommand(androidExtractCmd)
	androidCmd.AddCommand(androidSearchCmd)
	androidCmd.AddCommand(androidStatsCmd)
	androidCmd.AddCommand(androidGroupCmd)
}

func runAndroid(cmd *cobra.Command, args []string) error {
	data, err := store.LoadAndroidData()
	if err != nil {
		return err
	}

	if len(data.Packages) == 0 {
		fmt.Println("No Android traffic captured yet.")
		fmt.Println("\nTo capture: mitmdump -p 8080 -s scripts/android/mitm_uid.py")
		return nil
	}

	if androidListPackages || len(args) == 0 {
		return listAndroidPackages(data)
	}

	pkg := args[0]
	return showPackageRequests(data, pkg)
}

func listAndroidPackages(data *store.AndroidData) error {
	type pkgInfo struct {
		name    string
		count   int
		domains []string
	}

	var packages []pkgInfo
	for name, p := range data.Packages {
		packages = append(packages, pkgInfo{name: name, count: len(p.Requests), domains: p.Domains})
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].count > packages[j].count })

	fmt.Printf("Android Traffic: %d packages\n\n", len(packages))
	for _, p := range packages {
		fmt.Printf("  %-40s %4d reqs  %3d domains\n", p.name, p.count, len(p.domains))
		if androidShowDomains && len(p.domains) > 0 {
			for _, d := range p.domains {
				fmt.Printf("    - %s\n", d)
			}
		}
	}
	fmt.Println("\nUse: rep android <package> to view requests")
	return nil
}

func showPackageRequests(data *store.AndroidData, pkgQuery string) error {
	// Find package (partial match)
	var pkg *store.AndroidPackage
	var pkgName string
	for name, p := range data.Packages {
		if strings.Contains(strings.ToLower(name), strings.ToLower(pkgQuery)) {
			pkg = p
			pkgName = name
			break
		}
	}
	if pkg == nil {
		fmt.Printf("Package '%s' not found\n", pkgQuery)
		return nil
	}

	// Filter requests
	var filtered []store.AndroidRequest
	for _, r := range pkg.Requests {
		if !matchAndroidFilters(&r) {
			continue
		}
		filtered = append(filtered, r)
	}

	// JSON output
	if getOutputMode() == "json" {
		out, _ := sonic.MarshalIndent(filtered, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	fmt.Printf("Package: %s (%d/%d requests)\n\n", pkgName, len(filtered), len(pkg.Requests))

	// Limit
	if len(filtered) > androidLimit {
		filtered = filtered[len(filtered)-androidLimit:]
	}

	if androidDetail {
		printAndroidDetail(filtered)
	} else {
		printAndroidLine(filtered)
	}

	fmt.Printf("\nUse 'rep body <id>' for full response body\n")
	return nil
}

func matchAndroidFilters(r *store.AndroidRequest) bool {
	// Skip filtered requests unless --all is set
	if r.Skipped && !androidAll {
		return false
	}
	// Time filter
	if androidSince != "" {
		cutoff := parseSinceTime(androidSince)
		if r.Timestamp < cutoff {
			return false
		}
	}
	// Domain filter
	if androidDomain != "" && !strings.Contains(r.Domain, androidDomain) {
		return false
	}
	// Method filter
	if androidMethod != "" && !strings.EqualFold(r.Method, androidMethod) {
		return false
	}
	// Errors preset - check both formats
	if androidErrors {
		status := r.Status
		if status == 0 && r.Response != nil {
			status = r.Response.Status
		}
		if status < 400 {
			return false
		}
	}
	// Mutations preset
	if androidMutations {
		m := strings.ToUpper(r.Method)
		if m != "POST" && m != "PUT" && m != "DELETE" && m != "PATCH" {
			return false
		}
	}
	return true
}

// parseSinceTime parses duration string (5m, 1h, 30s) and returns cutoff timestamp in ms
func parseSinceTime(since string) int64 {
	d, err := time.ParseDuration(since)
	if err != nil {
		return 0
	}
	return time.Now().Add(-d).UnixMilli()
}

func printAndroidLine(reqs []store.AndroidRequest) {
	for _, r := range reqs {
		// Get status from either format
		status := r.Status
		if status == 0 && r.Response != nil {
			status = r.Response.Status
		}
		url := r.URL
		if len(url) > 70 {
			url = url[:67] + "..."
		}
		// Show skip indicator
		skipTag := ""
		if r.Skipped {
			skipTag = "~ "
		}
		// Show type indicator
		typeTag := ""
		switch r.Type {
		case "api":
			typeTag = "API"
		case "media":
			typeTag = "MED"
		case "telemetry":
			typeTag = "TEL"
		case "static":
			typeTag = "STT"
		}
		if typeTag != "" {
			fmt.Printf("%s[%s] %-3s %s %s → %d\n", skipTag, r.ID, typeTag, r.Method, url, status)
		} else {
			fmt.Printf("%s[%s] %s %s → %d\n", skipTag, r.ID, r.Method, url, status)
		}
	}
}

func printAndroidDetail(reqs []store.AndroidRequest) {
	for _, r := range reqs {
		status := 0
		if r.Response != nil {
			status = r.Response.Status
		}
		ts := time.UnixMilli(r.Timestamp).Format("15:04:05")

		fmt.Printf("─── %s [%s] ───\n", r.ID, ts)
		fmt.Printf("%s %s → %d\n", r.Method, r.URL, status)

		// Key request headers
		for _, h := range []string{"authorization", "content-type", "x-api-key"} {
			if vals := r.Headers[h]; len(vals) > 0 {
				v := vals[0]
				if (h == "authorization" || h == "x-api-key") && len(v) > 20 {
					v = v[:10] + "..." + v[len(v)-5:]
				}
				fmt.Printf("  %s: %s\n", h, v)
			}
		}

		// Request body preview
		if r.Body != "" {
			body := r.Body
			if len(body) > 200 {
				body = body[:200] + fmt.Sprintf("... [%s]", output.FormatBodySize(len(r.Body)))
			}
			fmt.Printf("  Body: %s\n", strings.ReplaceAll(body, "\n", " "))
		}

		// Response body preview
		if r.Response != nil && r.Response.Body != "" {
			body := r.Response.Body
			if len(body) > 300 {
				body = body[:300] + fmt.Sprintf("... [%s]", output.FormatBodySize(len(r.Response.Body)))
			}
			fmt.Printf("  Response: %s\n", strings.ReplaceAll(body, "\n", " ")[:min(200, len(body))])
		}
		fmt.Println()
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Android primitive implementations
func runAndroidExtract() error {
	data, err := store.LoadAndroidData()
	if err != nil {
		return err
	}

	re, err := regexp.Compile(androidExtractPattern)
	if err != nil {
		return fmt.Errorf("invalid pattern: %w", err)
	}

	var results []map[string]interface{}
	for _, pkg := range data.Packages {
		for _, r := range pkg.Requests {
			var content string
			switch androidExtractFrom {
			case "path", "url":
				content = r.URL
			case "body":
				content = r.Body
			case "response":
				if r.Response != nil {
					content = r.Response.Body
				}
			default:
				content = r.URL
			}
			matches := re.FindAllString(content, -1)
			if len(matches) > 0 {
				results = append(results, map[string]interface{}{
					"id":      r.ID,
					"matches": matches,
				})
			}
		}
	}

	out, _ := sonic.MarshalIndent(results, "", "  ")
	fmt.Println(string(out))
	return nil
}

func runAndroidSearch(pattern string) error {
	data, err := store.LoadAndroidData()
	if err != nil {
		return err
	}

	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return fmt.Errorf("invalid pattern: %w", err)
	}

	var results []map[string]interface{}
	for _, pkg := range data.Packages {
		for _, r := range pkg.Requests {
			var found bool
			switch androidSearchIn {
			case "body":
				found = re.MatchString(r.Body)
			case "response":
				found = r.Response != nil && re.MatchString(r.Response.Body)
			default:
				found = re.MatchString(r.URL) || re.MatchString(r.Body) ||
					(r.Response != nil && re.MatchString(r.Response.Body))
			}
			if found {
				results = append(results, map[string]interface{}{
					"id":     r.ID,
					"method": r.Method,
					"url":    r.URL,
				})
			}
		}
	}

	out, _ := sonic.MarshalIndent(results, "", "  ")
	fmt.Println(string(out))
	return nil
}

func runAndroidStats() error {
	data, err := store.LoadAndroidData()
	if err != nil {
		return err
	}

	stats := map[string]interface{}{
		"packages": len(data.Packages),
	}

	var totalReqs int
	methodCounts := make(map[string]int)
	statusCounts := make(map[string]int)

	for _, pkg := range data.Packages {
		totalReqs += len(pkg.Requests)
		for _, r := range pkg.Requests {
			methodCounts[r.Method]++
			status := r.Status
			if status == 0 && r.Response != nil {
				status = r.Response.Status
			}
			bucket := fmt.Sprintf("%dxx", status/100)
			statusCounts[bucket]++
		}
	}

	stats["total_requests"] = totalReqs
	stats["methods"] = methodCounts
	stats["status_buckets"] = statusCounts

	out, _ := sonic.MarshalIndent(stats, "", "  ")
	fmt.Println(string(out))
	return nil
}

func runAndroidGroup() error {
	data, err := store.LoadAndroidData()
	if err != nil {
		return err
	}

	groups := make(map[string]int)
	for _, pkg := range data.Packages {
		for _, r := range pkg.Requests {
			var key string
			switch androidGroupBy {
			case "method":
				key = r.Method
			case "domain":
				key = r.Domain
			case "package":
				key = r.Package
			case "status":
				status := r.Status
				if status == 0 && r.Response != nil {
					status = r.Response.Status
				}
				key = fmt.Sprintf("%d", status)
			default:
				key = r.Domain
			}
			groups[key]++
		}
	}

	out, _ := sonic.MarshalIndent(groups, "", "  ")
	fmt.Println(string(out))
	return nil
}

func init() {
	// Android extract flags
	androidExtractCmd.Flags().StringVar(&androidExtractPattern, "pattern", "", "Regex pattern to extract")
	androidExtractCmd.Flags().StringVar(&androidExtractFrom, "from", "url", "Field to extract from: url, body, response")
	androidExtractCmd.MarkFlagRequired("pattern")

	// Android search flags
	androidSearchCmd.Flags().StringVar(&androidSearchIn, "in", "", "Search in: body, response, or all (default)")

	// Android group flags
	androidGroupCmd.Flags().StringVar(&androidGroupBy, "by", "domain", "Group by: method, domain, package, status")

	// Android stats flags
	androidStatsCmd.Flags().StringVar(&androidStatsCountBy, "count-by", "", "Count by field")
}
