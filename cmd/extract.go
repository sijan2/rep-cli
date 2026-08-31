package cmd

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	extractPattern string
	extractFrom    string
	extractUnique  bool
	extractContext int
	extractLimit   int
)

var extractCmd = &cobra.Command{
	Use:   "extract",
	Short: "Extract patterns from request/response data",
	Long: `Extract patterns from any field using regex.

AI agent primitive for flexible data extraction without hardcoded patterns.
AI defines what to extract - tool just returns matches.

Fields (--from):
  path              URL path
  url               Full URL
  body              Request body
  response          Response body
  header:<name>     Specific request header
  resp-header:<name> Specific response header
  query             Query string parameters
  all               Search all fields

Examples:
  rep extract --pattern '\d+' --from path
  rep extract --pattern 'Bearer (.+)' --from header:authorization
  rep extract --pattern '"(\w+)":' --from body --unique
  rep extract --pattern 'password|secret|key' --from response --context 50
  rep extract --pattern '[a-f0-9-]{36}' --from body --unique`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if extractPattern == "" {
			return fmt.Errorf("--pattern is required")
		}

		re, err := regexp.Compile(extractPattern)
		if err != nil {
			return fmt.Errorf("invalid regex pattern: %w", err)
		}

		// Load requests
		requests, err := loadRequestsForExtract()
		if err != nil {
			return err
		}

		if len(requests) == 0 {
			pterm.Info.Println("No requests to extract from")
			return nil
		}

		// Extract matches
		var results []ExtractResult
		seen := make(map[string]bool)

		for i := range requests {
			req := &requests[i]
			matches := extractFromRequest(req, re, extractFrom, extractContext)

			for _, m := range matches {
				if extractUnique {
					key := m.Value
					if seen[key] {
						continue
					}
					seen[key] = true
				}
				results = append(results, m)
				if extractLimit > 0 && len(results) >= extractLimit {
					break
				}
			}
			if extractLimit > 0 && len(results) >= extractLimit {
				break
			}
		}

		// Output
		if getOutputMode() == "json" {
			out, _ := sonic.MarshalIndent(results, "", "  ")
			fmt.Println(string(out))
		} else {
			printExtractResults(results, extractUnique)
		}

		return nil
	},
}

// ExtractResult represents a single extraction match
type ExtractResult struct {
	RequestID  string   `json:"request_id"`
	SemanticID string   `json:"sid"`
	Field      string   `json:"field"`
	Value      string   `json:"value"`
	Groups     []string `json:"groups,omitempty"`
	Context    string   `json:"context,omitempty"`
	Line       int      `json:"line,omitempty"`
}

func loadRequestsForExtract() ([]store.Request, error) {
	// Try live.json first
	livePath, err := store.GetLiveFilePath()
	if err == nil {
		if export, err := loadLiveExport(livePath); err == nil && len(export.Requests) > 0 {
			return export.Requests, nil
		}
	}

	// Fall back to store
	s, err := store.Get()
	if err != nil {
		return nil, fmt.Errorf("failed to load store: %w", err)
	}

	var requests []store.Request
	for _, session := range s.Sessions {
		requests = append(requests, session.Requests...)
	}
	return requests, nil
}

func extractFromRequest(req *store.Request, re *regexp.Regexp, from string, contextLen int) []ExtractResult {
	var results []ExtractResult

	// Ensure SemanticID is computed
	if req.SemanticID == "" {
		store.ComputeRequestFields(req)
	}

	addMatch := func(field, content string) {
		matches := re.FindAllStringSubmatchIndex(content, -1)
		for _, match := range matches {
			if len(match) < 2 {
				continue
			}

			result := ExtractResult{
				RequestID:  req.ID,
				SemanticID: string(req.SemanticID),
				Field:      field,
				Value:      content[match[0]:match[1]],
			}

			// Capture groups
			if len(match) > 2 {
				for i := 2; i < len(match); i += 2 {
					if match[i] >= 0 && match[i+1] >= 0 {
						result.Groups = append(result.Groups, content[match[i]:match[i+1]])
					}
				}
			}

			// Context
			if contextLen > 0 {
				start := match[0] - contextLen
				if start < 0 {
					start = 0
				}
				end := match[1] + contextLen
				if end > len(content) {
					end = len(content)
				}
				result.Context = content[start:end]

				// Find line number
				result.Line = strings.Count(content[:match[0]], "\n") + 1
			}

			results = append(results, result)
		}
	}

	// Determine which fields to search
	switch {
	case from == "path":
		addMatch("path", req.Path)

	case from == "url":
		addMatch("url", req.URL)

	case from == "body":
		addMatch("body", req.Body)

	case from == "response":
		if req.Response != nil {
			addMatch("response", req.Response.Body)
		}

	case from == "query":
		if idx := strings.Index(req.URL, "?"); idx >= 0 {
			addMatch("query", req.URL[idx+1:])
		}

	case strings.HasPrefix(from, "header:"):
		headerName := strings.TrimPrefix(from, "header:")
		if val := store.HeaderFirst(req.Headers, headerName); val != "" {
			addMatch("header:"+headerName, val)
		}

	case strings.HasPrefix(from, "resp-header:"):
		headerName := strings.TrimPrefix(from, "resp-header:")
		if req.Response != nil {
			if val := store.HeaderFirst(req.Response.Headers, headerName); val != "" {
				addMatch("resp-header:"+headerName, val)
			}
		}

	case from == "all" || from == "":
		addMatch("url", req.URL)
		addMatch("path", req.Path)
		addMatch("body", req.Body)
		if req.Response != nil {
			addMatch("response", req.Response.Body)
		}
		// Headers
		for name, values := range req.Headers {
			for _, v := range values {
				addMatch("header:"+name, v)
			}
		}
		if req.Response != nil {
			for name, values := range req.Response.Headers {
				for _, v := range values {
					addMatch("resp-header:"+name, v)
				}
			}
		}
	}

	return results
}

func printExtractResults(results []ExtractResult, unique bool) {
	if len(results) == 0 {
		pterm.Info.Println("No matches found")
		return
	}

	if unique {
		// Print just unique values
		for _, r := range results {
			fmt.Println(r.Value)
		}
		pterm.Info.Printf("Found %d unique matches\n", len(results))
	} else {
		// Print with request context
		for _, r := range results {
			if r.Context != "" {
				fmt.Printf("[%s] %s:%d: %s\n", r.SemanticID, r.Field, r.Line, r.Context)
			} else if len(r.Groups) > 0 {
				fmt.Printf("[%s] %s: %s -> %v\n", r.SemanticID, r.Field, r.Value, r.Groups)
			} else {
				fmt.Printf("[%s] %s: %s\n", r.SemanticID, r.Field, r.Value)
			}
		}
		pterm.Info.Printf("Found %d matches\n", len(results))
	}
}

func init() {
	rootCmd.AddCommand(extractCmd)
	extractCmd.Flags().StringVarP(&extractPattern, "pattern", "p", "", "Regex pattern to extract (required)")
	extractCmd.Flags().StringVarP(&extractFrom, "from", "f", "all", "Field to extract from (path, url, body, response, header:<name>, all)")
	extractCmd.Flags().BoolVarP(&extractUnique, "unique", "u", false, "Show only unique values")
	extractCmd.Flags().IntVarP(&extractContext, "context", "c", 0, "Show N characters of context around match")
	extractCmd.Flags().IntVarP(&extractLimit, "limit", "l", 0, "Limit number of results")
}
