package cmd

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	listDomain         string
	listMethod         string
	listStatus         int
	listStatusRange    string
	listPattern        string
	listLimit          int
	listOffset         int
	listPrimary        bool
	listIncludeIgnored bool
	listLine           bool
	listDetail         bool
	// New flags for agent-optimized filtering
	listType        string // Comma-separated resource types: script,xhr,fetch,document
	listAPI         bool   // Preset: API calls only (xmlhttprequest,fetch)
	listInteresting bool   // Preset: Error responses + state-changing methods
	listErrors      bool   // Preset: Only error responses (4xx/5xx)
	listMutations   bool   // Preset: Only state-changing methods
	listSaved       string // Session ID to read from saved sessions
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List captured requests",
	Long: `List HTTP requests with optional filters.

Default: Shows LIVE requests to PRIMARY domains only.
Use 'rep summary' to see all domains, then 'rep primary <domain>' to add targets.

Output modes (controlled by --output flag):
  compact   Show truncated response bodies
  meta      Show headers only, no bodies
  full      Show complete response bodies
  json      Raw JSON output

Presets (agent-optimized shortcuts):
  --api          API calls only (xmlhttprequest, fetch)
  --errors       Only error responses (4xx/5xx)
  --mutations    Only state-changing methods (POST/PUT/DELETE/PATCH)
  --interesting  Errors + mutations combined

Data sources:
  (default)              Show live.json (real-time, same as extension)
  --saved <id>           Show saved session by ID/prefix
  --saved latest         Show most recent saved session

Examples:
  rep list                          List requests to primary domains
  rep list --primary=false          List ALL requests (bypass primary filter)
  rep list --saved latest           List most recent saved session
  rep list --saved 20231227         List session starting with 20231227
  rep list --api                    Only API calls (xhr/fetch)
  rep list --interesting            Errors + state-changing methods
  rep list --type script            Only JavaScript files
  rep list --detail                 Multi-line request output
  rep list -d api.example.com       Filter by domain
  rep list -m POST                  Filter by method
  rep list --status 200             Filter by exact status
  rep list --status-range 4xx       Filter by status range
  rep list -p "api/v1"              Filter by URL pattern (regex)
  rep list --limit 10               Limit results
  rep list -o full                  Show full response bodies
  rep list --line | rg "Login"      Grep-friendly one-line output with IDs
  rep body <id>                     Fetch full response body by ID`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Apply presets before building filter
		resourceTypes := parseCommaSeparated(listType)
		methods := parseCommaSeparated(listMethod)
		statusRanges := []string{}

		if listAPI {
			// Preset: API calls only (xhr/fetch)
			resourceTypes = []string{"xmlhttprequest", "fetch"}
		}

		if listInteresting {
			// Preset: Error responses + state-changing methods
			statusRanges = []string{"4xx", "5xx"}
			if len(methods) == 0 {
				methods = []string{"POST", "PUT", "DELETE", "PATCH"}
			}
		}

		if listErrors {
			// Preset: Only error responses
			statusRanges = []string{"4xx", "5xx"}
		}

		if listMutations {
			// Preset: Only state-changing methods
			if len(methods) == 0 {
				methods = []string{"POST", "PUT", "DELETE", "PATCH"}
			}
		}

		// Build filter options
		opts := store.FilterOptions{
			Domain:         listDomain,
			Method:         strings.ToUpper(listMethod),
			Methods:        methods,
			Status:         listStatus,
			StatusRange:    listStatusRange,
			StatusRanges:   statusRanges,
			ResourceTypes:  resourceTypes,
			Pattern:        listPattern,
			Limit:          listLimit,
			Offset:         listOffset,
			PrimaryOnly:    listPrimary,
			ExcludeIgnored: !listIncludeIgnored,
		}

		var requests []store.Request
		var totalCount int

		if listSaved != "" {
			// Load from saved session in store.json
			s, err := store.Get()
			if err != nil {
				return fmt.Errorf("failed to load store: %w", err)
			}

			var session *store.Session
			if listSaved == "latest" || listSaved == "last" {
				session = s.GetLatestSession()
			} else {
				session = s.GetSession(listSaved)
			}

			if session == nil {
				pterm.Warning.Printf("Session not found: %s\n", listSaved)
				pterm.Info.Println("Use 'rep sessions' to list available sessions")
				return nil
			}

			// Create temp store for filtering
			tempStore := store.NewTempStore(session.Requests)
			tempStore.PrimaryDomains = s.PrimaryDomains
			tempStore.IgnoredDomains = s.IgnoredDomains
			tempStore.MutedPaths = s.MutedPaths

			if listPrimary && len(s.GetPrimaryDomains()) == 0 {
				pterm.Info.Println("No primary domains set. Use 'rep primary <domain>' to add.")
				return nil
			}

			// Get total count first (without limit)
			if opts.Limit > 0 {
				unlimitedOpts := opts
				unlimitedOpts.Limit = 0
				unlimitedOpts.Offset = 0
				totalCount = len(tempStore.Filter(unlimitedOpts))
			}
			requests = tempStore.Filter(opts)
		} else {
			// Default: Load from live.json (real-time, same as extension)
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
			// Filter live requests using store's filter logic
			tempStore := store.NewTempStore(export.Requests)
			// Load ignore/primary/mute lists from persistent store
			s, err := store.Get()
			if err == nil {
				tempStore.PrimaryDomains = s.PrimaryDomains
				tempStore.IgnoredDomains = s.IgnoredDomains
				tempStore.MutedPaths = s.MutedPaths
			}
			if listPrimary && len(tempStore.GetPrimaryDomains()) == 0 {
				pterm.Info.Println("No primary domains set. Use 'rep primary <domain>' to add.")
				return nil
			}

			// Get total count first (without limit)
			if opts.Limit > 0 {
				unlimitedOpts := opts
				unlimitedOpts.Limit = 0
				unlimitedOpts.Offset = 0
				totalCount = len(tempStore.Filter(unlimitedOpts))
			}
			requests = tempStore.Filter(opts)
		}

		if len(requests) == 0 {
			pterm.Info.Println("No requests match the filter")
			return nil
		}

		// Determine output mode
		mode := store.OutputCompact
		switch getOutputMode() {
		case "meta":
			mode = store.OutputMeta
		case "full":
			mode = store.OutputFull
		case "json":
			mode = store.OutputJSON
		}

		if mode == store.OutputJSON || getOutputMode() == "json" {
			formatted := output.FormatRequests(requests, mode)
			out, _ := sonic.MarshalIndent(formatted, "", "  ")
			fmt.Println(string(out))
			return nil
		}

		useLine := listLine && !listDetail && mode == store.OutputCompact
		if useLine {
			printRequestsLine(requests, totalCount, opts.Limit)
		} else {
			printRequests(requests, mode, totalCount, opts.Limit)
		}

		return nil
	},
}

