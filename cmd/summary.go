package cmd

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/noise"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	summarySaved string
)

var summaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "AI-friendly traffic overview for first-pass analysis",
	Long: `Generate a compact summary of captured traffic.
Designed for AI agents to quickly understand the traffic landscape.

Default: Shows summary from LIVE session (real-time).
Use --saved to view summary from archived sessions.

Shows:
  - Total requests and unique domains
  - Domain breakdown with request counts
  - Method distribution
  - Suggested domains to ignore (analytics, CDN, tracking)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var tempStore *store.Store
		var persistentStore *store.Store

		// Load persistent store for ignore/primary lists
		var err error
		persistentStore, err = store.Get()
		if err != nil {
			return fmt.Errorf("failed to load store: %w", err)
		}

		if summarySaved != "" {
			// Load from saved session
			var session *store.Session
			if summarySaved == "latest" || summarySaved == "last" {
				session = persistentStore.GetLatestSession()
			} else {
				session = persistentStore.GetSession(summarySaved)
			}

			if session == nil {
				pterm.Warning.Printf("Session not found: %s\n", summarySaved)
				pterm.Info.Println("Use 'rep sessions' to list available sessions")
				return nil
			}

			tempStore = store.NewTempStore(session.Requests)
		} else {
			// Default: Load from live.json
			livePath, err := store.GetLiveFilePath()
			if err != nil {
				return fmt.Errorf("failed to get live path: %w", err)
			}
			export, err := loadLiveExport(livePath)
			if err != nil {
				pterm.Warning.Printf("Could not read live.json: %v\n", err)
				pterm.Info.Println("Enable auto-export in rep+ extension first")
				return nil
			}
			if len(export.Requests) == 0 {
				pterm.Info.Println("No requests captured yet (live session empty)")
				return nil
			}

			tempStore = store.NewTempStore(export.Requests)
		}

		// Apply ignore/primary lists
		tempStore.PrimaryDomains = persistentStore.PrimaryDomains
		tempStore.IgnoredDomains = persistentStore.IgnoredDomains

		domains := tempStore.GetDomains()

		// Build summary data
		summary := buildSummary(tempStore, domains, persistentStore)

		if getOutputMode() == "json" {
			out, _ := sonic.MarshalIndent(summary, "", "  ")
			fmt.Println(string(out))
		} else {
			printSummary(summary, domains, tempStore)
		}

		return nil
	},
}

type Summary struct {
	TotalRequests   int             `json:"total_requests"`
	UniqueDomains   int             `json:"unique_domains"`
	IgnoredDomains  int             `json:"ignored_domains"`
	PrimaryDomains  []string        `json:"primary_domains"`
	MethodBreakdown map[string]int  `json:"method_breakdown"`
	StatusBreakdown map[string]int  `json:"status_breakdown"`
	PageBreakdown   []PageSummary   `json:"page_breakdown"`
	TopDomains      []DomainSummary `json:"top_domains"`
	SuggestIgnore   []string        `json:"suggest_ignore"`
}

type DomainSummary struct {
	Domain     string   `json:"domain"`
	Requests   int      `json:"requests"`
	Endpoints  int      `json:"endpoints"`
	Methods    []string `json:"methods"`
	IsPrimary  bool     `json:"is_primary"`
	IsIgnored  bool     `json:"is_ignored"`
	LikelyType string   `json:"likely_type,omitempty"` // analytics, cdn, tracking, api, unknown
}

type PageSummary struct {
	PageDomain string `json:"page_domain"`
	Requests   int    `json:"requests"`
}

// Noise patterns are now in internal/noise/patterns.go for shared use

func buildSummary(tempStore *store.Store, domains []store.DomainInfo, persistentStore *store.Store) Summary {
	summary := Summary{
		TotalRequests:   tempStore.Count(),
		UniqueDomains:   len(domains),
		IgnoredDomains:  len(persistentStore.GetIgnoredDomains()),
		PrimaryDomains:  persistentStore.GetPrimaryDomains(),
		MethodBreakdown: make(map[string]int),
		StatusBreakdown: make(map[string]int),
		PageBreakdown:   []PageSummary{},
		TopDomains:      []DomainSummary{},
		SuggestIgnore:   []string{},
	}

	// Build method and status breakdown from all requests
	pageCounts := make(map[string]int)
	pageOrder := make([]string, 0)
	for _, req := range tempStore.Filter(store.FilterOptions{}) {
		summary.MethodBreakdown[req.Method]++
		if req.Response != nil {
			statusRange := fmt.Sprintf("%dxx", req.Response.Status/100)
			summary.StatusBreakdown[statusRange]++
		}
		pageDomain := pageDomainFromRequest(req)
		if pageDomain != "" {
			if _, exists := pageCounts[pageDomain]; !exists {
				pageOrder = append(pageOrder, pageDomain)
			}
			pageCounts[pageDomain]++
		}
	}

	for _, pageDomain := range pageOrder {
		summary.PageBreakdown = append(summary.PageBreakdown, PageSummary{
			PageDomain: pageDomain,
			Requests:   pageCounts[pageDomain],
		})
	}

	// Build top domains and suggestions
	suggestMap := make(map[string]bool)
	for _, d := range domains {
		methods := make([]string, 0, len(d.Methods))
		for m := range d.Methods {
			methods = append(methods, m)
		}
		sort.Strings(methods)

		// Use shared noise detection
		likelyType := noise.DetectNoiseType(d.Domain)
		if likelyType != "" && !d.IsIgnored && !d.IsPrimary {
			suggestMap[d.Domain] = true
		}

		summary.TopDomains = append(summary.TopDomains, DomainSummary{
			Domain:     d.Domain,
			Requests:   d.RequestCount,
			Endpoints:  len(d.Endpoints),
			Methods:    methods,
			IsPrimary:  d.IsPrimary,
			IsIgnored:  d.IsIgnored,
			LikelyType: likelyType,
		})
	}

	for domain := range suggestMap {
		summary.SuggestIgnore = append(summary.SuggestIgnore, domain)
	}
	sort.Strings(summary.SuggestIgnore)

	return summary
}

func printSummary(summary Summary, domains []store.DomainInfo, s *store.Store) {
	// Header box
	pterm.DefaultBox.WithTitle("Traffic Summary").WithTitleTopCenter().Println(
		fmt.Sprintf("Total Requests: %d\nUnique Domains: %d\nIgnored: %d",
			summary.TotalRequests, summary.UniqueDomains, summary.IgnoredDomains))

	// Method breakdown
	fmt.Println()
	pterm.DefaultSection.Println("Methods")
	for method, count := range summary.MethodBreakdown {
		pterm.Printf("  %-8s %d\n", method, count)
	}

	// Status breakdown
	fmt.Println()
	pterm.DefaultSection.Println("Response Status")
	for status, count := range summary.StatusBreakdown {
		pterm.Printf("  %-8s %d\n", status, count)
	}

	// Page breakdown (dev panel style)
	if len(summary.PageBreakdown) > 0 {
		fmt.Println()
		pterm.DefaultSection.Println("Pages")
		for _, page := range summary.PageBreakdown {
			fmt.Printf("  %s (%d)\n", page.PageDomain, page.Requests)
		}
	}

	// Primary domains
	if len(summary.PrimaryDomains) > 0 {
		fmt.Println()
		pterm.DefaultSection.Println("Primary Domains")
		for _, d := range summary.PrimaryDomains {
			pterm.Success.Printf("  %s\n", d)
		}
	}

	// Top domains
	fmt.Println()
	pterm.DefaultSection.Println("Domain Breakdown")

	// Create table data
	tableData := pterm.TableData{{"Domain", "Requests", "Endpoints", "Type", "Status"}}

	limit := 20
	if len(summary.TopDomains) < limit {
		limit = len(summary.TopDomains)
	}

	for i := 0; i < limit; i++ {
		d := summary.TopDomains[i]
		status := ""
		if d.IsPrimary {
			status = "PRIMARY"
		} else if d.IsIgnored {
			status = "IGNORED"
		}
		tableData = append(tableData, []string{
			d.Domain,
			fmt.Sprintf("%d", d.Requests),
			fmt.Sprintf("%d", d.Endpoints),
			d.LikelyType,
			status,
		})
	}

	pterm.DefaultTable.WithHasHeader().WithData(tableData).Render()

	if len(domains) > 20 {
		pterm.Printf("  ... and %d more domains\n", len(domains)-20)
	}

	// Suggestions
	if len(summary.SuggestIgnore) > 0 {
		fmt.Println()
		pterm.DefaultSection.Println("Suggested Ignore (analytics/tracking/CDN)")
		pterm.Warning.Printf("  Found %d domains that look like noise\n", len(summary.SuggestIgnore))
		fmt.Println()

		// Show domains
		for _, d := range summary.SuggestIgnore {
			fmt.Printf("    %s\n", d)
		}

		fmt.Println()
		pterm.Info.Println("To ignore these domains:")
		fmt.Printf("  rep ignore %s\n", strings.Join(summary.SuggestIgnore, " "))
	}

	// Next steps
	fmt.Println()
	pterm.DefaultSection.Println("Next Steps")
	fmt.Println("  rep domains              List all domains")
	fmt.Println("  rep list                 List requests (compact)")
	fmt.Println("  rep list -d <domain>     Filter by domain")
	fmt.Println("  rep body <id>            Get full response body")
}

func pageDomainFromRequest(req store.Request) string {
	pageURL := strings.TrimSpace(req.PageURL)
	if pageURL == "" {
		pageURL = strings.TrimSpace(req.URL)
	}
	if pageURL == "" {
		return ""
	}
	if host := hostFromURL(pageURL); host != "" {
		return host
	}
	if req.Domain != "" {
		return req.Domain
	}
	return pageURL
}

func hostFromURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Host != "" {
		return parsed.Hostname()
	}
	if !strings.Contains(raw, "://") {
		parsed, err = url.Parse("https://" + raw)
		if err == nil && parsed.Host != "" {
			return parsed.Hostname()
		}
	}
	return ""
}

func init() {
	rootCmd.AddCommand(summaryCmd)
	summaryCmd.Flags().StringVar(&summarySaved, "saved", "", "Read from saved session (ID, prefix, or 'latest')")
}
