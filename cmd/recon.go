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
	reconFlows bool   // Include cross-domain flow analysis
	reconSaved string // Session ID to read from
)

// ReconOutput is the structured output for agent consumption
type ReconOutput struct {
	Provenance       observedCaptureProvenance `json:"provenance"`
	Coverage         CaptureCoverageReport     `json:"coverage"`
	Target           string                    `json:"target"`
	TotalRequests    int                       `json:"total_requests"`
	FirstParty       DomainBreakdown           `json:"first_party"`
	ThirdParty       DomainBreakdown           `json:"third_party"`
	NoiseDetected    []NoiseDomain             `json:"noise_detected"`
	SuggestedIgnore  string                    `json:"suggested_ignore_command,omitempty"`
	CrossDomainFlows []CrossDomainFlow         `json:"cross_domain_flows,omitempty"`
	NextSteps        []string                  `json:"next_steps"`
}

// DomainBreakdown groups domains by category
type DomainBreakdown struct {
	Domains   []ReconDomainSummary `json:"domains"`
	Requests  int                  `json:"requests"`
	Endpoints int                  `json:"endpoints"`
}

// ReconDomainSummary provides domain-level stats
type ReconDomainSummary struct {
	Domain    string   `json:"domain"`
	Requests  int      `json:"requests"`
	Endpoints int      `json:"endpoints"`
	Methods   []string `json:"methods"`
	IsPrimary bool     `json:"is_primary,omitempty"`
	IsIgnored bool     `json:"is_ignored,omitempty"`
}

// NoiseDomain represents a detected noise domain
type NoiseDomain struct {
	Domain   string `json:"domain"`
	Type     string `json:"type"` // analytics, cdn, tracking, ads, social, monitoring
	Requests int    `json:"requests"`
}

// CrossDomainFlow shows requests grouped by originating page
type CrossDomainFlow struct {
	PageURL          string   `json:"page_url"`
	PageDomain       string   `json:"page_domain"`
	RequestedDomains []string `json:"requested_domains"`
	RequestCount     int      `json:"request_count"`
	IsFirstParty     bool     `json:"is_first_party"`
}

var reconCmd = &cobra.Command{
	Use:   "recon <target-domain>",
	Short: "Organize observed traffic and report capture coverage",
	Long: `Organize captured traffic for a target domain.

Default: Analyzes LIVE session traffic (real-time).
Use --saved to analyze archived sessions.

Analyzes captured traffic for a target domain:
  - Sets target as primary domain
  - Groups requests into first-party vs third-party
  - Detects noise domains (analytics, CDN, tracking)
  - Shows cross-domain request flows (using PageURL)
  - Provides suggested next commands

Coverage reports describe retained traffic and known capture gaps.
Unvisited features and uncaptured traffic remain unknown.

Examples:
  rep recon example.com               Interactive recon overview
  rep recon example.com -o json       Full structured output for agents
  rep recon example.com --flows       Include cross-domain flow analysis
  rep recon example.com --saved latest  Analyze saved session`,
	Args: cobra.ExactArgs(1),
	RunE: runRecon,
}

func runRecon(cmd *cobra.Command, args []string) error {
	targetDomain := strings.ToLower(strings.TrimSpace(args[0]))
	if !validSummaryDomain(targetDomain) {
		return fmt.Errorf("target must be a hostname without a scheme, port, path, or wildcard")
	}
	capture, err := loadObservedCapture(reconSaved)
	if err != nil {
		return err
	}

	var persistentStore *store.Store

	// Load persistent store for ignore/primary lists
	persistentStore, err = store.Get()
	if err != nil {
		return fmt.Errorf("failed to load store: %w", err)
	}

	tempStore := store.NewTempStore(capture.Export.Requests)

	// Apply ignore/primary lists
	tempStore.PrimaryDomains = persistentStore.PrimaryDomains
	tempStore.IgnoredDomains = persistentStore.IgnoredDomains

	// Set target as primary (helps with future filtering).
	persistentStore.SetPrimary(targetDomain)
	if err := persistentStore.Save(); err != nil {
		pterm.Warning.Printf("Could not save primary domain: %v\n", err)
	}

	// Get all requests (including ignored for full analysis)
	allRequests := tempStore.Filter(store.FilterOptions{
		ExcludeIgnored: false,
	})

	// Build recon output
	output := buildReconOutput(targetDomain, allRequests, tempStore)
	output.Provenance = capture.Provenance
	output.Coverage = capture.coverage()

	// Add cross-domain flows if requested
	if reconFlows {
		output.CrossDomainFlows = buildCrossDomainFlows(allRequests, targetDomain)
	}

	if getOutputMode() == "json" {
		out, err := sonic.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return err
	}

	// Interactive display
	printReconOutput(output, targetDomain)
	return nil
}

