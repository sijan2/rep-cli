package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	bodyRequest bool
	bodyHead    int
	bodySave    bool
)

var bodyCmd = &cobra.Command{
	Use:   "body <request-id>",
	Short: "Get response body for a request",
	Long: `Retrieve response body for analysis.

Flags for large responses:
  --head N    Show only first N bytes
  --save      Save to temp file, output path only

Examples:
  rep body 6f2d0c                    Full body
  rep body 6f2d0c --head 5000        First 5KB
  rep body 6f2d0c --save             Save to file
  rep body 6f2d0c --request          Request body instead`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		requestID := args[0]

		var req *store.Request

		// Try live.json first
		livePath, err := store.GetLiveFilePath()
		if err == nil {
			if export, err := loadLiveExport(livePath); err == nil {
				idx := store.BuildIndex(export.Requests)
				req = idx.GetByAny(requestID)
			}
		}

		// Try android.json
		if req == nil {
			if androidData, err := store.LoadAndroidData(); err == nil {
				for _, pkg := range androidData.Packages {
					for i := range pkg.Requests {
						ar := &pkg.Requests[i]
						store.ComputeRequestFields(&store.Request{ID: ar.ID, Method: ar.Method, URL: ar.URL})
						tempReq := store.Request{ID: ar.ID, Method: ar.Method, URL: ar.URL}
						store.ComputeRequestFields(&tempReq)

						if ar.ID == requestID || string(tempReq.SemanticID) == requestID {
							req = &store.Request{
								ID:      ar.ID,
								Method:  ar.Method,
								URL:     ar.URL,
								Headers: ar.Headers,
								Body:    ar.GetReqBody(),
								Response: &store.Response{
									Status: ar.Status,
									Body:   ar.GetResBody(),
								},
							}
							store.ComputeRequestFields(req)
							break
						}
					}
					if req != nil {
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
			if req == nil {
				req = findRequestByAnyID(s, requestID)
			}
		}

		if req == nil {
			ae := output.NewAgentError(
				output.ErrCodeRequestNotFound,
				"body",
				fmt.Sprintf("no request matched %q", requestID),
				"rep list --line | head  # browse available IDs",
				"rep summary  # confirm live.json has data",
			)
			return output.EmitAgentError(os.Stdout, ae, getOutputMode() == "json")
		}

		// Get body content
		var body string
		var contentType string
		if bodyRequest {
			body = req.Body
			contentType = store.HeaderFirst(req.Headers, "content-type")
		} else if req.Response != nil {
			body = req.Response.Body
			contentType = store.HeaderFirst(req.Response.Headers, "content-type")
		}

		// --save: Write to temp file, return path
		if bodySave {
			ext := ".txt"
			if strings.Contains(contentType, "json") {
				ext = ".json"
			} else if strings.Contains(contentType, "html") {
				ext = ".html"
			} else if strings.Contains(contentType, "javascript") {
				ext = ".js"
			}
			tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("rep_%s%s", req.ID, ext))
			if err := os.WriteFile(tmpFile, []byte(body), 0644); err != nil {
				return fmt.Errorf("failed to save: %w", err)
			}
			fmt.Println(tmpFile)
			return nil
		}

		// --head: Truncate
		if bodyHead > 0 && len(body) > bodyHead {
			body = body[:bodyHead]
			fmt.Println(body)
			fmt.Fprintf(os.Stderr, "\n[truncated, %d more bytes]\n", len(req.Response.Body)-bodyHead)
			return nil
		}

		// Overflow-to-disk: when a body exceeds the threshold and the caller
		// did NOT set --head or --save, write the full body to /tmp and
		// return a preview + path. Agents can read the file directly.
		var overflowInfo output.TruncationInfo
		if !bodySave && bodyHead == 0 && len(body) > output.OverflowThreshold {
			preview, info, err := output.SpillBodyToDisk(body, req.ID, contentType)
			if err == nil && info.Reason == "overflow-to-disk" {
				body = preview
				overflowInfo = info
			}
		}

		if getOutputMode() == "json" {
			out := map[string]interface{}{
				"id":     req.ID,
				"method": req.Method,
				"url":    req.URL,
			}
			if bodyRequest {
				out["body"] = body
			} else if req.Response != nil {
				out["status"] = req.Response.Status
				out["body"] = body
			}
			if overflowInfo.Reason != "" {
				out["truncation"] = overflowInfo
			}
			data, _ := sonic.MarshalIndent(out, "", "  ")
			fmt.Println(string(data))
		} else {
			if bodyRequest {
				printRequestBodyWith(req, body)
			} else {
				printResponseBodyWith(req, body)
			}
			// SpillBodyToDisk already appended a trailing "REP_SPILL=<path>"
			// line to the body preview; no additional wrapper here.
		}

		return nil
	},
}

// printRequestBodyWith prints the header/meta for a request body and then
// writes whatever the caller decided to show (possibly truncated / spilled).
// Size reflects the ORIGINAL body so the agent can tell how much was captured.
func printRequestBodyWith(req *store.Request, body string) {
	pterm.DefaultSection.Printf("Request Body: %s\n", req.ID)
	fmt.Printf("  %s %s\n\n", req.Method, req.URL)

	if req.Body == "" {
		pterm.Info.Println("No request body")
		return
	}

	contentType := store.HeaderFirst(req.Headers, "content-type")
	fmt.Printf("Content-Type: %s\n", contentType)
	fmt.Printf("Size: %d bytes\n\n", len(req.Body))
	fmt.Println(body)
}

func printResponseBodyWith(req *store.Request, body string) {
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

	contentType := store.HeaderFirst(req.Response.Headers, "content-type")
	fmt.Printf("Content-Type: %s\n", contentType)
	fmt.Printf("Size: %d bytes\n\n", len(req.Response.Body))
	fmt.Println(body)
}

func findRequestByAnyID(s *store.Store, requestID string) *store.Request {
	for _, session := range s.Sessions {
		idx := store.BuildIndex(session.Requests)
		if req := idx.GetByAny(requestID); req != nil {
			return req
		}
	}
	return nil
}

func init() {
	rootCmd.AddCommand(bodyCmd)
	bodyCmd.Flags().BoolVarP(&bodyRequest, "request", "r", false, "Get request body instead")
	bodyCmd.Flags().IntVar(&bodyHead, "head", 0, "Show only first N bytes")
	bodyCmd.Flags().BoolVar(&bodySave, "save", false, "Save to temp file, output path")
}
