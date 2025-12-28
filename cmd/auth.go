package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	authExport bool
	authSave   bool
	authEnv    bool
	authShell  bool
	authVars   bool
	authPrefix string
	authDomain string
	authSaved  string
)

// AuthToken represents an extracted authentication token
type AuthToken struct {
	Name   string `json:"name"`   // Variable name (e.g., BEARER_TOKEN)
	Value  string `json:"value"`  // The actual token value
	Source string `json:"source"` // Header it came from
	Domain string `json:"domain"` // Which domain
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Extract auth tokens for curl replay",
	Long: `Extract authentication tokens from captured requests.

Finds Bearer tokens, cookies, API keys, and other auth headers.
Use --save to write a shell env file (AI never sees tokens!).
Use --vars to export your own variables without printing tokens.

Token-saving workflow (recommended):
  rep auth --save -d api.target.com
  eval "$(rep auth --vars -d api.target.com --prefix KIRO)"
  # Now use $KIRO_AUTH, $KIRO_COOKIE, $KIRO_CSRF in curl

Shell helper:
  source "$(rep auth --env -d api.target.com)"

Legacy workflow (shell vars):
  rep auth --export                      Output as shell exports (prints tokens)
  eval "$(rep auth --export)"            Set in current shell

Examples:
  rep auth                               Show extracted auth tokens
  rep auth --save                        Save to env file
  rep auth --save -d api.target.com      Save auth for one domain
  rep auth --env -d api.target.com       Print env path only
  rep auth --shell -d api.target.com     Print "source <path>" for shell
  rep auth --vars -d api.target.com --prefix KIRO
  rep auth --export                      Output as shell exports

Extracted headers:
  - Authorization (Bearer, Basic, etc.)
  - Cookie
  - X-API-Key, X-Auth-Token, X-Access-Token
  - X-CSRF-Token, X-XSRF-Token`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if (authEnv || authShell || authVars) && !authSave {
			envPath, err := authEnvPath(authDomain)
			if err != nil {
				return fmt.Errorf("failed to resolve auth env path: %w", err)
			}
			if !fileExists(envPath) {
				return fmt.Errorf("auth env not found: %s (run 'rep auth --save' first)", envPath)
			}
			if authVars {
				return printAuthVars(envPath, authPrefix, authDomain)
			}
			return printAuthEnv(envPath, authShell)
		}

		var requests []store.Request

		if authSaved != "" {
			// Load from saved session
			s, err := store.Get()
			if err != nil {
				return fmt.Errorf("failed to load store: %w", err)
			}

			var session *store.Session
			if authSaved == "latest" || authSaved == "last" {
				session = s.GetLatestSession()
			} else {
				session = s.GetSession(authSaved)
			}

			if session == nil {
				pterm.Warning.Printf("Session not found: %s\n", authSaved)
				return nil
			}
			requests = session.Requests
		} else {
			// Load from live.json
			livePath, err := store.GetLiveFilePath()
			if err != nil {
				return fmt.Errorf("failed to get live path: %w", err)
			}
			export, err := loadLiveExport(livePath)
			if err != nil {
				pterm.Warning.Printf("Could not read live.json: %v\n", err)
				return nil
			}
			requests = export.Requests
		}

		// Extract auth tokens
		tokens := extractAuthTokens(requests, authDomain)

		if len(tokens) == 0 {
			pterm.Info.Println("No auth tokens found in captured requests")
			return nil
		}

		// Output based on mode
		if authSave {
			// Save to shell env file
			envPath, err := saveAuthEnv(tokens, authDomain)
			if err != nil {
				return fmt.Errorf("failed to save auth env: %w", err)
			}
			if authEnv || authShell || authVars {
				if authVars {
					return printAuthVars(envPath, authPrefix, authDomain)
				}
				return printAuthEnv(envPath, authShell)
			}
			if getOutputMode() == "json" {
				out, _ := sonic.MarshalIndent(map[string]interface{}{
					"saved":  envPath,
					"env":    envPath,
					"tokens": len(tokens),
				}, "", "  ")
				fmt.Println(string(out))
			} else {
				pterm.Success.Printf("Saved %d auth tokens to %s\n", len(tokens), envPath)
				fmt.Println("\nLoad into shell:")
				fmt.Printf("  source \"%s\"\n", envPath)
				domainArg := ""
				if strings.TrimSpace(authDomain) != "" {
					domainArg = fmt.Sprintf(" -d %s", shellQuote(authDomain))
				}
				prefix := resolveAuthPrefix(authPrefix, authDomain)
				fmt.Printf("  eval \"$(rep auth --vars%s --prefix %s)\"\n", domainArg, prefix)
			}
			return nil
		} else if authExport {
			// Shell export format
			for _, t := range tokens {
				// Escape single quotes in value
				escaped := strings.ReplaceAll(t.Value, "'", "'\"'\"'")
				fmt.Printf("export %s='%s'\n", t.Name, escaped)
			}
			fmt.Println("# Usage: eval \"$(rep auth --export)\"")
		} else if getOutputMode() == "json" {
			out, _ := sonic.MarshalIndent(tokens, "", "  ")
			fmt.Println(string(out))
		} else {
			// Human-readable format
			pterm.DefaultSection.Println("Extracted Auth Tokens")

			// Group by domain
			byDomain := make(map[string][]AuthToken)
			for _, t := range tokens {
				byDomain[t.Domain] = append(byDomain[t.Domain], t)
			}

			domains := make([]string, 0, len(byDomain))
			for d := range byDomain {
				domains = append(domains, d)
			}
			sort.Strings(domains)

			for _, domain := range domains {
				fmt.Printf("\n%s:\n", pterm.Bold.Sprint(domain))
				for _, t := range byDomain[domain] {
					// Truncate value for display
					displayVal := t.Value
					if len(displayVal) > 50 {
						displayVal = displayVal[:25] + "..." + displayVal[len(displayVal)-15:]
					}
					fmt.Printf("  %s=%s\n", pterm.FgCyan.Sprint(t.Name), displayVal)
					fmt.Printf("    Source: %s\n", t.Source)
				}
			}

			fmt.Println()
			pterm.Info.Printf("Found %d auth tokens\n", len(tokens))
			fmt.Println("Use --save to write shell env file (recommended)")
			fmt.Println("Use --vars to export prefixed variables (after --save)")
			fmt.Println("Use --export for shell variables (legacy)")
		}

		return nil
	},
}