func printRequests(requests []store.Request, mode store.OutputMode, totalCount int, limit int) {
	for _, req := range requests {
		printRequest(&req, mode)
		fmt.Println()
	}
	// Show truncation indicator when limited
	if limit > 0 && totalCount > len(requests) {
		pterm.Info.Printf("[Showing %d of %d requests. Use --offset to paginate]\n", len(requests), totalCount)
	} else {
		pterm.Info.Printf("Showing %d requests\n", len(requests))
	}
	fmt.Println("Use 'rep body <id>' to get full response body for a specific request")
}

func printRequestsLine(requests []store.Request, totalCount int, limit int) {
	for _, req := range requests {
		status := 0
		if req.Response != nil {
			status = req.Response.Status
		}
		url := output.SanitizeText(req.URL)
		fmt.Printf("[%s] %s %s → %d\n", req.ID, req.Method, url, status)
	}
	// Show truncation indicator when limited
	if limit > 0 && totalCount > len(requests) {
		fmt.Printf("[Showing %d of %d requests]\n", len(requests), totalCount)
	}
}

func printRequest(req *store.Request, mode store.OutputMode) {
	// Status with color
	status := 0
	statusColor := pterm.FgWhite
	if req.Response != nil {
		status = req.Response.Status
		if status >= 200 && status < 300 {
			statusColor = pterm.FgGreen
		} else if status >= 300 && status < 400 {
			statusColor = pterm.FgYellow
		} else if status >= 400 && status < 500 {
			statusColor = pterm.FgRed
		} else if status >= 500 {
			statusColor = pterm.FgMagenta
		}
	}

	// Header line
	pterm.DefaultBox.WithTitle(req.ID).Println(
		fmt.Sprintf("%s %s\nStatus: %s",
			pterm.Bold.Sprint(req.Method),
			req.URL,
			pterm.NewStyle(statusColor).Sprintf("%d", status)))

	// Request headers (always show key ones)
	if len(req.Headers) > 0 {
		fmt.Println("  Request Headers:")
		importantHeaders := []string{"content-type", "authorization", "cookie", "x-api-key", "accept"}
		for _, h := range importantHeaders {
			key, values := store.HeaderValuesWithKey(req.Headers, h)
			if len(values) == 0 {
				continue
			}
			if key == "" {
				key = h
			}
			for _, v := range values {
				v = output.SanitizeText(v)
				// Mask sensitive values
				if h == "authorization" || h == "cookie" || h == "x-api-key" {
					if len(v) > 20 {
						v = v[:10] + "..." + v[len(v)-5:]
					}
				}
				fmt.Printf("    %s: %s\n", key, v)
			}
		}
	}

	// Request body
	if req.Body != "" {
		fmt.Println("  Request Body:")
		body := req.Body
		if mode == store.OutputCompact && len(body) > 200 {
			body = body[:200] + fmt.Sprintf("\n    [...truncated, %s total]", output.FormatBodySize(len(req.Body)))
		}
		body = output.SanitizeText(body)
		for _, line := range strings.Split(body, "\n") {
			fmt.Printf("    %s\n", line)
		}
	}

	// Response
	if req.Response != nil && mode != store.OutputMeta {
		fmt.Println("  Response Headers:")
		for _, h := range []string{"content-type", "content-length"} {
			key, values := store.HeaderValuesWithKey(req.Response.Headers, h)
			if len(values) == 0 {
				continue
			}
			if key == "" {
				key = h
			}
			for _, v := range values {
				v = output.SanitizeText(v)
				fmt.Printf("    %s: %s\n", key, v)
			}
		}

		if req.Response.Body != "" {
			fmt.Println("  Response Body:")
			// Get content type
			contentType := store.HeaderFirst(req.Response.Headers, "content-type")

			var body string
			if mode == store.OutputFull {
				body = req.Response.Body
			} else {
				body, _ = output.TruncateBody(req.Response.Body, contentType, store.DefaultTruncateConfig())
			}

			body = output.SanitizeText(body)
			for _, line := range strings.Split(body, "\n") {
				fmt.Printf("    %s\n", line)
			}
		}
	}
}

