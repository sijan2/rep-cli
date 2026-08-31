package cmd

import (
	"fmt"
	"io"
	"os"
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
	listBudget         string // Token budget mode: minimal, compact, standard, full
	listSince          string // Time filter
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
		method := ""
		if len(methods) == 1 {
			method = strings.ToUpper(methods[0])
		}
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
			Method:         method,
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
			Since:          parseSinceTime(listSince),
		}

		var requests []store.Request
		var totalCount int
		var source string
		var candidates []store.Request
		var primaryCount int

		if listSaved != "" {
			// Load from saved session in sessions.jsonl
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
				return output.EmitAgentError(os.Stdout, output.NewAgentError(
					output.ErrCodeSessionNotFound,
					"list",
					fmt.Sprintf("session not found: %s", listSaved),
					"rep sessions",
				), getOutputMode() == "json")
			}

			source = "saved/" + session.ID

			// Create temp store for filtering
			tempStore := store.NewTempStore(session.Requests)
			tempStore.PrimaryDomains = s.PrimaryDomains
			tempStore.IgnoredDomains = s.IgnoredDomains
			tempStore.MutedPaths = s.MutedPaths
			candidates = session.Requests
			primaryCount = len(s.GetPrimaryDomains())

			if listPrimary && primaryCount == 0 {
				return emitListNoPrimary(os.Stdout, source, getOutputMode() == "json", useEnvelope())
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
				return emitLiveUnavailable("list", err)
			}
			if len(export.Requests) == 0 {
				emitLiveEmpty("list")
				return nil
			}
			source = "live.json"
			// Filter live requests using store's filter logic
			tempStore := store.NewTempStore(export.Requests)
			candidates = export.Requests
			// Load ignore/primary/mute lists from persistent store
			s, err := store.Get()
			if err == nil {
				tempStore.PrimaryDomains = s.PrimaryDomains
				tempStore.IgnoredDomains = s.IgnoredDomains
				tempStore.MutedPaths = s.MutedPaths
				primaryCount = len(s.GetPrimaryDomains())
			}
			if listPrimary && len(tempStore.GetPrimaryDomains()) == 0 {
				return emitListNoPrimary(os.Stdout, source, getOutputMode() == "json", useEnvelope())
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
			// Build an explainer in the caller's requested output format.
			primaries := loadPrimaryDomains()
			distinctDomains, sampleDomains, primaryMatches := candidateStats(candidates, primaries)
			return emitListEmptyResult(os.Stdout, output.EmptyResultContext{
				Command:               "list",
				Source:                source,
				TotalCandidates:       len(candidates),
				DistinctDomains:       distinctDomains,
				Filters:               opts,
				PrimaryCount:          primaryCount,
				PrimariesInCandidates: primaryMatches,
				SampleDomains:         sampleDomains,
			}, getOutputMode() == "json", useEnvelope())
		}

		mode := resolvedListOutputMode()

		if getOutputMode() == "json" {
			formatted := output.FormatRequests(requests, mode)
			var payload interface{} = formatted
			if useEnvelope() {
				env := output.WrapData("list", source, formatted)
				env.Filters = buildListFilterMap(opts)
				if opts.Limit > 0 && totalCount > len(formatted) {
					nextOffset := opts.Offset + len(formatted)
					env.Truncation = &output.TruncationInfo{
						Reason:   "size-cap",
						Returned: len(formatted),
						Total:    totalCount,
					}
					env.Suggest = []string{
						fmt.Sprintf("rep list --offset=%d --limit=%d", nextOffset, opts.Limit),
					}
				}
				payload = env
			}
			out, _ := sonic.MarshalIndent(payload, "", "  ")
			fmt.Println(string(out))
			return nil
		}

		// Disclose data source on the first non-JSON line.
		fmt.Println(output.FormatSourceLine(source))

		useLine := listLine && !listDetail && mode == store.OutputCompact
		if useLine {
			printRequestsLine(requests, totalCount, opts.Limit, opts.Offset)
		} else {
			printRequests(requests, mode, totalCount, opts.Limit, opts.Offset)
		}

		return nil
	},
}

func resolvedListOutputMode() store.OutputMode {
	return resolveListOutputMode(outputMode, jsonOutput)
}

