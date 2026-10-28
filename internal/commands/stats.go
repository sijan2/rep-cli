package commands

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var statsCmd = newStatsCommand()

func newStatsCommand() *cobra.Command {
	var countBy, unique, distribution string
	var limit int
	command := &cobra.Command{
		Use:   "stats <operation>",
		Short: "Aggregate statistics from traffic data",
		Long: `Compute aggregations from captured traffic.

Choose count, unique, or distribution, then give one field.
Fields: method, status, domain, path, path:segment:N, header:<name>, body-type.
Use rep describe stats for source and output contracts.`,
	}
	legacyMode := func() (string, string, error) {
		mode, field := "", ""
		for _, option := range []struct{ flag, mode, value string }{
			{"count-by", "count", countBy}, {"unique", "unique", unique}, {"distribution", "distribution", distribution},
		} {
			if !command.PersistentFlags().Changed(option.flag) {
				continue
			}
			if mode != "" {
				return "", "", fmt.Errorf("choose exactly one stats operation")
			}
			mode, field = option.mode, option.value
		}
		return mode, field, nil
	}
	validateOptions := func(cmd *cobra.Command) error {
		// Cobra's built-in flag deprecation notes go to stdout; keep legacy
		// spellings usable without contaminating an agent's JSON result.
		for _, option := range []struct{ flag, replacement string }{
			{"count-by", "rep stats count FIELD"}, {"unique", "rep stats unique FIELD"},
			{"distribution", "rep stats distribution FIELD"}, {"top", "--limit"},
		} {
			if command.PersistentFlags().Changed(option.flag) {
				legacy, _, _ := legacyMode()
				if option.flag == "top" && (cmd.Name() == "unique" || legacy == "unique") {
					fmt.Fprintln(cmd.ErrOrStderr(), "Flag --top is deprecated and has no effect for unique; omit it")
				} else {
					fmt.Fprintf(cmd.ErrOrStderr(), "Flag --%s is deprecated; use %s\n", option.flag, option.replacement)
				}
			}
		}
		if command.PersistentFlags().Changed("top") && cmd.Flags().Changed("limit") {
			return fmt.Errorf("do not combine --limit with legacy --top")
		}
		return nil
	}
	command.Args = func(cmd *cobra.Command, args []string) error {
		if err := validateOptions(cmd); err != nil {
			return err
		}
		if err := cobra.NoArgs(cmd, args); err != nil {
			return err
		}
		mode, field, err := legacyMode()
		if err != nil {
			return err
		}
		if mode == "" {
			return fmt.Errorf("choose stats count, unique, or distribution and one field")
		}
		return validateStatsField(field)
	}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		mode, field, _ := legacyMode()
		return runStats(cmd, mode, field, limit)
	}
	command.PersistentFlags().IntVar(&limit, "top", 0, "Legacy limit on results")
	_ = command.PersistentFlags().MarkHidden("top")
	for _, option := range []struct {
		flag  string
		value *string
	}{
		{"count-by", &countBy}, {"unique", &unique}, {"distribution", &distribution},
	} {
		command.PersistentFlags().StringVar(option.value, option.flag, "", "Legacy statistics operation")
		_ = command.PersistentFlags().MarkHidden(option.flag)
	}
	for _, mode := range []string{"count", "unique", "distribution"} {
		operation := mode
		child := &cobra.Command{
			Use:   operation + " <field>",
			Short: map[string]string{"count": "Count requests by field value", "unique": "Count distinct field values", "distribution": "Show field-value percentages"}[operation],
			Args: func(cmd *cobra.Command, args []string) error {
				if err := validateOptions(cmd); err != nil {
					return err
				}
				if err := cobra.ExactArgs(1)(cmd, args); err != nil {
					return err
				}
				legacy, _, err := legacyMode()
				if err != nil {
					return err
				}
				if legacy != "" {
					return fmt.Errorf("do not combine a stats operation with legacy --count-by, --unique, or --distribution")
				}
				return validateStatsField(args[0])
			},
			RunE: func(cmd *cobra.Command, args []string) error {
				return runStats(cmd, operation, args[0], limit)
			},
		}
		// Unique has never truncated values: advertise a limit only where it
		// is consumed, while the hidden --top adapter keeps old calls valid.
		if operation != "unique" {
			child.Flags().IntVar(&limit, "limit", 0, "Show only the first N results")
		}
		command.AddCommand(child)
	}
	return command
}

// Field names are data, not commands. Validate them before reading traffic so
// a misspelling cannot quietly turn every request into an empty aggregation.
func validateStatsField(field string) error {
	switch field {
	case "method", "status", "domain", "path", "body-type":
		return nil
	}
	if strings.HasPrefix(field, "header:") && strings.TrimSpace(strings.TrimPrefix(field, "header:")) != "" {
		return nil
	}
	if strings.HasPrefix(field, "path:segment:") {
		n, err := strconv.Atoi(strings.TrimPrefix(field, "path:segment:"))
		if err == nil && n >= 0 {
			return nil
		}
	}
	return fmt.Errorf("unsupported stats field %q; use method, status, domain, path, path:segment:N, header:<name>, or body-type", field)
}

func runStats(cmd *cobra.Command, mode, field string, top int) error {
	requests, err := loadRequestsForExtract()
	if err != nil {
		return err
	}
	if len(requests) == 0 {
		pterm.Info.Println("No requests to analyze")
		return nil
	}
	var result interface{}
	switch mode {
	case "count":
		result = computeCountBy(requests, field, top)
	case "unique":
		result = computeUnique(requests, field)
	case "distribution":
		result = computeDistribution(requests, field, top)
	}
	if getOutputMode() == "json" {
		out, _ := sonic.MarshalIndent(result, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
	} else {
		printStats(result)
	}
	return nil
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
}
