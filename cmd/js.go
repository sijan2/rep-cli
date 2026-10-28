package cmd

import (
	"fmt"
	"net/url"
	"sort"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/noise"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	jsURLs  bool   // Just list URLs (for curl piping)
	jsGraph bool   // Show dependency graph
	jsCurl  bool   // Output curl commands
	jsSaved string // Session ID to read from
)

// JSFile represents a JavaScript file for output
type JSFile struct {
	RequestID         string            `json:"request_id"`
	BodyCapture       store.BodyCapture `json:"body_capture"`
	PayloadValidation string            `json:"payload_validation"`
	URL               string            `json:"url"`
	Domain            string            `json:"domain"`
	PageURL           string            `json:"page_url,omitempty"`
	Size              int               `json:"size,omitempty"`
	Status            int               `json:"status"`
	Category          string            `json:"category,omitempty"` // first_party, third_party, cdn
}

// JSPageDeps represents JavaScript dependencies for a page
type JSPageDeps struct {
	PageURL string   `json:"page_url"`
	Scripts []string `json:"scripts"`
}

// JSOutput is the full JSON output structure
type JSOutput struct {
	Provenance      observedCaptureProvenance `json:"provenance"`
	Coverage        CaptureCoverageReport     `json:"coverage"`
	SourceAnalysis  string                    `json:"source_analysis"`
	FirstPartyJS    []JSFile                  `json:"first_party_js"`
	ThirdPartyJS    []JSFile                  `json:"third_party_js"`
	CDNScripts      []JSFile                  `json:"cdn_scripts"`
	DependencyGraph []JSPageDeps              `json:"dependency_graph,omitempty"`
	CurlCommands    []string                  `json:"curl_commands,omitempty"`
	Summary         JSSummary                 `json:"summary"`
}

// JSSummary provides counts for quick overview
type JSSummary struct {
	ObservedRequests  int `json:"observed_requests"`
	DuplicateRequests int `json:"duplicate_requests"`
	TotalScripts      int `json:"total_scripts"`
	FirstPartyCount   int `json:"first_party_count"`
	ThirdPartyCount   int `json:"third_party_count"`
	CDNCount          int `json:"cdn_count"`
	UniquePages       int `json:"unique_pages"`
}

var jsCmd = &cobra.Command{
	Use:   "js",
	Short: "Inventory captured JavaScript and its body evidence",
	Long: `List all JavaScript URLs captured in traffic.

Default: Analyzes LIVE session traffic (real-time).
Use --saved to analyze archived sessions.

Each script includes its captured request ID, decoded body size and evidence
state. This command inventories observed scripts; source analysis is separate.
Use body REQUEST_ID --saved HASH to retrieve the original captured evidence.

Includes both first-party and third-party/CDN scripts.
Categorizes scripts as:
  - First-party: Same base domain as page that loaded it
  - Third-party: Different domain, not a known CDN
  - CDN: Known CDN domains (jsdelivr, cloudflare, unpkg, etc.)

Examples:
  rep js                       Show JS summary with URLs
  rep js --urls                Just URLs, one per line (for curl)
  rep js --graph               Show page -> JS dependency graph
  rep js --curl                Generate curl commands for download
  rep js --saved latest        Analyze saved session
  rep js -o json               Full structured output for agents`,
	RunE: runJS,
}

func runJS(cmd *cobra.Command, args []string) error {
	capture, err := loadObservedCapture(jsSaved)
	if err != nil {
		return err
	}
	tempStore := store.NewTempStore(capture.Export.Requests)

	// Get all JavaScript requests
	jsRequests := getJSRequests(tempStore)

	// Categorize scripts
	output := categorizeJS(jsRequests)
	output.Provenance = capture.Provenance
	output.Coverage = capture.coverage()
	output.SourceAnalysis = "not_performed"

	// Handle different output modes
	if jsURLs {
		// Plain URLs, one per line
		printJSURLs(output)
		return nil
	}

	if jsCurl {
		// Generate curl commands
		printCurlCommands(output)
		return nil
	}

	if jsGraph {
		// Show dependency graph
		output.DependencyGraph = buildDependencyGraph(jsRequests)
	}

	if getOutputMode() == "json" {
		if jsCurl {
			output.CurlCommands = generateCurlCommands(output)
		}
		out, err := sonic.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return err
	}

	// Default: summary view
	printJSSummary(output)
	return nil
}