func resolveListOutputMode(mode string, jsonFlag bool) store.OutputMode {
	if jsonFlag {
		return store.OutputJSON
	}
	switch mode {
	case "meta":
		return store.OutputMeta
	case "full":
		return store.OutputFull
	case "json":
		return store.OutputJSON
	default:
		return store.OutputCompact
	}
}

func emitListNoPrimary(writer io.Writer, source string, jsonMode, envelopeMode bool) error {
	if !jsonMode {
		_, err := fmt.Fprintln(writer, "No primary domains set. Use 'rep primary <domain>' to add.")
		return err
	}
	data := []output.RequestOutput{}
	var payload interface{} = data
	if envelopeMode {
		envelope := output.WrapData("list", source, data)
		envelope.Filters = map[string]interface{}{"primary": true}
		envelope.Suggest = []string{
			"rep primary <domain>",
			"rep list --primary=false -o json",
		}
		payload = envelope
	}
	encoded, err := sonic.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(writer, string(encoded))
	return err
}

func emitListEmptyResult(writer io.Writer, context output.EmptyResultContext, jsonMode, envelopeMode bool) error {
	if !jsonMode {
		_, err := fmt.Fprint(writer, output.FormatEmptyReason(context))
		return err
	}
	data := []output.RequestOutput{}
	var payload interface{} = data
	if envelopeMode {
		envelope := output.WrapData("list", context.Source, data)
		envelope.Filters = buildListFilterMap(context.Filters)
		if context.TotalCandidates == 0 {
			envelope.Suggest = []string{
				"rep browser status",
				"rep browse <url>",
				"rep summary",
			}
		} else {
			envelope.Suggest = []string{"rep list --primary=false -o json"}
			if len(context.SampleDomains) > 0 {
				envelope.Suggest = append(envelope.Suggest, fmt.Sprintf("rep primary --clear && rep primary %s", context.SampleDomains[0]))
			}
		}
		payload = envelope
	}
	encoded, err := sonic.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(writer, string(encoded))
	return err
}

// buildListFilterMap serializes active filter state into the envelope's
// filters map. Only non-default fields are emitted so consumers can cheaply
// see what actually constrained the query.
func buildListFilterMap(opts store.FilterOptions) map[string]interface{} {
	m := map[string]interface{}{
		"primary": opts.PrimaryOnly,
	}
	if opts.Domain != "" {
		m["domain"] = opts.Domain
	}
	if len(opts.Methods) > 0 {
		m["methods"] = opts.Methods
	} else if opts.Method != "" {
		m["method"] = opts.Method
	}
	if opts.Status > 0 {
		m["status"] = opts.Status
	}
	if opts.StatusRange != "" {
		m["status_range"] = opts.StatusRange
	}
	if len(opts.StatusRanges) > 0 {
		m["status_ranges"] = opts.StatusRanges
	}
	if len(opts.ResourceTypes) > 0 {
		m["resource_types"] = opts.ResourceTypes
	}
	if opts.Pattern != "" {
		m["pattern"] = opts.Pattern
	}
	if opts.Limit > 0 {
		m["limit"] = opts.Limit
	}
	if opts.Offset > 0 {
		m["offset"] = opts.Offset
	}
	return m
}

// candidateStats computes distinct domains, a sample of top domains, and how many
// configured primaries appear in the candidate set. Used by the empty-result explainer.
func candidateStats(reqs []store.Request, primaries []string) (distinct int, sample []string, primaryMatches int) {
	counts := make(map[string]int, 32)
	for i := range reqs {
		d := reqs[i].Domain
		if d == "" {
			continue
		}
		counts[d]++
	}
	distinct = len(counts)

	type kv struct {
		k string
		v int
	}
	pairs := make([]kv, 0, len(counts))
	for k, v := range counts {
		pairs = append(pairs, kv{k, v})
	}
	// Simple selection of up to 3 highest-count domains; small N so no need to sort fully.
	for i := 0; i < len(pairs); i++ {
		maxIdx := i
		for j := i + 1; j < len(pairs); j++ {
			if pairs[j].v > pairs[maxIdx].v {
				maxIdx = j
			}
		}
		pairs[i], pairs[maxIdx] = pairs[maxIdx], pairs[i]
		if i >= 2 {
			break
		}
	}
	for i := 0; i < len(pairs) && i < 3; i++ {
		sample = append(sample, pairs[i].k)
	}

	primarySet := make(map[string]bool, len(primaries))
	for _, p := range primaries {
		primarySet[strings.ToLower(p)] = true
	}
	for d := range counts {
		if primarySet[strings.ToLower(d)] {
			primaryMatches++
		}
	}
	return
}

