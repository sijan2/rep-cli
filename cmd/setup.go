package cmd

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/noise"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	setupKeepConfig     bool
	setupExact          bool
	setupSaved          string
	setupWrite          bool
	setupIncludeSecrets bool
)

// TokenInfo carries every token field; Value is only emitted when the caller
// explicitly opts in via --include-secrets. Default JSON output uses
// TokenInfoRedacted instead (see buildRedactedTokens).
type TokenInfo struct {
	Name    string `json:"name"`
	Value   string `json:"value,omitempty"`
	Domain  string `json:"domain"`
	Header  string `json:"header"`
	EnvName string `json:"env_name"`
	Hint    string `json:"hint,omitempty"`
}

// TokenInfoRedacted is the agent-safe projection: no raw secret, a short
// preview for identification, and a pointer to the shell env file where the
// full value is persisted.
type TokenInfoRedacted struct {
	Name         string `json:"name"`
	ValuePreview string `json:"value_preview"`
	Domain       string `json:"domain"`
	Header       string `json:"header"`
	EnvName      string `json:"env_name"`
	EnvFile      string `json:"env_file,omitempty"`
	Hint         string `json:"hint,omitempty"`
}

// setupConfigDiff summarizes state mutations so dry-run can show "would change"
// and the --write path can show "changed".
type setupConfigDiff struct {
	PreviousPrimaries  []string `json:"previous_primaries"`
	NewPrimaries       []string `json:"new_primaries"`
	PreviousIgnored    []string `json:"previous_ignored"`
	NewIgnored         []string `json:"new_ignored"`
	StorePath          string   `json:"store_path,omitempty"`
	Applied            bool     `json:"applied"`
}

// setupOutput is the JSON envelope for `rep setup`. The token shape changes
// based on --include-secrets, but the rest of the envelope stays stable.
type setupOutput struct {
	Target         string              `json:"target"`
	PrimaryDomains []string            `json:"primary_domains"`
	IgnoredDomains []string            `json:"ignored_domains,omitempty"`
	Tokens         []TokenInfoRedacted `json:"tokens,omitempty"`
	TokensRaw      []TokenInfo         `json:"tokens_raw,omitempty"` // only with --include-secrets
	EnvFile        string              `json:"env_file,omitempty"`
	ConfigDiff     *setupConfigDiff    `json:"config_diff,omitempty"`
	AgentHint      string              `json:"agent_hint,omitempty"`
	Summary        *Summary            `json:"summary,omitempty"`
}

// tokenValuePreview redacts a secret to a short preview suitable for agent
// context. Keeps the shape identifiable without exposing the full value.
func tokenValuePreview(v string) string {
	if len(v) <= 10 {
		return "••"
	}
	return v[:4] + "…" + v[len(v)-4:]
}

// buildRedactedTokens projects TokenInfo -> TokenInfoRedacted, attaching the
// env-file path so the agent can source it instead of reading the value.
func buildRedactedTokens(tokens []TokenInfo, envFile string) []TokenInfoRedacted {
	out := make([]TokenInfoRedacted, len(tokens))
	for i, t := range tokens {
		out[i] = TokenInfoRedacted{
			Name:         t.Name,
			ValuePreview: tokenValuePreview(t.Value),
			Domain:       t.Domain,
			Header:       t.Header,
			EnvName:      t.EnvName,
			EnvFile:      envFile,
			Hint:         t.Hint,
		}
	}
	return out
}

// tokenInfosToAuthTokens adapts TokenInfo (setup) to AuthToken (auth) so we
// can reuse saveAuthEnv unchanged.
func tokenInfosToAuthTokens(tokens []TokenInfo) []AuthToken {
	out := make([]AuthToken, len(tokens))
	for i, t := range tokens {
		out[i] = AuthToken{
			Name:   t.EnvName,
			Value:  t.Value,
			Source: t.Header,
			Domain: t.Domain,
		}
	}
	return out
}

