package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var contextCmd = &cobra.Command{
	Use:   "context",
	Short: "Generate AI-optimized context summary",
	Long: `Generate a compact context summary optimized for AI agents.

Outputs a structured summary of traffic for efficient AI analysis.`,
	Run: func(cmd *cobra.Command, args []string) {
		runContext()
	},
}

func runContext() {
	s, err := store.Get()
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	// Collect stats
	var allRequests []store.Request
	for _, session := range s.Sessions {
		allRequests = append(allRequests, session.Requests...)
	}

	if len(allRequests) == 0 {
		if jsonOutput {
			fmt.Println(`{"requests": 0, "message": "No traffic captured"}`)
		} else {
			fmt.Println("No traffic captured yet")
		}
		return
	}

	// Domain counts
	domainCounts := make(map[string]int)
	methodCounts := make(map[string]int)
	statusBuckets := make(map[string]int)
	endpoints := make(map[string][]string) // endpoint -> request IDs

	for i := range allRequests {
		store.ComputeRequestFields(&allRequests[i])
		req := &allRequests[i]
		domainCounts[req.Domain]++
		methodCounts[req.Method]++

		status := 0
		if req.Response != nil {
			status = req.Response.Status
		}
		bucket := fmt.Sprintf("%dxx", status/100)
		statusBuckets[bucket]++

		ep := fmt.Sprintf("%s %s", req.Method, req.Path)
		endpoints[ep] = append(endpoints[ep], req.ID)
	}

	// Sort domains by count
	type domainCount struct {
		domain string
		count  int
	}
	var sortedDomains []domainCount
	for d, c := range domainCounts {
		sortedDomains = append(sortedDomains, domainCount{d, c})
	}
	sort.Slice(sortedDomains, func(i, j int) bool {
		return sortedDomains[i].count > sortedDomains[j].count
	})

	// Load notes
	ns, _ := store.LoadNotes()

	if jsonOutput {
		out := map[string]interface{}{
			"total_requests": len(allRequests),
			"total_domains":  len(domainCounts),
			"unique_paths":   len(endpoints),
			"domains":        sortedDomains,
			"methods":        methodCounts,
			"status_buckets": statusBuckets,
			"notes_count":    len(ns.Notes),
		}
		data, _ := sonic.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
		return
	}

	// Human-readable markdown output
	fmt.Println("# AI-Optimized Traffic Context")
	fmt.Println()
	fmt.Println("## Overview")
	fmt.Printf("- %d requests across %d domains\n", len(allRequests), len(domainCounts))
	fmt.Printf("- %d unique endpoints\n", len(endpoints))
	fmt.Println()

	fmt.Println("## Primary Domains")
	for i, dc := range sortedDomains {
		if i >= 5 {
			break
		}
		fmt.Printf("- %s (%d requests)\n", dc.domain, dc.count)
	}
	fmt.Println()

	fmt.Println("## Methods")
	for m, c := range methodCounts {
		fmt.Printf("- %s: %d\n", m, c)
	}
	fmt.Println()

	fmt.Println("## Status Distribution")
	for b, c := range statusBuckets {
		fmt.Printf("- %s: %d\n", b, c)
	}
	fmt.Println()

	if len(ns.Notes) > 0 {
		fmt.Println("## Notes")
		for _, n := range ns.Notes {
			fmt.Printf("- #%d (%s): %s\n", n.ID, n.HashID, n.Content)
			if len(n.Refs) > 0 {
				fmt.Printf("  refs: %s\n", strings.Join(n.Refs, ", "))
			}
		}
		fmt.Println()
	}

	fmt.Println("## Commands")
	fmt.Println("- `rep detail <id>` - Full request details")
	fmt.Println("- `rep diff <a> <b>` - Compare requests")
	fmt.Println("- `rep body <id>` - Raw response body")
	fmt.Println("- `rep search <pattern>` - Search content")
	fmt.Println("- `rep extract --pattern <regex>` - Extract patterns")
}

func init() {
	rootCmd.AddCommand(contextCmd)
}