// loadPrimaryDomains returns the configured primary domains, or nil on error.
func loadPrimaryDomains() []string {
	s, err := store.Get()
	if err != nil {
		return nil
	}
	return s.GetPrimaryDomains()
}

func printRequests(requests []store.Request, mode store.OutputMode, totalCount int, limit, offset int) {
	for _, req := range requests {
		printRequest(&req, mode)
		fmt.Println()
	}
	if footer := output.FormatPaginationFooter(len(requests), totalCount, offset, limit); footer != "" {
		fmt.Println(footer)
	}
	fmt.Println("Use 'rep body <id>' to get full response body for a specific request")
}

func printRequestsLine(requests []store.Request, totalCount int, limit, offset int) {
	budget := output.ParseBudgetMode(listBudget)
	for i := range requests {
		fmt.Println(output.FormatRequestLineBudget(&requests[i], budget))
	}
	if footer := output.FormatPaginationFooter(len(requests), totalCount, offset, limit); footer != "" {
		fmt.Println(footer)
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

	// Request headers (agent-safe and bounded in meta mode).
	if len(req.Headers) > 0 {
		fmt.Println("  Request Headers:")
		importantHeaders := []string{"content-type", "authorization", "cookie", "x-api-key", "accept"}
		headers := req.Headers
		if mode == store.OutputMeta {
			headers = output.MetaHeaders(req.Headers, false)
			importantHeaders = sortedHeaderNames(headers)
		}
		for _, h := range importantHeaders {
			key, values := store.HeaderValuesWithKey(headers, h)
			if len(values) == 0 {
				continue
			}
			if key == "" {
				key = h
			}
			for _, v := range values {
				v = output.SanitizeText(v)
				// Compact/full text keeps a short hint for interactive use. Meta
				// values are already irreversibly fingerprinted by MetaHeaders.
				if mode != store.OutputMeta && (h == "authorization" || h == "cookie" || h == "x-api-key") {
					if len(v) > 20 {
						v = v[:10] + "..." + v[len(v)-5:]
					}
				}
				fmt.Printf("    %s: %s\n", key, v)
			}
		}
	}

	// Request body (hidden in meta mode; contract: headers only)
	if req.Body != "" && mode != store.OutputMeta {
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
	if req.Response != nil {
		fmt.Println("  Response Headers:")
		responseHeaders := req.Response.Headers
		responseHeaderNames := []string{"content-type", "content-length"}
		if mode == store.OutputMeta {
			responseHeaders = output.MetaHeaders(req.Response.Headers, true)
			responseHeaderNames = sortedHeaderNames(responseHeaders)
		}
		for _, h := range responseHeaderNames {
			key, values := store.HeaderValuesWithKey(responseHeaders, h)
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

		if req.Response.Body != "" && mode != store.OutputMeta {
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
	listCmd.Flags().StringVar(&listBudget, "budget", "compact", "Token budget: minimal (~15/req), compact (~30/req), standard (~100/req), full")
	listCmd.Flags().StringVar(&listSince, "since", "", "Only requests since duration (e.g., 5m, 1h, 30s)")
	// New agent-optimized flags
	listCmd.Flags().StringVar(&listType, "type", "", "Filter by resource type (script,xmlhttprequest,fetch,document)")
	listCmd.Flags().BoolVar(&listAPI, "api", false, "Preset: API calls only (xmlhttprequest, fetch)")
	listCmd.Flags().BoolVar(&listInteresting, "interesting", false, "Preset: Error responses (4xx/5xx) + state-changing methods")
	listCmd.Flags().BoolVar(&listErrors, "errors", false, "Preset: Only error responses (4xx/5xx)")
	listCmd.Flags().BoolVar(&listMutations, "mutations", false, "Preset: Only state-changing methods (POST/PUT/DELETE/PATCH)")
	// Data source
	listCmd.Flags().StringVar(&listSaved, "saved", "", "Read from saved session (ID, prefix, or 'latest')")
}