// getRepConfigDir returns ~/.rep/ directory path
func getRepConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".rep"), nil
}

// authEnvPath returns the shell env file path for a domain (or default).
func authEnvPath(domain string) (string, error) {
	configDir, err := getRepConfigDir()
	if err != nil {
		return "", err
	}

	configFile := "auth.env"
	trimmedDomain := strings.TrimSpace(domain)
	if trimmedDomain != "" {
		configFile = fmt.Sprintf("auth-%s.env", sanitizeDomainForFilename(trimmedDomain))
	}

	return filepath.Join(configDir, configFile), nil
}

func printAuthEnv(envPath string, shell bool) error {
	if getOutputMode() == "json" {
		payload := map[string]interface{}{
			"env": envPath,
		}
		if shell {
			payload["source"] = fmt.Sprintf("source %s", shellQuote(envPath))
		}
		out, _ := sonic.MarshalIndent(payload, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	if shell {
		fmt.Printf("source %s\n", shellQuote(envPath))
	} else {
		fmt.Println(envPath)
	}
	return nil
}

func printAuthVars(envPath, prefix, domain string) error {
	resolvedPrefix := resolveAuthPrefix(prefix, domain)
	lines := buildAuthVarLines(envPath, resolvedPrefix)

	if getOutputMode() == "json" {
		payload := map[string]interface{}{
			"env":    envPath,
			"prefix": resolvedPrefix,
			"shell":  strings.Join(lines, "\n"),
		}
		out, _ := sonic.MarshalIndent(payload, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	fmt.Println(strings.Join(lines, "\n"))
	return nil
}

func buildAuthVarLines(envPath, prefix string) []string {
	if prefix == "" {
		prefix = "TARGET"
	}
	authVar := fmt.Sprintf("%s_AUTH", prefix)
	lines := []string{
		fmt.Sprintf("source %s", shellQuote(envPath)),
		fmt.Sprintf("if [ -z \"${%s:-}\" ] && [ -n \"${BEARER_TOKEN:-}\" ]; then export %s=\"Bearer $BEARER_TOKEN\"; fi", authVar, authVar),
		fmt.Sprintf("if [ -z \"${%s:-}\" ] && [ -n \"${BASIC_AUTH:-}\" ]; then export %s=\"Basic $BASIC_AUTH\"; fi", authVar, authVar),
		fmt.Sprintf("if [ -z \"${%s:-}\" ] && [ -n \"${AUTH_TOKEN:-}\" ]; then export %s=\"$AUTH_TOKEN\"; fi", authVar, authVar),
		fmt.Sprintf("if [ -n \"${SESSION_COOKIE:-}\" ]; then export %s_COOKIE=\"$SESSION_COOKIE\"; fi", prefix),
		fmt.Sprintf("if [ -n \"${CSRF_TOKEN:-}\" ]; then export %s_CSRF=\"$CSRF_TOKEN\"; fi", prefix),
		fmt.Sprintf("if [ -n \"${XSRF_TOKEN:-}\" ]; then export %s_XSRF=\"$XSRF_TOKEN\"; fi", prefix),
		fmt.Sprintf("if [ -n \"${API_KEY:-}\" ]; then export %s_API_KEY=\"$API_KEY\"; fi", prefix),
		fmt.Sprintf("if [ -n \"${ACCESS_TOKEN:-}\" ]; then export %s_ACCESS_TOKEN=\"$ACCESS_TOKEN\"; fi", prefix),
		fmt.Sprintf("if [ -n \"${AUTH_TOKEN:-}\" ]; then export %s_AUTH_TOKEN=\"$AUTH_TOKEN\"; fi", prefix),
	}
	return lines
}

func resolveAuthPrefix(prefix, domain string) string {
	if cleaned := sanitizeEnvPrefix(prefix); cleaned != "" {
		return cleaned
	}
	if cleaned := sanitizeEnvPrefix(domain); cleaned != "" {
		return cleaned
	}
	return "TARGET"
}

func sanitizeEnvPrefix(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(trimmed); i++ {
		ch := trimmed[i]
		switch {
		case ch >= 'a' && ch <= 'z':
			b.WriteByte(ch - ('a' - 'A'))
		case ch >= 'A' && ch <= 'Z':
			b.WriteByte(ch)
		case ch >= '0' && ch <= '9':
			b.WriteByte(ch)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return ""
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "AUTH_" + out
	}
	return out
}

func sanitizeDomainForFilename(domain string) string {
	normalized := strings.TrimSpace(strings.ToLower(domain))
	if normalized == "" {
		return ""
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return replacer.Replace(normalized)
}

// saveAuthEnv writes auth tokens to a shell env file.
func saveAuthEnv(tokens []AuthToken, domain string) (string, error) {
	envPath, err := authEnvPath(domain)
	if err != nil {
		return "", err
	}

	// Create directory if needed
	if err := os.MkdirAll(filepath.Dir(envPath), 0700); err != nil {
		return "", err
	}

	// Build shell export content
	var lines []string
	seen := make(map[string]bool)

	for _, t := range tokens {
		exportLine := fmt.Sprintf("export %s=%s", t.Name, shellQuote(t.Value))

		// Deduplicate
		if !seen[exportLine] {
			seen[exportLine] = true
			lines = append(lines, exportLine)
		}
	}

	content := strings.Join(lines, "\n") + "\n"

	// Write with secure permissions (0600)
	if err := os.WriteFile(envPath, []byte(content), 0600); err != nil {
		return "", err
	}

	return envPath, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func extractAuthTokens(requests []store.Request, filterDomain string) []AuthToken {
	seen := make(map[string]bool) // Deduplicate by name+value
	var tokens []AuthToken

	for _, req := range requests {
		// Compute domain if not set
		domain := req.Domain
		if domain == "" {
			store.ComputeRequestFields(&req)
			domain = req.Domain
		}

		// Filter by domain if specified
		if filterDomain != "" && !strings.EqualFold(domain, filterDomain) {
			continue
		}

		// Extract from various auth headers
		extractFromHeader := func(headerName, varPrefix string) {
			value := store.HeaderFirst(req.Headers, headerName)
			if value == "" {
				return
			}

			varName := varPrefix
			actualValue := value

			// Handle Authorization header specially
			if strings.EqualFold(headerName, "authorization") {
				if strings.HasPrefix(strings.ToLower(value), "bearer ") {
					varName = "BEARER_TOKEN"
					actualValue = strings.TrimPrefix(value, value[:7]) // Remove "Bearer "
				} else if strings.HasPrefix(strings.ToLower(value), "basic ") {
					varName = "BASIC_AUTH"
					actualValue = strings.TrimPrefix(value, value[:6]) // Remove "Basic "
				} else {
					varName = "AUTH_TOKEN"
				}
			}

			key := varName + ":" + actualValue
			if seen[key] {
				return
			}
			seen[key] = true

			tokens = append(tokens, AuthToken{
				Name:   varName,
				Value:  actualValue,
				Source: headerName,
				Domain: domain,
			})
		}

		// Check common auth headers
		extractFromHeader("authorization", "AUTH")
		extractFromHeader("x-api-key", "API_KEY")
		extractFromHeader("x-auth-token", "AUTH_TOKEN")
		extractFromHeader("x-access-token", "ACCESS_TOKEN")
		extractFromHeader("x-csrf-token", "CSRF_TOKEN")
		extractFromHeader("x-xsrf-token", "XSRF_TOKEN")

		// Handle cookies specially - extract the full cookie string
		cookie := store.HeaderFirst(req.Headers, "cookie")
		if cookie != "" {
			key := "COOKIE:" + cookie
			if !seen[key] {
				seen[key] = true
				tokens = append(tokens, AuthToken{
					Name:   "SESSION_COOKIE",
					Value:  cookie,
					Source: "Cookie",
					Domain: domain,
				})
			}

			// Also extract individual session cookies
			extractSessionCookies(cookie, domain, seen, &tokens)
		}
	}

	return tokens
}

// extractSessionCookies extracts common session cookie values
func extractSessionCookies(cookieStr, domain string, seen map[string]bool, tokens *[]AuthToken) {
	// Common session cookie patterns
	patterns := []struct {
		name    string
		varName string
	}{
		{"session", "SESSION_ID"},
		{"sessionid", "SESSION_ID"},
		{"PHPSESSID", "PHP_SESSION"},
		{"JSESSIONID", "JAVA_SESSION"},
		{"connect.sid", "CONNECT_SID"},
		{"auth_token", "AUTH_TOKEN_COOKIE"},
		{"access_token", "ACCESS_TOKEN_COOKIE"},
		{"jwt", "JWT_COOKIE"},
		{"token", "TOKEN_COOKIE"},
	}

	for _, p := range patterns {
		re := regexp.MustCompile(fmt.Sprintf(`(?i)%s=([^;]+)`, regexp.QuoteMeta(p.name)))
		matches := re.FindStringSubmatch(cookieStr)
		if len(matches) > 1 {
			key := p.varName + ":" + matches[1]
			if !seen[key] {
				seen[key] = true
				*tokens = append(*tokens, AuthToken{
					Name:   p.varName,
					Value:  matches[1],
					Source: "Cookie (" + p.name + ")",
					Domain: domain,
				})
			}
		}
	}
}

func init() {
	rootCmd.AddCommand(authCmd)
	authCmd.Flags().BoolVar(&authSave, "save", false, "Save auth to ~/.rep/auth.env (or auth-<domain>.env) for shell sourcing")
	authCmd.Flags().BoolVar(&authEnv, "env", false, "Print auth env path only")
	authCmd.Flags().BoolVar(&authShell, "shell", false, "Print shell source command for auth env")
	authCmd.Flags().BoolVar(&authVars, "vars", false, "Print shell exports for prefixed auth variables")
	authCmd.Flags().StringVar(&authPrefix, "prefix", "", "Prefix for --vars exports (default: domain-derived)")
	authCmd.Flags().BoolVar(&authExport, "export", false, "Output as shell export statements (legacy)")
	authCmd.Flags().StringVarP(&authDomain, "domain", "d", "", "Filter by domain")
	authCmd.Flags().StringVar(&authSaved, "saved", "", "Read from saved session (ID or 'latest')")
}
