package cmd

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	compareFilter string
	compareByField string
)

var compareCmd = &cobra.Command{
	Use:   "compare",
	Short: "Compare multiple requests",
	Long: `Compare multiple requests matching a filter.

AI agent primitive for bulk request comparison.
Useful for analyzing patterns across similar requests.

Options:
  --filter <pattern>   Regex to filter requests by path/URL
  --by <field>         Group comparison by field (status, method, body-type)

Examples:
  rep compare --filter '/api/users/\d+'
  rep compare --filter '/api/users' --by status
  rep compare --filter 'POST.*login' --by body-type`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if compareFilter == "" {
			return fmt.Errorf("--filter is required")
		}

		// Load requests
		requests, err := loadRequestsForExtract()
		if err != nil {
			return err
		}

		if len(requests) == 0 {
			pterm.Info.Println("No requests to compare")
			return nil
		}

		// Filter requests
		opts := store.FilterOptions{
			Pattern: compareFilter,
		}
		tempStore := store.NewTempStore(requests)
		filtered := tempStore.Filter(opts)

		if len(filtered) == 0 {
			pterm.Info.Println("No requests match the filter")
			return nil
		}

		// Compare
		result := compareRequests(filtered, compareByField)

		if getOutputMode() == "json" {
			out, _ := sonic.MarshalIndent(result, "", "  ")
			fmt.Println(string(out))
		} else {
			printCompareResult(result)
		}

		return nil
	},
}

// CompareResult represents comparison of multiple requests
type CompareResult struct {
	TotalRequests int              `json:"total"`
	Filter        string           `json:"filter"`
	GroupBy       string           `json:"group_by,omitempty"`
	Groups        []CompareGroup   `json:"groups"`
	CommonFields  []string         `json:"common_fields,omitempty"`
	VaryingFields []string         `json:"varying_fields,omitempty"`
}

// CompareGroup represents a group of similar requests
type CompareGroup struct {
	Key       string   `json:"key"`
	Count     int      `json:"count"`
	Requests  []string `json:"request_ids"`
	Sample    string   `json:"sample_path"`
	AuthTypes []string `json:"auth_types,omitempty"`
	BodyTypes []string `json:"body_types,omitempty"`
}

func compareRequests(requests []store.Request, byField string) CompareResult {
	result := CompareResult{
		TotalRequests: len(requests),
		GroupBy:       byField,
	}

	if byField == "" {
		// Single group comparison
		group := analyzeGroup(requests, "all")
		result.Groups = []CompareGroup{group}
		result.CommonFields, result.VaryingFields = findCommonVarying(requests)
	} else {
		// Group by field
		groupMap := make(map[string][]store.Request)
		for i := range requests {
			key := getCompareGroupKey(&requests[i], byField)
			groupMap[key] = append(groupMap[key], requests[i])
		}

		for key, reqs := range groupMap {
			group := analyzeGroup(reqs, key)
			result.Groups = append(result.Groups, group)
		}
	}

	return result
}

func getCompareGroupKey(req *store.Request, by string) string {
	store.ComputeRequestFields(req)

	switch by {
	case "status":
		if req.Response != nil {
			return fmt.Sprintf("%d", req.Response.Status)
		}
		return "0"
	case "method":
		return req.Method
	case "body-type":
		ct := strings.ToLower(store.HeaderFirst(req.Headers, "content-type"))
		if strings.Contains(ct, "json") {
			return "json"
		} else if strings.Contains(ct, "form") {
			return "form"
		} else if strings.Contains(ct, "xml") {
			return "xml"
		}
		return "other"
	default:
		return "all"
	}
}

func analyzeGroup(requests []store.Request, key string) CompareGroup {
	group := CompareGroup{
		Key:   key,
		Count: len(requests),
	}

	authTypeSet := make(map[string]bool)
	bodyTypeSet := make(map[string]bool)

	for i := range requests {
		req := &requests[i]
		store.ComputeRequestFields(req)

		group.Requests = append(group.Requests, string(req.SemanticID))

		if group.Sample == "" {
			group.Sample = req.Path
		}

		// Detect auth
		if auth := store.HeaderFirst(req.Headers, "authorization"); auth != "" {
			if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
				authTypeSet["bearer"] = true
			} else if strings.HasPrefix(strings.ToLower(auth), "basic ") {
				authTypeSet["basic"] = true
			} else {
				authTypeSet["other"] = true
			}
		} else if store.HeaderFirst(req.Headers, "cookie") != "" {
			authTypeSet["cookie"] = true
		}

		// Detect body type
		ct := strings.ToLower(store.HeaderFirst(req.Headers, "content-type"))
		if strings.Contains(ct, "json") {
			bodyTypeSet["json"] = true
		} else if strings.Contains(ct, "form") {
			bodyTypeSet["form"] = true
		}
	}

	for t := range authTypeSet {
		group.AuthTypes = append(group.AuthTypes, t)
	}
	for t := range bodyTypeSet {
		group.BodyTypes = append(group.BodyTypes, t)
	}

	// Only keep first 10 request IDs for display
	if len(group.Requests) > 10 {
		group.Requests = group.Requests[:10]
	}

	return group
}

func findCommonVarying(requests []store.Request) (common []string, varying []string) {
	if len(requests) < 2 {
		return
	}

	// Check key fields
	fields := map[string]func(*store.Request) string{
		"method": func(r *store.Request) string { return r.Method },
		"domain": func(r *store.Request) string { return r.Domain },
		"status": func(r *store.Request) string {
			if r.Response != nil {
				return fmt.Sprintf("%d", r.Response.Status)
			}
			return "0"
		},
		"auth": func(r *store.Request) string {
			return store.HeaderFirst(r.Headers, "authorization")
		},
	}

	for name, getter := range fields {
		firstVal := getter(&requests[0])
		allSame := true
		for i := 1; i < len(requests); i++ {
			if getter(&requests[i]) != firstVal {
				allSame = false
				break
			}
		}
		if allSame {
			common = append(common, name)
		} else {
			varying = append(varying, name)
		}
	}

	return
}

func printCompareResult(result CompareResult) {
	fmt.Printf("Comparing %d requests matching: %s\n", result.TotalRequests, compareFilter)
	fmt.Println(strings.Repeat("─", 60))

	for _, g := range result.Groups {
		fmt.Printf("\n%s (%d requests)\n", g.Key, g.Count)
		fmt.Printf("  Sample: %s\n", g.Sample)
		if len(g.AuthTypes) > 0 {
			fmt.Printf("  Auth: %s\n", strings.Join(g.AuthTypes, ", "))
		}
		if len(g.BodyTypes) > 0 {
			fmt.Printf("  Body: %s\n", strings.Join(g.BodyTypes, ", "))
		}
		if len(g.Requests) > 0 {
			fmt.Printf("  IDs: %s", strings.Join(g.Requests[:min(5, len(g.Requests))], ", "))
			if len(g.Requests) > 5 {
				fmt.Printf(" ... +%d more", len(g.Requests)-5)
			}
			fmt.Println()
		}
	}

	if len(result.CommonFields) > 0 {
		fmt.Printf("\nCommon: %s\n", strings.Join(result.CommonFields, ", "))
	}
	if len(result.VaryingFields) > 0 {
		fmt.Printf("Varying: %s\n", strings.Join(result.VaryingFields, ", "))
	}
}

func init() {
	rootCmd.AddCommand(compareCmd)
	compareCmd.Flags().StringVar(&compareFilter, "filter", "", "Regex to filter requests")
	compareCmd.Flags().StringVar(&compareByField, "by", "", "Group comparison by field (status, method, body-type)")
}
