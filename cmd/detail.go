package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var detailCmd = &cobra.Command{
	Use:   "detail <request-id>",
	Short: "Show full request details",
	Long: `Display complete request and response details.

Level 3 progressive disclosure - full request/response view.
Use when you need complete information about a specific request.

Accepts any ID format:
  - Original ID: h_6f2d0c31107d4f87
  - Semantic ID: 6f2d0c_POST_201
  - Short hash: 6f2d0c

Examples:
  rep detail 6f2d0c_POST_201
  rep detail h_6f2d0c
  rep detail 6f2d0c`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		requestID := args[0]

		req := findRequestByID(requestID)
		if req == nil {
			return fmt.Errorf("request not found: %s", requestID)
		}

		store.ComputeRequestFields(req)

		mode := store.OutputFull
		if outputMode == "meta" {
			mode = store.OutputMeta
		} else if outputMode == "compact" && jsonOutput {
			mode = store.OutputCompact
		}

		if jsonOutput || outputMode == "json" {
			formatted := output.FormatRequest(req, mode)
			out, _ := sonic.MarshalIndent(formatted, "", "  ")
			fmt.Println(string(out))
		} else {
			printRequestDetail(req, mode)
		}

		return nil
	},
}

func printRequestDetail(req *store.Request, mode store.OutputMode) {
	// Header
	fmt.Printf("ID: %s\n", req.SemanticID)
	fmt.Println(strings.Repeat("─", 60))

	// Request line
	fmt.Printf("%s %s HTTP/1.1\n", req.Method, req.URL)
	fmt.Printf("Host: %s\n", req.Domain)

	requestHeaders := req.Headers
	if mode == store.OutputMeta {
		requestHeaders = output.MetaHeaders(req.Headers, false)
	}
	for _, name := range sortedHeaderNames(requestHeaders) {
		values := requestHeaders[name]
		for _, v := range values {
			// Truncate very long header values
			if len(v) > 100 {
				v = v[:50] + "..." + v[len(v)-20:]
			}
			fmt.Printf("%s: %s\n", name, v)
		}
	}

	// Request body
	if req.Body != "" && mode != store.OutputMeta {
		fmt.Println()
		fmt.Println(req.Body)
	}

	// Response
	fmt.Println()
	fmt.Println(strings.Repeat("─", 60))

	if req.Response == nil {
		pterm.Warning.Println("No response captured")
		return
	}

	// Status line
	statusText := getStatusText(req.Response.Status)
	fmt.Printf("HTTP/1.1 %d %s\n", req.Response.Status, statusText)

	responseHeaders := req.Response.Headers
	if mode == store.OutputMeta {
		responseHeaders = output.MetaHeaders(req.Response.Headers, true)
	}
	for _, name := range sortedHeaderNames(responseHeaders) {
		values := responseHeaders[name]
		for _, v := range values {
			if len(v) > 100 {
				v = v[:50] + "..." + v[len(v)-20:]
			}
			fmt.Printf("%s: %s\n", name, v)
		}
	}
	if mode == store.OutputMeta {
		return
	}

	// Response body
	if req.Response.Body != "" {
		fmt.Println()
		fmt.Println(req.Response.Body)
	} else {
		fmt.Println()
		pterm.Info.Println("(empty body)")
	}
}

func sortedHeaderNames(headers store.HeaderMap) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	return names
}

func getStatusText(code int) string {
	statusTexts := map[int]string{
		200: "OK",
		201: "Created",
		204: "No Content",
		301: "Moved Permanently",
		302: "Found",
		304: "Not Modified",
		400: "Bad Request",
		401: "Unauthorized",
		403: "Forbidden",
		404: "Not Found",
		405: "Method Not Allowed",
		409: "Conflict",
		422: "Unprocessable Entity",
		429: "Too Many Requests",
		500: "Internal Server Error",
		502: "Bad Gateway",
		503: "Service Unavailable",
	}
	if text, ok := statusTexts[code]; ok {
		return text
	}
	return ""
}

func init() {
	rootCmd.AddCommand(detailCmd)
}