// getJSRequests returns all requests that are JavaScript files
func getJSRequests(s *store.Store) []store.Request {
	var jsReqs []store.Request

	// Get all requests (including ignored domains for JS analysis)
	allRequests := s.Filter(store.FilterOptions{
		ExcludeIgnored: false, // Include ignored domains for JS
	})

	for _, req := range allRequests {
		if isJavaScript(&req) {
			jsReqs = append(jsReqs, req)
		}
	}

	return jsReqs
}

// isJavaScript checks if a request is a JavaScript file
func isJavaScript(req *store.Request) bool {
	return captureIsScript(req)
}

// categorizeJS categorizes JavaScript files by first-party/third-party/CDN
func categorizeJS(requests []store.Request) JSOutput {
	output := JSOutput{
		FirstPartyJS: []JSFile{},
		ThirdPartyJS: []JSFile{},
		CDNScripts:   []JSFile{},
	}

	seen := make(map[string]bool) // Dedupe by URL
	pageSet := make(map[string]bool)

	for _, req := range requests {
		if req.PageURL != "" {
			pageSet[req.PageURL] = true
		}
		if seen[req.URL] {
			continue
		}
		seen[req.URL] = true

		status := 0
		if req.Response != nil {
			status = req.Response.Status
		}
		body, _, capture, bodyErr := bodyEvidence(&req, false)
		metadataCheck := captureBodyCounts{States: map[string]int{}}
		captureAccumulateBody(&metadataCheck, &req, false)
		validation := "validated"
		if bodyErr != nil || metadataCheck.MetadataInvalid > 0 {
			validation = "invalid"
			capture.State = "unavailable"
			capture.Reason = "invalid_retained_payload"
		} else {
			capture.State = browserBodyState(capture.State)
		}

		jsFile := JSFile{
			URL:               req.URL,
			Domain:            req.Domain,
			PageURL:           req.PageURL,
			Size:              len(body),
			Status:            status,
			RequestID:         req.ID,
			BodyCapture:       capture,
			PayloadValidation: validation,
		}

		// Categorize
		category := categorizeScript(&req)
		jsFile.Category = category

		switch category {
		case "first_party":
			output.FirstPartyJS = append(output.FirstPartyJS, jsFile)
		case "cdn":
			output.CDNScripts = append(output.CDNScripts, jsFile)
		default:
			output.ThirdPartyJS = append(output.ThirdPartyJS, jsFile)
		}
	}

	// Build summary
	output.Summary = JSSummary{
		ObservedRequests:  len(requests),
		DuplicateRequests: len(requests) - len(seen),
		TotalScripts:      len(output.FirstPartyJS) + len(output.ThirdPartyJS) + len(output.CDNScripts),
		FirstPartyCount:   len(output.FirstPartyJS),
		ThirdPartyCount:   len(output.ThirdPartyJS),
		CDNCount:          len(output.CDNScripts),
		UniquePages:       len(pageSet),
	}

	return output
}

// categorizeScript determines if a script is first-party, third-party, or CDN
func categorizeScript(req *store.Request) string {
	// Check if CDN first
	if noise.IsCDN(req.Domain) {
		return "cdn"
	}

	// Check first-party using PageURL
	if req.PageURL != "" {
		parsedPage, err := url.Parse(req.PageURL)
		if err == nil && parsedPage.Host != "" {
			if store.IsFirstParty(req.Domain, parsedPage.Host) {
				return "first_party"
			}
		}
	}

	return "third_party"
}

// buildDependencyGraph creates a page -> scripts mapping
func buildDependencyGraph(requests []store.Request) []JSPageDeps {
	pageMap := make(map[string][]string)

	for _, req := range requests {
		if req.PageURL == "" {
			continue
		}
		pageMap[req.PageURL] = append(pageMap[req.PageURL], req.URL)
	}

	var result []JSPageDeps
	for pageURL, scripts := range pageMap {
		// Dedupe scripts
		seen := make(map[string]bool)
		var unique []string
		for _, s := range scripts {
			if !seen[s] {
				seen[s] = true
				unique = append(unique, s)
			}
		}
		sort.Strings(unique)

		result = append(result, JSPageDeps{
			PageURL: pageURL,
			Scripts: unique,
		})
	}

	// Sort by page URL
	sort.Slice(result, func(i, j int) bool {
		return result[i].PageURL < result[j].PageURL
	})

	return result
}

