package cmd

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/cli"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	searchIn          string
	searchIgnoreCase  bool
	searchInvert      bool
	searchContext     int
	searchShowMatches bool
	searchFilter      store.FilterOptions
	searchTypeRaw     string
)

var searchCmd = &cobra.Command{
	Use:   "search <pattern>",
	Short: "Search across request/response data",
	Long: `Search for patterns across all captured traffic.

AI agent primitive for finding specific content in requests/responses.
Returns matching requests with context.

Search targets (--in):
  body              Request bodies only
  response          Response bodies only
  headers           All headers
  path              URL paths
  all               Everything (default)

Examples:
  rep search 'password'
  rep search 'user_id|userId' --in body
  rep search 'x-debug' --in headers -i
  rep search 'authorization' --in headers         # matches header key too
  rep search 'admin' --in path
  rep search 'error' --in response --context 100
  rep search 'Bearer' --in headers --show-matches
  rep search 'api' --in path -d api.example.com   # narrow via filter flags
  rep search 'token' --primary                    # only primary-domain traffic`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pattern := args[0]

		if searchIgnoreCase {
			pattern = "(?i)" + pattern
		}

		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("invalid regex pattern: %w", err)
		}

		// Wire --type into FilterOptions.ResourceTypes now that Cobra parsed it.
		searchFilter.ResourceTypes = cli.ParseCommaSeparated(searchTypeRaw)

		// Load candidates via the same path other commands use.
		allRequests, err := loadRequestsForExtract()
		if err != nil {
			return err
		}
		if len(allRequests) == 0 {
			pterm.Info.Println("No requests to search")
			return nil
		}

		// Apply structured filter (primary / domain / method / status / type).
		tempStore := store.NewTempStore(allRequests)
		if s, err := store.Get(); err == nil {
			tempStore.PrimaryDomains = s.PrimaryDomains
			tempStore.IgnoredDomains = s.IgnoredDomains
			tempStore.MutedPaths = s.MutedPaths
		}
		requests := tempStore.Filter(searchFilter)

		if len(requests) == 0 {
			pterm.Info.Println("No requests match the filter (search skipped)")
			return nil
		}

		// Search
		var results []SearchResult
		for i := range requests {
			req := &requests[i]
			if searchInvert {
				if !matchesRequest(req, re, searchIn) {
					results = append(results, SearchResult{
						Request:    req,
						SemanticID: string(req.SemanticID),
					})
				}
			} else {
				matches := searchInRequest(req, re, searchIn, searchContext)
				if len(matches) > 0 {
					results = append(results, SearchResult{
						Request:    req,
						SemanticID: string(req.SemanticID),
						Matches:    matches,
					})
				}
			}
			if searchFilter.Limit > 0 && len(results) >= searchFilter.Limit {
				break
			}
		}

		// Output
		if getOutputMode() == "json" {
			out, _ := sonic.MarshalIndent(results, "", "  ")
			fmt.Println(string(out))
		} else {
			printSearchResults(results)
		}

		return nil
	},
}

// SearchResult represents a request that matched the search
type SearchResult struct {
	Request    *store.Request `json:"-"`
	SemanticID string         `json:"sid"`
	Matches    []SearchMatch  `json:"matches,omitempty"`
}

// SearchMatch represents a single match within a request
type SearchMatch struct {
	Field   string `json:"field"`
	Value   string `json:"value"`
	Context string `json:"context,omitempty"`
	Line    int    `json:"line,omitempty"`
}

func matchesRequest(req *store.Request, re *regexp.Regexp, in string) bool {
	store.ComputeRequestFields(req)

	headerMatches := func(h store.HeaderMap) bool {
		for name, values := range h {
			// Match on key name (e.g. `authorization`, `x-csrf-token`).
			if re.MatchString(name) {
				return true
			}
			for _, v := range values {
				if re.MatchString(v) {
					return true
				}
			}
		}
		return false
	}

	switch in {
	case "body":
		return re.MatchString(req.Body)
	case "response":
		return req.Response != nil && re.MatchString(req.Response.Body)
	case "headers":
		if headerMatches(req.Headers) {
			return true
		}
		if req.Response != nil && headerMatches(req.Response.Headers) {
			return true
		}
		return false
	case "path":
		return re.MatchString(req.Path)
	default: // "all"
		if re.MatchString(req.URL) || re.MatchString(req.Body) {
			return true
		}
		if req.Response != nil && re.MatchString(req.Response.Body) {
			return true
		}
		if headerMatches(req.Headers) {
			return true
		}
		return false
	}
}

