package cmd

import (
	"fmt"
	"strings"

	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	curlUseVars bool
	curlSaved   string
)

var curlCmd = &cobra.Command{
	Use:   "curl <request-id>",
	Short: "Generate curl command to replay request",
	Long: `Generate a curl command to replay a captured request.

Use --use-vars to replace auth tokens with shell variables,
saving tokens when the AI needs to modify and replay requests.

Examples:
  rep curl h_abc123                     Generate full curl command
  rep curl h_abc123 --use-vars          Use $BEARER_TOKEN, $SESSION_COOKIE vars

Token-saving workflow (shell vars):
  1. eval "$(rep auth --export)"        Set auth variables
  2. rep curl <id> --use-vars           Get curl with $VARIABLES
  3. Modify params and execute          AI only sees variable names

Without --use-vars (wastes tokens):
  curl -H 'Cookie: session=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...' ...

With --use-vars (saves tokens):
  curl -H 'Cookie: $SESSION_COOKIE' ...`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		requestID := args[0]

		var req *store.Request

		if curlSaved != "" {
			// Load from saved session
			s, err := store.Get()
			if err != nil {
				return fmt.Errorf("failed to load store: %w", err)
			}

			var session *store.Session
			if curlSaved == "latest" || curlSaved == "last" {
				session = s.GetLatestSession()
			} else {
				session = s.GetSession(curlSaved)
			}

			if session == nil {
				pterm.Warning.Printf("Session not found: %s\n", curlSaved)
				return nil
			}

			for i := range session.Requests {
				if session.Requests[i].ID == requestID {
					req = &session.Requests[i]
					break
				}
			}
		} else {
			// Try live.json first
			livePath, err := store.GetLiveFilePath()
			if err == nil {
				export, err := loadLiveExport(livePath)
				if err == nil {
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
				if err == nil {
					req = s.GetRequestFromSessions(requestID)
				}
			}
		}

		if req == nil {
			pterm.Warning.Printf("Request not found: %s\n", requestID)
			pterm.Info.Println("Use 'rep list' to see available request IDs")
			return nil
		}

		// Generate curl command
		curlCmd := generateCurl(req, curlUseVars)
		fmt.Println(curlCmd)

		if curlUseVars {
			fmt.Println()
			fmt.Println("# Run first: eval \"$(rep auth --export)\"")
		}

		return nil
	},
}

func generateCurl(req *store.Request, useVars bool) string {
	var parts []string

	parts = append(parts, "curl")

	// Method
	if req.Method != "GET" {
		parts = append(parts, "-X", req.Method)
	}

	// URL
	parts = append(parts, fmt.Sprintf("'%s'", req.URL))

	// Headers
	skipHeaders := map[string]bool{
		"host":              true,
		"content-length":    true,
		"connection":        true,
		"accept-encoding":   true,
		"sec-fetch-site":    true,
		"sec-fetch-mode":    true,
		"sec-fetch-dest":    true,
		"sec-ch-ua":         true,
		"sec-ch-ua-mobile":  true,
		"sec-ch-ua-platform": true,
	}

	for key, values := range req.Headers {
		if skipHeaders[strings.ToLower(key)] {
			continue
		}

		for _, value := range values {
			headerValue := value

			if useVars {
				// Replace auth values with variables
				headerValue = replaceWithVars(key, value)
			}

			parts = append(parts, "-H", fmt.Sprintf("'%s: %s'", key, escapeQuote(headerValue)))
		}
	}

	// Body
	if req.Body != "" {
		body := req.Body
		if useVars {
			// Could potentially replace tokens in body too
			body = req.Body
		}
		parts = append(parts, "-d", fmt.Sprintf("'%s'", escapeQuote(body)))
	}

	// Format with line continuations for readability
	if len(parts) > 4 {
		return formatCurlMultiline(parts)
	}

	return strings.Join(parts, " ")
}

func replaceWithVars(headerName, value string) string {
	lowerName := strings.ToLower(headerName)

	// Authorization header
	if lowerName == "authorization" {
		if strings.HasPrefix(strings.ToLower(value), "bearer ") {
			return "Bearer $BEARER_TOKEN"
		}
		if strings.HasPrefix(strings.ToLower(value), "basic ") {
			return "Basic $BASIC_AUTH"
		}
		return "$AUTH_TOKEN"
	}

	// Cookie header
	if lowerName == "cookie" {
		return "$SESSION_COOKIE"
	}

	// API keys
	if lowerName == "x-api-key" {
		return "$API_KEY"
	}
	if lowerName == "x-auth-token" {
		return "$AUTH_TOKEN"
	}
	if lowerName == "x-access-token" {
		return "$ACCESS_TOKEN"
	}
	if lowerName == "x-csrf-token" {
		return "$CSRF_TOKEN"
	}
	if lowerName == "x-xsrf-token" {
		return "$XSRF_TOKEN"
	}

	return value
}

func escapeQuote(s string) string {
	return strings.ReplaceAll(s, "'", "'\"'\"'")
}

func formatCurlMultiline(parts []string) string {
	var lines []string
	lines = append(lines, parts[0]) // curl

	for i := 1; i < len(parts); i++ {
		if parts[i] == "-X" || parts[i] == "-H" || parts[i] == "-d" {
			if i+1 < len(parts) {
				lines = append(lines, fmt.Sprintf("  %s %s", parts[i], parts[i+1]))
				i++
			}
		} else {
			lines = append(lines, fmt.Sprintf("  %s", parts[i]))
		}
	}

	// Add backslashes
	result := lines[0]
	for i := 1; i < len(lines); i++ {
		result += " \\\n" + lines[i]
	}

	return result
}

func init() {
	rootCmd.AddCommand(curlCmd)
	curlCmd.Flags().BoolVar(&curlUseVars, "use-vars", false, "Replace auth tokens with shell variables")
	curlCmd.Flags().StringVar(&curlSaved, "saved", "", "Read from saved session (ID or 'latest')")
}