var setupCmd = &cobra.Command{
	Use:   "setup <target-domain>",
	Short: "Initialize recon with token extraction",
	Long: `Initialize recon for a target domain.

By default this command is a DRY RUN: it previews what primary/ignore
config would change and extracts tokens (saved to a shell env file) but
never mutates ~/.local/share/rep-cli/store.json. Pass --write to commit.

Previews (default):
  1. Shows the primary-domain list that WOULD be set
  2. Shows the noise domains that WOULD be ignored
  3. Extracts tokens into a shell env file at ~/.rep/auth-<domain>.env
  4. JSON output shows token previews + env-file path (NO raw secrets)

With --write:
  - Clears old filters and persists primaries + ignores into store.json
  - Still writes the auth env file
  - Still redacts tokens unless --include-secrets is set

For AI agents:
  rep setup example.com --json              Dry-run, agent-safe (redacted)
  rep setup example.com --write --json      Commit + agent-safe
  source ~/.rep/auth-example.com.env        Load secrets into shell

Examples:
  rep setup example.com                     Dry-run with preview
  rep setup example.com --write             Commit config
  rep setup example.com --json              Agent-friendly preview
  rep setup example.com --include-secrets   Escape hatch: raw values in JSON`,
	Args: cobra.ExactArgs(1),
	RunE: runSetup,
}

