package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	statsCountBy      string
	statsUnique       string
	statsDistribution string
	statsTop          int
)

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Aggregate statistics from traffic data",
	Long: `Compute aggregations and statistics from captured traffic.

AI agent primitive for understanding traffic patterns.

Options:
  --count-by <field>      Count requests by field value
  --unique <field>        Count unique values for a field
  --distribution <field>  Show percentage distribution
  --top N                 Show only top N results

Fields:
  method                  HTTP method
  status                  Response status code
  domain                  Request domain
  path                    URL path
  path:segment:N          Nth path segment (0-indexed)
  header:<name>           Request header value
  body-type               Request body content type

Examples:
  rep stats --count-by method
  rep stats --count-by status
  rep stats --count-by domain --top 10
  rep stats --unique header:authorization
  rep stats --distribution status
  rep stats --count-by 'path:segment:1'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if statsCountBy == "" && statsUnique == "" && statsDistribution == "" {
			return fmt.Errorf("one of --count-by, --unique, or --distribution is required")
		}

		// Load requests
		requests, err := loadRequestsForExtract()
		if err != nil {
			return err
		}

		if len(requests) == 0 {
			pterm.Info.Println("No requests to analyze")
			return nil
		}

		var result interface{}

		if statsCountBy != "" {
			result = computeCountBy(requests, statsCountBy, statsTop)
		} else if statsUnique != "" {
			result = computeUnique(requests, statsUnique)
		} else if statsDistribution != "" {
			result = computeDistribution(requests, statsDistribution, statsTop)
		}

		if getOutputMode() == "json" {
			out, _ := sonic.MarshalIndent(result, "", "  ")
			fmt.Println(string(out))
		} else {
			printStats(result)
		}

		return nil
	},
}

// CountResult represents a count aggregation
type CountResult struct {
	Field  string       `json:"field"`
	Counts []CountEntry `json:"counts"`
	Total  int          `json:"total"`
}

type CountEntry struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// UniqueResult represents unique value count
type UniqueResult struct {
	Field       string   `json:"field"`
	UniqueCount int      `json:"unique_count"`
	Values      []string `json:"values,omitempty"`
}

// DistributionResult represents percentage distribution
type DistributionResult struct {
	Field   string              `json:"field"`
	Total   int                 `json:"total"`
	Buckets []DistributionEntry `json:"buckets"`
}

type DistributionEntry struct {
	Value   string  `json:"value"`
	Count   int     `json:"count"`
	Percent float64 `json:"percent"`
}

func getFieldValue(req *store.Request, field string) string {
	store.ComputeRequestFields(req)

	switch field {
	case "method":
		return req.Method
	case "status":
		if req.Response != nil {
			return fmt.Sprintf("%d", req.Response.Status)
		}
		return "0"
	case "domain":
		return req.Domain
	case "path":
		return req.Path
	case "body-type":
		ct := strings.ToLower(store.HeaderFirst(req.Headers, "content-type"))
		if strings.Contains(ct, "json") {
			return "json"
		} else if strings.Contains(ct, "form") {
			return "form"
		} else if strings.Contains(ct, "xml") {
			return "xml"
		} else if ct == "" {
			return "none"
		}
		return ct
	default:
		// path:segment:N
		if strings.HasPrefix(field, "path:segment:") {
			nStr := strings.TrimPrefix(field, "path:segment:")
			var n int
			fmt.Sscanf(nStr, "%d", &n)
			parts := strings.Split(strings.Trim(req.Path, "/"), "/")
			if n >= 0 && n < len(parts) {
				return parts[n]
			}
			return ""
		}
		// header:<name>
		if strings.HasPrefix(field, "header:") {
			headerName := strings.TrimPrefix(field, "header:")
			return store.HeaderFirst(req.Headers, headerName)
		}
	}
	return ""
}

func computeCountBy(requests []store.Request, field string, top int) CountResult {
	counts := make(map[string]int)

	for i := range requests {
		val := getFieldValue(&requests[i], field)
		if val != "" {
			counts[val]++
		}
	}

	// Convert to sorted slice
	entries := make([]CountEntry, 0, len(counts))
	for k, v := range counts {
		entries = append(entries, CountEntry{Value: k, Count: v})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Count > entries[j].Count
	})

	if top > 0 && top < len(entries) {
		entries = entries[:top]
	}

	return CountResult{
		Field:  field,
		Counts: entries,
		Total:  len(requests),
	}
}

func computeUnique(requests []store.Request, field string) UniqueResult {
	seen := make(map[string]bool)
	var values []string

	for i := range requests {
		val := getFieldValue(&requests[i], field)
		if val != "" && !seen[val] {
			seen[val] = true
			values = append(values, val)
		}
	}

	return UniqueResult{
		Field:       field,
		UniqueCount: len(values),
		Values:      values,
	}
}

func computeDistribution(requests []store.Request, field string, top int) DistributionResult {
	counts := make(map[string]int)
	total := 0

	for i := range requests {
		val := getFieldValue(&requests[i], field)
		// For status, bucket into ranges
		if field == "status" && len(val) > 0 {
			val = val[:1] + "xx"
		}
		if val != "" {
			counts[val]++
			total++
		}
	}

	// Convert to sorted slice
	entries := make([]DistributionEntry, 0, len(counts))
	for k, v := range counts {
		entries = append(entries, DistributionEntry{
			Value:   k,
			Count:   v,
			Percent: float64(v) / float64(total) * 100,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Count > entries[j].Count
	})

	if top > 0 && top < len(entries) {
		entries = entries[:top]
	}

	return DistributionResult{
		Field:   field,
		Total:   total,
		Buckets: entries,
	}
}

func printStats(result interface{}) {
	switch r := result.(type) {
	case CountResult:
		fmt.Printf("Count by %s (%d total requests):\n", r.Field, r.Total)
		for _, e := range r.Counts {
			fmt.Printf("  %s: %d\n", e.Value, e.Count)
		}

	case UniqueResult:
		fmt.Printf("Unique %s values: %d\n", r.Field, r.UniqueCount)
		if len(r.Values) <= 20 {
			for _, v := range r.Values {
				// Truncate long values
				if len(v) > 50 {
					v = v[:25] + "..." + v[len(v)-15:]
				}
				fmt.Printf("  %s\n", v)
			}
		}

	case DistributionResult:
		fmt.Printf("Distribution by %s (%d total):\n", r.Field, r.Total)
		for _, e := range r.Buckets {
			bar := strings.Repeat("█", int(e.Percent/5))
			fmt.Printf("  %s: %d (%.1f%%) %s\n", e.Value, e.Count, e.Percent, bar)
		}
	}
}

func init() {
	rootCmd.AddCommand(statsCmd)
	statsCmd.Flags().StringVar(&statsCountBy, "count-by", "", "Count requests by field value")
	statsCmd.Flags().StringVar(&statsUnique, "unique", "", "Count unique values for field")
	statsCmd.Flags().StringVar(&statsDistribution, "distribution", "", "Show percentage distribution")
	statsCmd.Flags().IntVar(&statsTop, "top", 0, "Show only top N results")
}