// printJSURLs prints just the URLs, one per line
func printJSURLs(output JSOutput) {
	// Print first-party first (most relevant for analysis)
	for _, js := range output.FirstPartyJS {
		fmt.Println(js.URL)
	}
	// Then third-party
	for _, js := range output.ThirdPartyJS {
		fmt.Println(js.URL)
	}
	// Then CDN
	for _, js := range output.CDNScripts {
		fmt.Println(js.URL)
	}
}

// printCurlCommands prints curl commands to download all JS files
func printCurlCommands(output JSOutput) {
	commands := generateCurlCommands(output)
	for _, cmd := range commands {
		fmt.Println(cmd)
	}
}

// generateCurlCommands creates curl commands for downloading JS files
func generateCurlCommands(output JSOutput) []string {
	var commands []string

	// First-party scripts (most relevant)
	for _, js := range output.FirstPartyJS {
		commands = append(commands, fmt.Sprintf("curl -sLO '%s'", js.URL))
	}
	// Third-party scripts
	for _, js := range output.ThirdPartyJS {
		commands = append(commands, fmt.Sprintf("curl -sLO '%s'", js.URL))
	}
	// CDN scripts (often useful for version fingerprinting)
	for _, js := range output.CDNScripts {
		commands = append(commands, fmt.Sprintf("curl -sLO '%s'", js.URL))
	}

	return commands
}

// printJSSummary prints a human-readable summary
func printJSSummary(output JSOutput) {
	fmt.Printf("Source: %s; capture: %s; source analysis: %s\n", output.Provenance.SourceStatus, output.Coverage.Capture.State, output.SourceAnalysis)
	// Summary box
	pterm.DefaultBox.WithTitle("JavaScript Analysis").WithTitleTopCenter().Println(
		fmt.Sprintf("Total Scripts: %d\nFirst-Party: %d\nThird-Party: %d\nCDN: %d\nUnique Pages: %d",
			output.Summary.TotalScripts,
			output.Summary.FirstPartyCount,
			output.Summary.ThirdPartyCount,
			output.Summary.CDNCount,
			output.Summary.UniquePages))

	// First-party scripts
	if len(output.FirstPartyJS) > 0 {
		fmt.Println()
		pterm.DefaultSection.Println("First-Party Scripts (most relevant for analysis)")
		for _, js := range output.FirstPartyJS {
			fmt.Printf("  %s\n", js.URL)
		}
	}

	// Third-party scripts
	if len(output.ThirdPartyJS) > 0 {
		fmt.Println()
		pterm.DefaultSection.Println("Third-Party Scripts")
		for _, js := range output.ThirdPartyJS {
			fmt.Printf("  %s\n", js.URL)
		}
	}

	// CDN scripts
	if len(output.CDNScripts) > 0 {
		fmt.Println()
		pterm.DefaultSection.Println("CDN Scripts")
		for _, js := range output.CDNScripts {
			fmt.Printf("  %s\n", js.URL)
		}
	}

	// Next steps
	fmt.Println()
	pterm.DefaultSection.Println("Next Steps")
	fmt.Println("  rep js --urls > urls.txt                      # Export URLs")
	fmt.Println("  rep js --urls | xargs -I{} curl -sLO {}       # Download all")
	fmt.Println("  rep js --graph                                # Show page dependencies")
	fmt.Println("  rep js -o json                                # Full JSON output")
}

func init() {
	rootCmd.AddCommand(jsCmd)
	jsCmd.Flags().BoolVar(&jsURLs, "urls", false, "Just print URLs, one per line (for curl/wget)")
	jsCmd.Flags().BoolVar(&jsGraph, "graph", false, "Show page -> JS dependency graph")
	jsCmd.Flags().BoolVar(&jsCurl, "curl", false, "Generate curl commands for downloading")
	jsCmd.Flags().StringVar(&jsSaved, "saved", "", "Read from saved session (ID, prefix, or 'latest')")
}
