package cmd

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	bodyRequest bool
)

var bodyCmd = &cobra.Command{
	Use:   "body <request-id>",
	Short: "Get full response body for a specific request",
	Long: `Retrieve the complete response body for deep analysis.

Use this when you need to analyze the full content after
identifying interesting requests with 'rep list'.

Examples:
  rep body req_42              Get response body
  rep body req_42 --request    Get request body instead
  rep body req_42 -o json      Output as JSON`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		requestID := args[0]

		var req *store.Request

		// Try live.json first (current session)
		livePath, err := store.GetLiveFilePath()
		if err == nil {
			if export, err := loadLiveExport(livePath); err == nil {
				for i := range export.Requests {
					if export.Requests[i].ID == requestID {
						req = &export.Requests[i]
						break
					}
				}
			}
		}

		// Fall back to saved sessions
		if req == nil {
			s, err := store.Get()
			if err != nil {
				return fmt.Errorf("failed to load store: %w", err)
			}
			req = s.GetRequestFromSessions(requestID)
		}

		if req == nil {
			return fmt.Errorf("request not found: %s", requestID)
		}

		if getOutputMode() == "json" {
			output := map[string]interface{}{
				"id":     req.ID,
				"method": req.Method,
				"url":    req.URL,
			}

			if bodyRequest {
				output["body"] = req.Body
				output["type"] = "request"
			} else {
				if req.Response != nil {
					output["status"] = req.Response.Status
					output["body"] = req.Response.Body
					output["headers"] = req.Response.Headers
				}
				output["type"] = "response"
			}

			out, _ := sonic.MarshalIndent(output, "", "  ")
			fmt.Println(string(out))
		} else {
			if bodyRequest {
				printRequestBody(req)
			} else {
				printResponseBody(req)
			}
		}

		return nil
	},
}

func printRequestBody(req *store.Request) {
	pterm.DefaultSection.Printf("Request Body: %s\n", req.ID)
	fmt.Printf("  %s %s\n\n", req.Method, req.URL)

	if req.Body == "" {
		pterm.Info.Println("No request body")
		return
	}

	// Check content type
	contentType := store.HeaderFirst(req.Headers, "content-type")

	fmt.Printf("Content-Type: %s\n", contentType)
	fmt.Printf("Size: %d bytes\n\n", len(req.Body))
	fmt.Println(req.Body)
}

func printResponseBody(req *store.Request) {
	pterm.DefaultSection.Printf("Response Body: %s\n", req.ID)
	fmt.Printf("  %s %s\n", req.Method, req.URL)

	if req.Response == nil {
		pterm.Warning.Println("No response captured")
		return
	}

	fmt.Printf("  Status: %d\n\n", req.Response.Status)

	if req.Response.Body == "" {
		pterm.Info.Println("Empty response body")
		return
	}

	// Check content type
	contentType := store.HeaderFirst(req.Response.Headers, "content-type")

	fmt.Printf("Content-Type: %s\n", contentType)
	fmt.Printf("Size: %d bytes\n\n", len(req.Response.Body))
	fmt.Println(req.Response.Body)
}

func init() {
	rootCmd.AddCommand(bodyCmd)
	bodyCmd.Flags().BoolVarP(&bodyRequest, "request", "r", false, "Get request body instead of response")
}
