package cmd

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	groupBy       string
	groupShow     string
	groupPattern  string
	groupReplace  string
	groupLimit    int
	groupMinCount int
)

var groupCmd = &cobra.Command{
	Use:   "group",
	Short: "Group requests by field or pattern",
	Long: `Group requests dynamically by any field or pattern.

AI agent primitive for discovering patterns in traffic.
AI defines the grouping criteria - no hardcoded normalization.

Group by field:
  --by method               Group by HTTP method
  --by status               Group by response status
  --by domain               Group by domain
  --by path                 Group by exact path
  --by header:<name>        Group by header value

Group by pattern (AI-defined normalization):
  --by path --pattern '\d+' --replace ':id'
  This normalizes /users/123 and /users/456 into /users/:id

Show options (--show):
  count                     Request count per group
  statuses                  Status code breakdown
  methods                   Method breakdown
  ids                       Request IDs in group

Examples:
  rep group --by method
  rep group --by path --pattern '\d+' --replace ':id'
  rep group --by header:authorization --show count
  rep group --by status --show methods,ids
  rep group --by domain --min-count 5`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if groupBy == "" {
			return fmt.Errorf("--by is required")
		}

		// Load requests
		requests, err := loadRequestsForExtract()
		if err != nil {
			return err
		}

		if len(requests) == 0 {
			pterm.Info.Println("No requests to group")
			return nil
		}

		// Compile pattern if provided
		var re *regexp.Regexp
		if groupPattern != "" {
			re, err = regexp.Compile(groupPattern)
			if err != nil {
				return fmt.Errorf("invalid pattern: %w", err)
			}
		}

		// Group requests
		groups := groupRequests(requests, groupBy, re, groupReplace)

		// Filter by min count
		if groupMinCount > 0 {
			filtered := make([]RequestGroup, 0)
			for _, g := range groups {
				if g.Count >= groupMinCount {
					filtered = append(filtered, g)
				}
			}
			groups = filtered
		}

		// Sort by count descending
		sort.Slice(groups, func(i, j int) bool {
			return groups[i].Count > groups[j].Count
		})

		// Apply limit
		if groupLimit > 0 && groupLimit < len(groups) {
			groups = groups[:groupLimit]
		}

		// Parse show options
		showOpts := parseShowOptions(groupShow)

		// Output
		if getOutputMode() == "json" {
			out, _ := sonic.MarshalIndent(groups, "", "  ")
			fmt.Println(string(out))
		} else {
			printGroups(groups, showOpts)
		}

		return nil
	},
}

// RequestGroup represents a group of requests
type RequestGroup struct {
	Key      string         `json:"key"`
	Count    int            `json:"count"`
	Requests []string       `json:"request_ids,omitempty"`
	Statuses map[int]int    `json:"statuses,omitempty"`
	Methods  map[string]int `json:"methods,omitempty"`
}

type showOptions struct {
	count    bool
	statuses bool
	methods  bool
	ids      bool
}

func parseShowOptions(show string) showOptions {
	opts := showOptions{count: true} // Always show count
	if show == "" {
		return opts
	}
	parts := strings.Split(show, ",")
	for _, p := range parts {
		switch strings.TrimSpace(p) {
		case "statuses":
			opts.statuses = true
		case "methods":
			opts.methods = true
		case "ids":
			opts.ids = true
		}
	}
	return opts
}

func getGroupKey(req *store.Request, by string, re *regexp.Regexp, replace string) string {
	store.ComputeRequestFields(req)

	var val string
	switch {
	case by == "method":
		val = req.Method
	case by == "status":
		if req.Response != nil {
			val = fmt.Sprintf("%d", req.Response.Status)
		} else {
			val = "0"
		}
	case by == "domain":
		val = req.Domain
	case by == "path":
		val = req.Path
	case strings.HasPrefix(by, "header:"):
		headerName := strings.TrimPrefix(by, "header:")
		val = store.HeaderFirst(req.Headers, headerName)
		// Mask sensitive headers
		if strings.Contains(strings.ToLower(headerName), "auth") ||
			strings.Contains(strings.ToLower(headerName), "cookie") ||
			strings.Contains(strings.ToLower(headerName), "key") {
			if len(val) > 12 {
				val = val[:6] + "***" + val[len(val)-4:]
			}
		}
	default:
		val = req.Path
	}

	// Apply pattern normalization if provided
	if re != nil && replace != "" {
		val = re.ReplaceAllString(val, replace)
	}

	return val
}

func groupRequests(requests []store.Request, by string, re *regexp.Regexp, replace string) []RequestGroup {
	groupMap := make(map[string]*RequestGroup)

	for i := range requests {
		req := &requests[i]
		store.ComputeRequestFields(req)

		key := getGroupKey(req, by, re, replace)
		if key == "" {
			key = "(empty)"
		}

		if groupMap[key] == nil {
			groupMap[key] = &RequestGroup{
				Key:      key,
				Statuses: make(map[int]int),
				Methods:  make(map[string]int),
			}
		}

		g := groupMap[key]
		g.Count++
		g.Requests = append(g.Requests, string(req.SemanticID))

		if req.Response != nil {
			g.Statuses[req.Response.Status]++
		}
		g.Methods[req.Method]++
	}

	// Convert to slice
	groups := make([]RequestGroup, 0, len(groupMap))
	for _, g := range groupMap {
		groups = append(groups, *g)
	}

	return groups
}

func printGroups(groups []RequestGroup, opts showOptions) {
	if len(groups) == 0 {
		pterm.Info.Println("No groups found")
		return
	}

	for _, g := range groups {
		// Build the line
		parts := []string{g.Key}

		parts = append(parts, fmt.Sprintf("(%d)", g.Count))

		if opts.statuses && len(g.Statuses) > 0 {
			var statusStrs []string
			for s, c := range g.Statuses {
				statusStrs = append(statusStrs, fmt.Sprintf("%d:%d", s, c))
			}
			sort.Strings(statusStrs)
			parts = append(parts, "["+strings.Join(statusStrs, " ")+"]")
		}

		if opts.methods && len(g.Methods) > 0 {
			var methodStrs []string
			for m, c := range g.Methods {
				methodStrs = append(methodStrs, fmt.Sprintf("%s:%d", m, c))
			}
			sort.Strings(methodStrs)
			parts = append(parts, "{"+strings.Join(methodStrs, " ")+"}")
		}

		fmt.Println(strings.Join(parts, " "))

		if opts.ids && len(g.Requests) > 0 {
			// Show first few IDs
			shown := g.Requests
			if len(shown) > 5 {
				shown = shown[:5]
			}
			for _, id := range shown {
				fmt.Printf("  %s\n", id)
			}
			if len(g.Requests) > 5 {
				fmt.Printf("  ... and %d more\n", len(g.Requests)-5)
			}
		}
	}

	pterm.Info.Printf("Found %d groups\n", len(groups))
}

func init() {
	rootCmd.AddCommand(groupCmd)
	groupCmd.Flags().StringVar(&groupBy, "by", "", "Field to group by (method, status, domain, path, header:<name>)")
	groupCmd.Flags().StringVar(&groupShow, "show", "", "What to show per group (count,statuses,methods,ids)")
	groupCmd.Flags().StringVar(&groupPattern, "pattern", "", "Regex pattern for normalization")
	groupCmd.Flags().StringVar(&groupReplace, "replace", "", "Replacement string for pattern")
	groupCmd.Flags().IntVar(&groupLimit, "limit", 0, "Limit number of groups shown")
	groupCmd.Flags().IntVar(&groupMinCount, "min-count", 0, "Only show groups with at least N requests")
}