func buildReconOutput(target string, requests []store.Request, s *store.Store) ReconOutput {
	output := ReconOutput{
		Target:        target,
		TotalRequests: len(requests),
		FirstParty: DomainBreakdown{
			Domains: []ReconDomainSummary{},
		},
		ThirdParty: DomainBreakdown{
			Domains: []ReconDomainSummary{},
		},
		NoiseDetected: []NoiseDomain{},
		NextSteps:     []string{},
	}

	// Group requests by domain
	domainMap := make(map[string]*domainStats)

	for _, req := range requests {
		if req.Domain == "" {
			continue
		}

		stats, exists := domainMap[req.Domain]
		if !exists {
			stats = &domainStats{
				domain:    req.Domain,
				methods:   make(map[string]bool),
				endpoints: make(map[string]bool),
				isIgnored: s.IsIgnored(req.Domain),
				isPrimary: s.IsPrimary(req.Domain),
			}
			domainMap[req.Domain] = stats
		}

		stats.requests++
		stats.methods[req.Method] = true

		// Track unique endpoints (path without query)
		pathOnly := req.Path
		if idx := strings.Index(pathOnly, "?"); idx > 0 {
			pathOnly = pathOnly[:idx]
		}
		endpoint := fmt.Sprintf("%s %s", req.Method, pathOnly)
		stats.endpoints[endpoint] = true
	}

	// Categorize domains
	var noiseToIgnore []string
	targetBase := store.GetBaseDomain(target)

	for _, stats := range domainMap {
		summary := ReconDomainSummary{
			Domain:    stats.domain,
			Requests:  stats.requests,
			Endpoints: len(stats.endpoints),
			Methods:   mapKeys(stats.methods),
			IsPrimary: stats.isPrimary,
			IsIgnored: stats.isIgnored,
		}

		// Check if noise
		noiseType := noise.DetectNoiseType(stats.domain)
		if noiseType != "" {
			output.NoiseDetected = append(output.NoiseDetected, NoiseDomain{
				Domain:   stats.domain,
				Type:     noiseType,
				Requests: stats.requests,
			})
			if !stats.isIgnored {
				noiseToIgnore = append(noiseToIgnore, stats.domain)
			}
			continue // Don't add to first/third party lists
		}

		// Check if first-party (same base domain)
		domainBase := store.GetBaseDomain(stats.domain)
		if domainBase == targetBase {
			output.FirstParty.Domains = append(output.FirstParty.Domains, summary)
			output.FirstParty.Requests += stats.requests
			output.FirstParty.Endpoints += len(stats.endpoints)
		} else {
			output.ThirdParty.Domains = append(output.ThirdParty.Domains, summary)
			output.ThirdParty.Requests += stats.requests
			output.ThirdParty.Endpoints += len(stats.endpoints)
		}
	}

	// Sort domains by request count
	sort.Slice(output.FirstParty.Domains, func(i, j int) bool {
		if output.FirstParty.Domains[i].Requests == output.FirstParty.Domains[j].Requests {
			return output.FirstParty.Domains[i].Domain < output.FirstParty.Domains[j].Domain
		}
		return output.FirstParty.Domains[i].Requests > output.FirstParty.Domains[j].Requests
	})
	sort.Slice(output.ThirdParty.Domains, func(i, j int) bool {
		if output.ThirdParty.Domains[i].Requests == output.ThirdParty.Domains[j].Requests {
			return output.ThirdParty.Domains[i].Domain < output.ThirdParty.Domains[j].Domain
		}
		return output.ThirdParty.Domains[i].Requests > output.ThirdParty.Domains[j].Requests
	})
	sort.Slice(output.NoiseDetected, func(i, j int) bool {
		if output.NoiseDetected[i].Requests == output.NoiseDetected[j].Requests {
			return output.NoiseDetected[i].Domain < output.NoiseDetected[j].Domain
		}
		return output.NoiseDetected[i].Requests > output.NoiseDetected[j].Requests
	})

	// Build suggested ignore command
	if len(noiseToIgnore) > 0 {
		sort.Strings(noiseToIgnore)
		output.SuggestedIgnore = fmt.Sprintf("rep ignore %s", strings.Join(noiseToIgnore, " "))
	}

	// Build next steps
	output.NextSteps = buildNextSteps(output, target, noiseToIgnore)

	return output
}

type domainStats struct {
	domain    string
	requests  int
	methods   map[string]bool
	endpoints map[string]bool
	isIgnored bool
	isPrimary bool
}