// parseCommaSeparated splits a comma-separated string into a slice
func parseCommaSeparated(input string) []string {
	if input == "" {
		return nil
	}
	parts := strings.Split(input, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func init() {
	rootCmd.AddCommand(listCmd)
	listCmd.Flags().StringVarP(&listDomain, "domain", "d", "", "Filter by domain")
	listCmd.Flags().StringVarP(&listMethod, "method", "m", "", "Filter by HTTP method (or comma-separated list)")
	listCmd.Flags().IntVar(&listStatus, "status", 0, "Filter by exact status code")
	listCmd.Flags().StringVar(&listStatusRange, "status-range", "", "Filter by status range (2xx, 3xx, 4xx, 5xx)")
	listCmd.Flags().StringVarP(&listPattern, "pattern", "p", "", "Filter by URL pattern (regex)")
	listCmd.Flags().IntVarP(&listLimit, "limit", "l", 0, "Limit number of results")
	listCmd.Flags().IntVar(&listOffset, "offset", 0, "Skip first N results")
	listCmd.Flags().BoolVar(&listPrimary, "primary", true, "Only show requests to primary domains (default)")
	listCmd.Flags().BoolVar(&listIncludeIgnored, "include-ignored", false, "Include requests to ignored domains")
	listCmd.Flags().BoolVar(&listLine, "line", true, "One-line output with request ID (default)")
	listCmd.Flags().BoolVar(&listDetail, "detail", false, "Show multi-line request details")
	// New agent-optimized flags
	listCmd.Flags().StringVar(&listType, "type", "", "Filter by resource type (script,xmlhttprequest,fetch,document)")
	listCmd.Flags().BoolVar(&listAPI, "api", false, "Preset: API calls only (xmlhttprequest, fetch)")
	listCmd.Flags().BoolVar(&listInteresting, "interesting", false, "Preset: Error responses (4xx/5xx) + state-changing methods")
	listCmd.Flags().BoolVar(&listErrors, "errors", false, "Preset: Only error responses (4xx/5xx)")
	listCmd.Flags().BoolVar(&listMutations, "mutations", false, "Preset: Only state-changing methods (POST/PUT/DELETE/PATCH)")
	// Data source
	listCmd.Flags().StringVar(&listSaved, "saved", "", "Read from saved session (ID, prefix, or 'latest')")
}