func searchInRequest(req *store.Request, re *regexp.Regexp, in string, contextLen int) []SearchMatch {
	var matches []SearchMatch
	store.ComputeRequestFields(req)

	addMatches := func(field, content string) {
		locs := re.FindAllStringIndex(content, -1)
		for _, loc := range locs {
			match := SearchMatch{
				Field: field,
				Value: content[loc[0]:loc[1]],
			}

			if contextLen > 0 {
				start := loc[0] - contextLen
				if start < 0 {
					start = 0
				}
				end := loc[1] + contextLen
				if end > len(content) {
					end = len(content)
				}
				match.Context = content[start:end]
				match.Line = strings.Count(content[:loc[0]], "\n") + 1
			}

			matches = append(matches, match)
		}
	}

	// Match on header key names themselves (common agent query:
	// "find requests with header X"). Looking only at values misses
	// the very thing the agent is usually searching for.
	addHeaderKeys := func(prefix string, h store.HeaderMap) {
		for name := range h {
			addMatches(prefix+":key", name)
		}
	}

	switch in {
	case "body":
		addMatches("body", req.Body)
	case "response":
		if req.Response != nil {
			addMatches("response", req.Response.Body)
		}
	case "headers":
		addHeaderKeys("header", req.Headers)
		for name, values := range req.Headers {
			for _, v := range values {
				addMatches("header:"+name, v)
			}
		}
		if req.Response != nil {
			addHeaderKeys("resp-header", req.Response.Headers)
			for name, values := range req.Response.Headers {
				for _, v := range values {
					addMatches("resp-header:"+name, v)
				}
			}
		}
	case "path":
		addMatches("path", req.Path)
	default: // "all"
		addMatches("url", req.URL)
		addMatches("body", req.Body)
		if req.Response != nil {
			addMatches("response", req.Response.Body)
		}
		addHeaderKeys("header", req.Headers)
		for name, values := range req.Headers {
			for _, v := range values {
				addMatches("header:"+name, v)
			}
		}
	}

	return matches
}

func printSearchResults(results []SearchResult) {
	if len(results) == 0 {
		pterm.Info.Println("No matches found")
		return
	}

	for _, r := range results {
		req := r.Request
		status := 0
		if req.Response != nil {
			status = req.Response.Status
		}

		if searchShowMatches && len(r.Matches) > 0 {
			fmt.Printf("[%s] %s %s → %d\n", r.SemanticID, req.Method, req.Path, status)
			for _, m := range r.Matches {
				if m.Context != "" {
					fmt.Printf("  %s:%d: %s\n", m.Field, m.Line, m.Context)
				} else {
					fmt.Printf("  %s: %s\n", m.Field, m.Value)
				}
			}
		} else {
			fmt.Printf("[%s] %s %s → %d (%d matches)\n", r.SemanticID, req.Method, req.Path, status, len(r.Matches))
		}
	}

	pterm.Info.Printf("Found %d matching requests\n", len(results))
}

func init() {
	rootCmd.AddCommand(searchCmd)
	searchCmd.Flags().StringVar(&searchIn, "in", "all", "Where to search (body, response, headers, path, all)")
	searchCmd.Flags().BoolVarP(&searchIgnoreCase, "ignore-case", "i", false, "Case insensitive search")
	searchCmd.Flags().BoolVarP(&searchInvert, "invert", "v", false, "Show requests that DON'T match")
	searchCmd.Flags().IntVarP(&searchContext, "context", "c", 0, "Show N characters of context")
	searchCmd.Flags().BoolVar(&searchShowMatches, "show-matches", false, "Show individual matches within requests")
	// Shared request-selection flags (domain, method, status, primary, type, limit, offset).
	cli.RegisterFilterFlags(searchCmd, &searchFilter, cli.FilterFlagConfig{
		SkipURLPattern: true, // search uses positional arg; avoid flag collision
		DefaultPrimary: false,
		TypeVar:        &searchTypeRaw,
	})
}