func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func buildCrossDomainFlows(requests []store.Request, target string) []CrossDomainFlow {
	pageMap := make(map[string]*CrossDomainFlow)
	targetBase := store.GetBaseDomain(target)

	for _, req := range requests {
		if req.PageURL == "" {
			continue
		}

		flow, exists := pageMap[req.PageURL]
		if !exists {
			parsedPage, _ := url.Parse(req.PageURL)
			pageDomain := ""
			if parsedPage != nil {
				pageDomain = parsedPage.Host
			}

			isFirstParty := store.GetBaseDomain(pageDomain) == targetBase

			flow = &CrossDomainFlow{
				PageURL:          req.PageURL,
				PageDomain:       pageDomain,
				RequestedDomains: []string{},
				RequestCount:     0,
				IsFirstParty:     isFirstParty,
			}
			pageMap[req.PageURL] = flow
		}

		flow.RequestCount++

		// Track unique domains
		found := false
		for _, d := range flow.RequestedDomains {
			if d == req.Domain {
				found = true
				break
			}
		}
		if !found && req.Domain != "" {
			flow.RequestedDomains = append(flow.RequestedDomains, req.Domain)
		}
	}

	// Convert to slice and sort
	result := make([]CrossDomainFlow, 0, len(pageMap))
	for _, flow := range pageMap {
		sort.Strings(flow.RequestedDomains)
		result = append(result, *flow)
	}

	// Sort by request count descending
	sort.Slice(result, func(i, j int) bool {
		if result[i].RequestCount == result[j].RequestCount {
			return result[i].PageURL < result[j].PageURL
		}
		return result[i].RequestCount > result[j].RequestCount
	})

	return result
}

func buildNextSteps(output ReconOutput, target string, noiseToIgnore []string) []string {
	var steps []string

	// Step 1: Ignore noise if detected
	if len(noiseToIgnore) > 0 {
		steps = append(steps, output.SuggestedIgnore)
	}

	// Step 2: List API calls for primary domains
	steps = append(steps, "rep list --api --primary -o json")

	// Inspect captured script evidence before choosing any separate source work.
	steps = append(steps, "rep js -o json", "rep coverage")

	// Step 4: Find interesting responses
	steps = append(steps, "rep list --interesting -o json")

	// Step 5: Review specific domain
	if len(output.FirstParty.Domains) > 0 {
		topDomain := output.FirstParty.Domains[0].Domain
		if topDomain != target {
			steps = append(steps, fmt.Sprintf("rep list -d %s -o json", topDomain))
		}
	}

	return steps
}

func printReconOutput(output ReconOutput, target string) {
	fmt.Printf("Source: %s; capture: %s; unvisited features: unknown\n", output.Provenance.SourceStatus, output.Coverage.Capture.State)
	// Header
	pterm.DefaultBox.WithTitle("Recon: " + target).WithTitleTopCenter().Println(
		fmt.Sprintf("Total Requests: %d\nFirst-Party Domains: %d (%d requests)\nThird-Party Domains: %d (%d requests)\nNoise Domains: %d",
			output.TotalRequests,
			len(output.FirstParty.Domains), output.FirstParty.Requests,
			len(output.ThirdParty.Domains), output.ThirdParty.Requests,
			len(output.NoiseDetected)))

	// First-party domains
	if len(output.FirstParty.Domains) > 0 {
		fmt.Println()
		pterm.DefaultSection.Println("First-Party Domains (same base domain)")
		tableData := pterm.TableData{{"Domain", "Requests", "Endpoints", "Methods"}}
		for _, d := range output.FirstParty.Domains {
			tableData = append(tableData, []string{
				d.Domain,
				fmt.Sprintf("%d", d.Requests),
				fmt.Sprintf("%d", d.Endpoints),
				strings.Join(d.Methods, ", "),
			})
		}
		pterm.DefaultTable.WithHasHeader().WithData(tableData).Render()
	}

	// Third-party domains (limit to top 10)
	if len(output.ThirdParty.Domains) > 0 {
		fmt.Println()
		pterm.DefaultSection.Println("Third-Party Domains")
		tableData := pterm.TableData{{"Domain", "Requests", "Endpoints"}}
		limit := 10
		if len(output.ThirdParty.Domains) < limit {
			limit = len(output.ThirdParty.Domains)
		}
		for i := 0; i < limit; i++ {
			d := output.ThirdParty.Domains[i]
			tableData = append(tableData, []string{
				d.Domain,
				fmt.Sprintf("%d", d.Requests),
				fmt.Sprintf("%d", d.Endpoints),
			})
		}
		pterm.DefaultTable.WithHasHeader().WithData(tableData).Render()
		if len(output.ThirdParty.Domains) > 10 {
			fmt.Printf("  ... and %d more\n", len(output.ThirdParty.Domains)-10)
		}
	}

	// Noise detected
	if len(output.NoiseDetected) > 0 {
		fmt.Println()
		pterm.DefaultSection.Println("Noise Detected (analytics, CDN, tracking)")
		for _, n := range output.NoiseDetected {
			fmt.Printf("  %s [%s] - %d requests\n", n.Domain, n.Type, n.Requests)
		}
		if output.SuggestedIgnore != "" {
			fmt.Println()
			pterm.Info.Println("Suggested: " + output.SuggestedIgnore)
		}
	}

	// Next steps
	fmt.Println()
	pterm.DefaultSection.Println("Next Steps")
	for i, step := range output.NextSteps {
		fmt.Printf("  %d. %s\n", i+1, step)
	}
}

func init() {
	rootCmd.AddCommand(reconCmd)
	reconCmd.Flags().BoolVar(&reconFlows, "flows", false, "Include cross-domain flow analysis")
	reconCmd.Flags().StringVar(&reconSaved, "saved", "", "Read from saved session (ID, prefix, or 'latest')")
}
