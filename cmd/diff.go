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
	diffOnly string
)

var diffCmd = &cobra.Command{
	Use:   "diff <id1> <id2>",
	Short: "Compare two requests",
	Long: `Show differences between two requests.

AI agent primitive for comparing request/response pairs.
Helps identify what changed between similar requests.

Options:
  --only <fields>   Compare only specific fields (comma-separated)
                    Fields: method,path,status,body,response,headers

Accepts any ID format (original, semantic, short hash).

Examples:
  rep diff h6f2d h9c5g
  rep diff 6f2d0c_POST_201 9c5g7h_POST_403
  rep diff h6f2d h9c5g --only path,status,body`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		id1, id2 := args[0], args[1]

		// Find both requests
		req1 := findRequestByID(id1)
		req2 := findRequestByID(id2)

		if req1 == nil {
			return fmt.Errorf("request not found: %s", id1)
		}
		if req2 == nil {
			return fmt.Errorf("request not found: %s", id2)
		}

		// Compute fields
		store.ComputeRequestFields(req1)
		store.ComputeRequestFields(req2)

		// Parse --only fields
		var onlyFields []string
		if diffOnly != "" {
			onlyFields = strings.Split(diffOnly, ",")
		}

		diff := computeDiff(req1, req2, onlyFields)

		if getOutputMode() == "json" {
			out, _ := sonic.MarshalIndent(diff, "", "  ")
			fmt.Println(string(out))
		} else {
			printDiff(diff)
		}

		return nil
	},
}

// DiffResult represents differences between two requests
type DiffResult struct {
	Request1 string      `json:"request1"`
	Request2 string      `json:"request2"`
	Diffs    []FieldDiff `json:"diffs"`
	Same     []string    `json:"same,omitempty"`
}

// FieldDiff represents a difference in a single field
type FieldDiff struct {
	Field  string `json:"field"`
	Value1 string `json:"value1"`
	Value2 string `json:"value2"`
}

func findRequestByID(id string) *store.Request {
	// Try live.json first
	livePath, err := store.GetLiveFilePath()
	if err == nil {
		if export, err := loadLiveExport(livePath); err == nil {
			idx := store.BuildIndex(export.Requests)
			if req := idx.GetByAny(id); req != nil {
				return req
			}
		}
	}

	// Try store
	s, err := store.Get()
	if err != nil {
		return nil
	}

	return findRequestByAnyID(s, id)
}

func computeDiff(req1, req2 *store.Request, onlyFields []string) DiffResult {
	result := DiffResult{
		Request1: string(req1.SemanticID),
		Request2: string(req2.SemanticID),
	}

	shouldCheck := func(field string) bool {
		if len(onlyFields) == 0 {
			return true
		}
		for _, f := range onlyFields {
			if strings.TrimSpace(f) == field {
				return true
			}
		}
		return false
	}

	addDiff := func(field, v1, v2 string) {
		if v1 != v2 {
			result.Diffs = append(result.Diffs, FieldDiff{
				Field:  field,
				Value1: v1,
				Value2: v2,
			})
		} else if v1 != "" {
			result.Same = append(result.Same, field)
		}
	}

	// Compare fields
	if shouldCheck("method") {
		addDiff("method", req1.Method, req2.Method)
	}

	if shouldCheck("path") {
		addDiff("path", req1.Path, req2.Path)
	}

	if shouldCheck("domain") {
		addDiff("domain", req1.Domain, req2.Domain)
	}

	if shouldCheck("status") {
		s1, s2 := "0", "0"
		if req1.Response != nil {
			s1 = fmt.Sprintf("%d", req1.Response.Status)
		}
		if req2.Response != nil {
			s2 = fmt.Sprintf("%d", req2.Response.Status)
		}
		addDiff("status", s1, s2)
	}

	if shouldCheck("body") {
		addDiff("body", summarizeBody(req1.Body), summarizeBody(req2.Body))
	}

	if shouldCheck("response") {
		r1, r2 := "", ""
		if req1.Response != nil {
			r1 = summarizeBody(req1.Response.Body)
		}
		if req2.Response != nil {
			r2 = summarizeBody(req2.Response.Body)
		}
		addDiff("response", r1, r2)
	}

	if shouldCheck("headers") {
		// Compare key headers
		for _, h := range []string{"authorization", "cookie", "content-type", "x-api-key"} {
			v1 := store.HeaderFirst(req1.Headers, h)
			v2 := store.HeaderFirst(req2.Headers, h)
			if v1 != "" || v2 != "" {
				addDiff("header:"+h, truncateValue(v1), truncateValue(v2))
			}
		}
	}

	return result
}

func summarizeBody(body string) string {
	if body == "" {
		return "(empty)"
	}
	if len(body) > 100 {
		return fmt.Sprintf("(%d bytes) %s...", len(body), body[:100])
	}
	return body
}

func truncateValue(v string) string {
	if len(v) > 50 {
		return v[:25] + "..." + v[len(v)-15:]
	}
	return v
}

func printDiff(diff DiffResult) {
	fmt.Printf("Comparing: %s vs %s\n", diff.Request1, diff.Request2)
	fmt.Println(strings.Repeat("─", 60))

	if len(diff.Diffs) == 0 {
		pterm.Info.Println("No differences found")
		return
	}

	for _, d := range diff.Diffs {
		pterm.FgYellow.Printf("%s:\n", d.Field)
		pterm.FgRed.Printf("  - %s\n", d.Value1)
		pterm.FgGreen.Printf("  + %s\n", d.Value2)
	}

	if len(diff.Same) > 0 {
		fmt.Printf("\nSame: %s\n", strings.Join(diff.Same, ", "))
	}
}

func init() {
	rootCmd.AddCommand(diffCmd)
	diffCmd.Flags().StringVar(&diffOnly, "only", "", "Compare only specific fields (comma-separated)")
}