func runSetup(cmd *cobra.Command, args []string) error {
	target := strings.TrimSpace(args[0])
	if target == "" {
		return fmt.Errorf("target domain required")
	}
	if normalized := hostFromURL(target); normalized != "" {
		target = normalized
	}

	s, err := store.Get()
	if err != nil {
		return fmt.Errorf("failed to load store: %w", err)
	}

	// Snapshot current state BEFORE any in-memory mutation so we can
	// report a coherent diff regardless of --write.
	previousPrimaries := append([]string(nil), s.GetPrimaryDomains()...)
	previousIgnored := snapshotIgnored(s)

	requests, _ := loadSetupRequests(s)

	var tempStore *store.Store
	if len(requests) > 0 {
		tempStore = store.NewTempStore(requests)
	}

	// Compute what primaries WOULD be set, but only apply in --write mode.
	primaryDomains := resolvePrimaryDomains(target, tempStore, setupExact)
	if len(primaryDomains) == 0 {
		primaryDomains = []string{target}
	}

	// Compute noise domains against the proposed primaries (simulation).
	var ignoredDomains []string
	if tempStore != nil {
		simStore := store.NewTempStore(requests)
		simPrimary := make(map[string]bool, len(primaryDomains))
		for _, d := range primaryDomains {
			simPrimary[d] = true
		}
		simStore.PrimaryDomains = simPrimary
		simStore.IgnoredDomains = s.IgnoredDomains
		ignoredDomains = detectNoiseDomains(simStore)
	}

	// Extract tokens from the proposed primary set (does not mutate store).
	var tokens []TokenInfo
	if tempStore != nil {
		simStore := store.NewTempStore(requests)
		simPrimary := make(map[string]bool, len(primaryDomains))
		for _, d := range primaryDomains {
			simPrimary[d] = true
		}
		simStore.PrimaryDomains = simPrimary
		tokens = extractTokens(simStore, primaryDomains, target)
	}

	// Always persist the auth env file — that's non-destructive to store.json
	// and lets the agent `source` it without seeing raw values. If token
	// extraction found nothing, skip the file write.
	envFile := ""
	if len(tokens) > 0 {
		path, err := saveAuthEnv(tokenInfosToAuthTokens(tokens), target)
		if err != nil {
			pterm.Warning.Printf("Could not save auth env: %v\n", err)
		} else {
			envFile = path
		}
	}

	// Commit config to store.json only when --write is set.
	if setupWrite {
		if !setupKeepConfig {
			s.ClearIgnoreList()
			s.ClearMutedPaths()
			if primaries := s.GetPrimaryDomains(); len(primaries) > 0 {
				s.UnsetPrimary(primaries...)
			}
		}
		s.SetPrimary(primaryDomains...)
		if len(ignoredDomains) > 0 {
			s.Ignore(ignoredDomains...)
		}
		if err := s.Save(); err != nil {
			return fmt.Errorf("failed to save store: %w", err)
		}
	}

	diff := &setupConfigDiff{
		PreviousPrimaries: previousPrimaries,
		NewPrimaries:      primaryDomains,
		PreviousIgnored:   previousIgnored,
		NewIgnored:        ignoredDomains,
		Applied:           setupWrite,
	}
	if path, err := store.GetStoreFilePath(); err == nil {
		diff.StorePath = path
	}

	// JSON output (agent-friendly).
	if getOutputMode() == "json" {
		output := setupOutput{
			Target:         target,
			PrimaryDomains: primaryDomains,
			IgnoredDomains: ignoredDomains,
			EnvFile:        envFile,
			ConfigDiff:     diff,
		}
		if setupIncludeSecrets {
			output.TokensRaw = tokens
			output.AgentHint = "Raw secret values included (--include-secrets). Prefer sourcing env_file instead."
		} else {
			output.Tokens = buildRedactedTokens(tokens, envFile)
			if len(tokens) > 0 {
				output.AgentHint = "Token values redacted. Source env_file in the user's shell; reference via env_name."
			}
		}
		if tempStore != nil {
			tempStore.PrimaryDomains = s.PrimaryDomains
			tempStore.IgnoredDomains = s.IgnoredDomains
			domains := tempStore.GetDomains()
			summary := buildSummary(tempStore, domains, s)
			output.Summary = &summary
		}
		out, _ := sonic.MarshalIndent(output, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	// Human output.
	mode := "DRY RUN (use --write to commit)"
	if setupWrite {
		mode = "WRITTEN to store"
	}
	pterm.Info.Printf("mode: %s\n", mode)
	pterm.Success.Printf("Target: %s\n", target)
	pterm.Success.Printf("Primary: %s\n", strings.Join(primaryDomains, ", "))
	if len(ignoredDomains) > 0 {
		pterm.Info.Printf("Noise to ignore (%d): %s\n", len(ignoredDomains), strings.Join(ignoredDomains, ", "))
	}
	printSetupDiff(diff)

	if len(tokens) > 0 {
		fmt.Println()
		pterm.DefaultSection.Println("Tokens Found (values redacted)")
		for _, t := range tokens {
			fmt.Printf("  %s  %s=%s  (from %s)\n", t.EnvName, t.EnvName, tokenValuePreview(t.Value), t.Header)
		}
		if envFile != "" {
			fmt.Println()
			pterm.Success.Printf("Saved %d tokens to: %s\n", len(tokens), envFile)
			fmt.Printf("Load into shell: source %q\n", envFile)
		}
		if setupIncludeSecrets {
			fmt.Println()
			pterm.Warning.Println("--include-secrets is set; raw values are in --json output")
		}
	}

	if tempStore != nil {
		tempStore.PrimaryDomains = s.PrimaryDomains
		tempStore.IgnoredDomains = s.IgnoredDomains
		domains := tempStore.GetDomains()
		summary := buildSummary(tempStore, domains, s)
		fmt.Println()
		printSummary(summary, domains, tempStore)
	}

	return nil
}

// snapshotIgnored captures the current ignore-list as a sorted slice.
func snapshotIgnored(s *store.Store) []string {
	out := make([]string, 0, len(s.IgnoredDomains))
	for d := range s.IgnoredDomains {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// printSetupDiff shows the before/after config change so the user (or agent)
// can verify exactly what a --write will do (or did do).
func printSetupDiff(diff *setupConfigDiff) {
	if diff == nil {
		return
	}
	fmt.Println()
	fmt.Printf("Primary: %d → %d\n", len(diff.PreviousPrimaries), len(diff.NewPrimaries))
	fmt.Printf("Ignored: %d → %d\n", len(diff.PreviousIgnored), len(diff.NewIgnored))
	if diff.StorePath != "" {
		if diff.Applied {
			fmt.Printf("store.json updated: %s\n", diff.StorePath)
		} else {
			fmt.Printf("store.json (unchanged): %s\n", diff.StorePath)
		}
	}
}

func extractTokens(tempStore *store.Store, primaryDomains []string, target string) []TokenInfo {
	var tokens []TokenInfo
	seen := make(map[string]bool)
	prefix := strings.ToUpper(sanitizeEnvName(strings.Split(target, ".")[0]))

	// Auth header patterns
	authHeaders := []struct {
		header  string
		envName string
		pattern *regexp.Regexp
	}{
		{"authorization", "AUTH", regexp.MustCompile(`(?i)^(bearer|basic)\s+(.+)$`)},
		{"x-api-key", "API_KEY", nil},
		{"x-auth-token", "AUTH_TOKEN", nil},
		{"x-access-token", "ACCESS_TOKEN", nil},
		{"x-csrf-token", "CSRF", nil},
		{"x-xsrf-token", "XSRF", nil},
	}

	// Auth cookie patterns
	authCookiePatterns := []string{"session", "token", "auth", "jwt", "sid", "csrf"}

	for _, req := range tempStore.Requests {
		// Only from primary domains
		isPrimary := false
		for _, pd := range primaryDomains {
			if store.IsFirstParty(req.Domain, pd) {
				isPrimary = true
				break
			}
		}
		if !isPrimary {
			continue
		}

		// Check auth headers
		for _, ah := range authHeaders {
			values := store.HeaderValues(req.Headers, ah.header)
			for _, v := range values {
				if v == "" {
					continue
				}
				tokenValue := v
				hint := ""
				if ah.pattern != nil {
					if m := ah.pattern.FindStringSubmatch(v); len(m) > 2 {
						tokenValue = m[2]
						hint = m[1] + " token"
					}
				}
				envName := prefix + "_" + ah.envName
				if !seen[envName] {
					seen[envName] = true
					tokens = append(tokens, TokenInfo{
						Name:    ah.header,
						Value:   tokenValue,
						Domain:  req.Domain,
						Header:  ah.header,
						EnvName: envName,
						Hint:    hint,
					})
				}
			}
		}

		// Check cookies
		cookieHeader := store.HeaderFirst(req.Headers, "cookie")
		if cookieHeader != "" {
			for _, cookie := range strings.Split(cookieHeader, ";") {
				parts := strings.SplitN(strings.TrimSpace(cookie), "=", 2)
				if len(parts) != 2 || parts[1] == "" {
					continue
				}
				name, value := parts[0], parts[1]
				nameLower := strings.ToLower(name)

				for _, pattern := range authCookiePatterns {
					if strings.Contains(nameLower, pattern) {
						envName := prefix + "_COOKIE_" + strings.ToUpper(sanitizeEnvName(name))
						if !seen[envName] {
							seen[envName] = true
							tokens = append(tokens, TokenInfo{
								Name:    name,
								Value:   value,
								Domain:  req.Domain,
								Header:  "cookie",
								EnvName: envName,
								Hint:    "cookie: " + name,
							})
						}
						break
					}
				}
			}
		}
	}

	return tokens
}

func sanitizeEnvName(s string) string {
	var b strings.Builder
	for _, c := range s {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func loadSetupRequests(s *store.Store) ([]store.Request, string) {
	if setupSaved != "" {
		var session *store.Session
		if setupSaved == "latest" || setupSaved == "last" {
			session = s.GetLatestSession()
		} else {
			session = s.GetSession(setupSaved)
		}
		if session == nil {
			return nil, "saved"
		}
		return session.Requests, "saved"
	}

	livePath, err := store.GetLiveFilePath()
	if err != nil {
		return nil, "live"
	}
	export, err := loadLiveExport(livePath)
	if err != nil {
		return nil, "live"
	}
	return export.Requests, "live"
}

func resolvePrimaryDomains(target string, tempStore *store.Store, exact bool) []string {
	if exact || tempStore == nil {
		return []string{target}
	}
	targetBase := store.GetBaseDomain(target)
	domainSet := map[string]bool{target: true}
	for _, req := range tempStore.Requests {
		if req.Domain != "" && store.GetBaseDomain(req.Domain) == targetBase {
			domainSet[req.Domain] = true
		}
	}
	return mapKeysSorted(domainSet)
}

func detectNoiseDomains(tempStore *store.Store) []string {
	domains := tempStore.GetDomains()
	var ignored []string
	for _, d := range domains {
		if !d.IsPrimary && !d.IsIgnored && noise.DetectNoiseType(d.Domain) != "" {
			ignored = append(ignored, d.Domain)
		}
	}
	sort.Strings(ignored)
	return ignored
}

func mapKeysSorted(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func init() {
	rootCmd.AddCommand(setupCmd)
	setupCmd.Flags().BoolVar(&setupKeepConfig, "keep-config", false, "Preserve existing config (no-op in dry-run)")
	setupCmd.Flags().BoolVar(&setupExact, "exact", false, "Only set exact domain as primary")
	setupCmd.Flags().StringVar(&setupSaved, "saved", "", "Read from saved session")
	setupCmd.Flags().BoolVar(&setupWrite, "write", false, "Commit changes to store.json (without, setup is a dry-run)")
	setupCmd.Flags().BoolVar(&setupIncludeSecrets, "include-secrets", false, "Emit raw token values in --json (default: redacted)")
}
