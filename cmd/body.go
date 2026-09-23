package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	bodyRequest bool
	bodyHead    int
	bodySave    bool
	bodySaved   string
	bodyView    bodyViewFlags
)

var bodyCmd = &cobra.Command{
	Use:   "body <request-id>",
	Short: "Get response body for a request",
	Long: `Inspect captured response bytes with explicit completeness and bounded JSON.

Flags for large responses:
  --info      Check capture state, decoded byte count, and digest
  --head N    Read N bytes, optionally from --offset
  --save      Save decoded bytes privately; -j returns artifact metadata
  --saved ID  Read only one archive (full hash, unique ID/prefix, or latest)
  --pointer P Select a JSON value; --format sse/ndjson selects record pages

Examples:
  rep body 6f2d0c --info             Diagnose capture completeness
  rep body 6f2d0c --head 5000        First 5KB
  rep body 6f2d0c --save             Save to file
  rep body 6f2d0c --pointer /data    Select a JSON subtree
  rep body 6f2d0c --format sse       Read complete application events
  rep body 6f2d0c --request          Request body instead`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		requestID := args[0]

		req, err := findBodyWebRequest(requestID, bodySaved)
		if err != nil {
			return output.EmitAgentError(cmd.OutOrStdout(), output.NewAgentError(output.ErrCodeStoreRead, "body", err.Error(), "rep scope", "rep sessions"), getOutputMode() == "json")
		}

		// Legacy Android lookup is limited to a canonical ID and explicit global
		// access. It never overrides a selected archive or an isolated web task.
		selected, _ := scope.Current()
		if req == nil && bodySaved == "" && !selected.Scoped {
			if androidData, err := store.LoadAndroidData(); err == nil {
				for _, pkg := range androidData.Packages {
					for i := range pkg.Requests {
						ar := &pkg.Requests[i]
						if ar.ID == requestID {
							req = &store.Request{ID: ar.ID, Method: ar.Method, URL: ar.URL, Headers: ar.Headers, Body: ar.GetReqBody(), Response: &store.Response{Status: ar.Status, Body: ar.GetResBody()}}
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

		if err := renderCapturedBody(cmd, req); err != nil {
			return output.EmitAgentError(cmd.OutOrStdout(), output.NewAgentError("body_unavailable", "body", err.Error(), "rep body "+requestID+" --info", "rep describe body"), true)
		}
		return nil
	},
}

// saveBodyArtifact uses a unique file in the task namespace, so equal request
// IDs from independent agents (or repeated saves) cannot replace one another.
func saveBodyArtifact(requestID, body, extension string) (string, error) {
	switch extension {
	case ".txt", ".json", ".html", ".js", ".bin":
	default:
		return "", fmt.Errorf("unsupported body artifact extension")
	}
	selected, err := scope.Current()
	if err != nil {
		return "", err
	}
	if !selected.Scoped {
		if strings.ContainsAny(requestID, `/\`) {
			return "", fmt.Errorf("request ID cannot be used as an artifact filename")
		}
		path := filepath.Join(os.TempDir(), fmt.Sprintf("rep_%s%s", requestID, extension))
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			return "", err
		}
		return path, nil
	}
	dir := filepath.Join(selected.DataDir, "body-exports")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("body export directory must be a regular directory")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, "body-*"+extension)
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			os.Remove(file.Name())
		}
	}()
	if _, err := file.WriteString(body); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	complete = true
	return file.Name(), nil
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
	var requests []store.Request
	for _, session := range s.Sessions {
		requests = append(requests, session.Requests...)
	}
	return store.BuildIndex(requests).GetByAny(requestID)
}

// A canonical live ID explicitly identifies the current capture. Otherwise
// exact archived IDs win over live prefixes, and aliases/prefixes must be
// unique across all candidate requests. --saved pins one immutable source.
func findBodyWebRequest(requestID, saved string) (*store.Request, error) {
	if saved != "" {
		persistent, err := store.Load()
		if err != nil {
			return nil, fmt.Errorf("cannot read this task's archives")
		}
		var session *store.Session
		if saved == "latest" || saved == "last" {
			session = persistent.GetLatestSession()
		} else {
			session, err = selectSummaryArchive(persistent.Sessions, saved)
			if err != nil {
				return nil, err
			}
		}
		if session == nil {
			return nil, fmt.Errorf("saved session not found in this task")
		}
		return store.BuildIndex(session.Requests).GetByAny(requestID), nil
	}
	livePath, err := store.GetLiveFilePath()
	if err != nil {
		return nil, err
	}
	export, err := loadLiveExport(livePath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("this task's live capture is unreadable; use --saved to select an archive explicitly")
	}
	for index := len(export.Requests) - 1; index >= 0; index-- {
		if export.Requests[index].ID == requestID {
			store.ComputeRequestFields(&export.Requests[index])
			return &export.Requests[index], nil
		}
	}
	persistent, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("cannot read this task's archives")
	}
	var requests []store.Request
	for _, session := range persistent.Sessions {
		requests = append(requests, session.Requests...)
	}
	requests = append(requests, export.Requests...)
	return store.BuildIndex(requests).GetByAny(requestID), nil
}

func init() {
	rootCmd.AddCommand(bodyCmd)
	bodyCmd.Flags().BoolVarP(&bodyRequest, "request", "r", false, "Get request body instead")
	bodyCmd.Flags().IntVar(&bodyHead, "head", 0, "Show only first N bytes")
	bodyCmd.Flags().BoolVar(&bodySave, "save", false, "Save a private body artifact in this task, output path")
	bodyCmd.Flags().StringVar(&bodySaved, "saved", "", "Read only one archive (full hash, unique ID/prefix, or latest)")
	bodyCmd.Flags().BoolVar(&bodyView.Info, "info", false, "Show capture completeness and body metadata without content")
	bodyCmd.Flags().BoolVar(&bodyView.RequireComplete, "require-complete", false, "Fail unless the body was captured completely")
	bodyCmd.Flags().IntVar(&bodyView.Offset, "offset", 0, "Byte offset within the selected decoded body")
	bodyCmd.Flags().IntVar(&bodyView.MaxBytes, "max-bytes", 8192, "Maximum inline JSON bytes including newline")
	bodyCmd.Flags().StringVar(&bodyView.Format, "format", "raw", "Body projection: raw, auto, json, ndjson, sse")
	bodyCmd.Flags().StringVar(&bodyView.Pointer, "pointer", "", "RFC 6901 JSON pointer to select one value")
	bodyCmd.Flags().StringVar(&bodyView.Find, "find", "", "Find literal text with byte offsets and bounded context")
	bodyCmd.Flags().IntVar(&bodyView.RecordOffset, "record-offset", 0, "Starting NDJSON/SSE record or search-match index")
	bodyCmd.Flags().IntVar(&bodyView.Records, "records", 20, "Maximum NDJSON/SSE records or search matches")
}
