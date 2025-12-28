package cmd

import (
	"fmt"
	"net/url"
	"sort"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	chainSaved string
)

var chainCmd = &cobra.Command{
	Use:   "chain [request-id]",
	Short: "Show request chain based on initiator relationships",
	Long: `Analyze request chains based on initiator relationships.

Shows how requests are connected through their initiator field.
Useful for understanding request flows like: Page → XHR → Redirect → Final

Default: Analyzes chains from LIVE session (real-time).
Use --saved to analyze chains from archived sessions.

Without arguments, shows all unique chains grouped by page.
With a request ID, shows the chain for that specific request.

Examples:
  rep chain                     Show all request chains from live session
  rep chain h_abc123            Show chain for specific request
  rep chain --saved latest      Show chains from most recent saved session
  rep chain -o json             JSON output for agents`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var tempStore *store.Store
		var persistentStore *store.Store

		// Load persistent store for ignore/primary lists
		var err error
		persistentStore, err = store.Get()
		if err != nil {
			return fmt.Errorf("failed to load store: %w", err)
		}

		if chainSaved != "" {
			// Load from saved session
			var session *store.Session
			if chainSaved == "latest" || chainSaved == "last" {
				session = persistentStore.GetLatestSession()
			} else {
				session = persistentStore.GetSession(chainSaved)
			}

			if session == nil {
				pterm.Warning.Printf("Session not found: %s\n", chainSaved)
				pterm.Info.Println("Use 'rep sessions' to list available sessions")
				return nil
			}

			tempStore = store.NewTempStore(session.Requests)
		} else {
			// Default: Load from live.json
			livePath, err := store.GetLiveFilePath()
			if err != nil {
				return fmt.Errorf("failed to get live path: %w", err)
			}
			export, err := loadLiveExport(livePath)
			if err != nil {
				pterm.Warning.Printf("Could not read live.json: %v\n", err)
				pterm.Info.Println("Enable auto-export in rep+ extension first")
				return nil
			}
			if len(export.Requests) == 0 {
				pterm.Info.Println("No requests captured yet (live session empty)")
				return nil
			}

			tempStore = store.NewTempStore(export.Requests)
		}

		// Apply ignore/primary lists
		tempStore.PrimaryDomains = persistentStore.PrimaryDomains
		tempStore.IgnoredDomains = persistentStore.IgnoredDomains

		if tempStore.Count() == 0 {
			pterm.Info.Println("No requests found")
			return nil
		}

		if len(args) > 0 {
			return showRequestChain(tempStore, args[0])
		}

		return showAllChains(tempStore)
	},
}

// ChainLink represents a single request in a chain
type ChainLink struct {
	ID           string `json:"id"`
	Method       string `json:"method"`
	URL          string `json:"url"`
	Status       int    `json:"status,omitempty"`
	Initiator    string `json:"initiator,omitempty"`
	ResourceType string `json:"resource_type,omitempty"`
}

// RequestChain represents a chain of requests
type RequestChain struct {
	PageURL string      `json:"page_url"`
	Links   []ChainLink `json:"links"`
}

func showRequestChain(s *store.Store, requestID string) error {
	req := s.GetRequest(requestID)
	if req == nil {
		return fmt.Errorf("request not found: %s", requestID)
	}

	// Build chain by following initiator
	chain := buildChainForRequest(s, req)

	if getOutputMode() == "json" {
		out, _ := sonic.MarshalIndent(chain, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	// Terminal output
	pterm.DefaultSection.Printf("Request Chain for %s\n", requestID)
	for i, link := range chain.Links {
		prefix := "├─"
		if i == len(chain.Links)-1 {
			prefix = "└─"
		}
		statusStr := ""
		if link.Status > 0 {
			statusStr = fmt.Sprintf(" [%d]", link.Status)
		}
		initiatorStr := ""
		if link.Initiator != "" && link.Initiator != link.URL {
			initiatorStr = fmt.Sprintf(" (from: %s)", truncateURL(link.Initiator, 50))
		}
		fmt.Printf("  %s %s %s%s%s\n", prefix, link.Method, truncateURL(link.URL, 60), statusStr, initiatorStr)
	}

	return nil
}

func showAllChains(s *store.Store) error {
	// Group requests by PageURL
	pageGroups := make(map[string][]store.Request)
	requests := s.Filter(store.FilterOptions{ExcludeIgnored: true})

	for _, req := range requests {
		pageURL := req.PageURL
		if pageURL == "" {
			pageURL = req.URL
		}
		pageGroups[pageURL] = append(pageGroups[pageURL], req)
	}

	// Build chains for each page
	var chains []RequestChain
	for pageURL, reqs := range pageGroups {
		chain := RequestChain{
			PageURL: pageURL,
			Links:   make([]ChainLink, 0),
		}

		// Sort by timestamp
		sort.Slice(reqs, func(i, j int) bool {
			return reqs[i].Timestamp < reqs[j].Timestamp
		})

		for _, req := range reqs {
			status := 0
			if req.Response != nil {
				status = req.Response.Status
			}
			chain.Links = append(chain.Links, ChainLink{
				ID:           req.ID,
				Method:       req.Method,
				URL:          req.URL,
				Status:       status,
				Initiator:    req.Initiator,
				ResourceType: req.ResourceType,
			})
		}

		chains = append(chains, chain)
	}

	// Sort chains by number of links descending
	sort.Slice(chains, func(i, j int) bool {
		return len(chains[i].Links) > len(chains[j].Links)
	})

	if getOutputMode() == "json" {
		out, _ := sonic.MarshalIndent(chains, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	// Terminal output
	pterm.DefaultSection.Println("Request Chains by Page")
	for _, chain := range chains {
		pageDomain := getDomainFromURL(chain.PageURL)
		pterm.Info.Printf("%s (%d requests)\n", pageDomain, len(chain.Links))

		// Group by initiator for cleaner display
		initiatorGroups := make(map[string][]ChainLink)
		for _, link := range chain.Links {
			initiator := link.Initiator
			if initiator == "" {
				initiator = "direct"
			}
			initiatorGroups[initiator] = append(initiatorGroups[initiator], link)
		}

		// Show first few from each initiator
		shown := 0
		for initiator, links := range initiatorGroups {
			if shown >= 10 {
				fmt.Printf("    ... and %d more\n", len(chain.Links)-shown)
				break
			}
			initiatorLabel := "direct"
			if initiator != "direct" {
				initiatorLabel = truncateURL(initiator, 40)
			}
			fmt.Printf("  [%s] → %d requests\n", initiatorLabel, len(links))
			for _, link := range links[:min(3, len(links))] {
				statusStr := ""
				if link.Status > 0 {
					statusStr = fmt.Sprintf(" [%d]", link.Status)
				}
				fmt.Printf("    • %s %s%s\n", link.Method, truncateURL(link.URL, 50), statusStr)
				shown++
			}
			if len(links) > 3 {
				fmt.Printf("    ... +%d more\n", len(links)-3)
			}
		}
		fmt.Println()
	}

	return nil
}

func buildChainForRequest(s *store.Store, req *store.Request) RequestChain {
	chain := RequestChain{
		PageURL: req.PageURL,
		Links:   make([]ChainLink, 0),
	}

	// Build the chain starting from the request
	visited := make(map[string]bool)
	current := req

	for current != nil && !visited[current.ID] {
		visited[current.ID] = true
		status := 0
		if current.Response != nil {
			status = current.Response.Status
		}

		// Prepend to show chain from root to target
		chain.Links = append([]ChainLink{{
			ID:           current.ID,
			Method:       current.Method,
			URL:          current.URL,
			Status:       status,
			Initiator:    current.Initiator,
			ResourceType: current.ResourceType,
		}}, chain.Links...)

		// Try to find parent by initiator URL
		if current.Initiator == "" {
			break
		}

		// Find request matching the initiator URL
		parent := findRequestByURL(s, current.Initiator)
		if parent == nil || parent.ID == current.ID {
			// Add initiator as root if no matching request
			chain.Links = append([]ChainLink{{
				URL:       current.Initiator,
				Method:    "→",
				Initiator: "",
			}}, chain.Links...)
			break
		}
		current = parent
	}

	return chain
}

func findRequestByURL(s *store.Store, targetURL string) *store.Request {
	requests := s.Filter(store.FilterOptions{})
	for i := range requests {
		if requests[i].URL == targetURL {
			return &requests[i]
		}
	}
	return nil
}

func truncateURL(u string, maxLen int) string {
	if len(u) <= maxLen {
		return u
	}
	// Try to preserve domain and end of path
	parsed, err := url.Parse(u)
	if err != nil {
		return u[:maxLen-3] + "..."
	}
	domain := parsed.Host
	if len(domain) > maxLen-10 {
		return u[:maxLen-3] + "..."
	}
	remaining := maxLen - len(domain) - 6
	path := parsed.Path
	if len(path) > remaining {
		path = "..." + path[len(path)-remaining+3:]
	}
	return domain + path
}

func getDomainFromURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return u
	}
	return parsed.Host
}

func init() {
	rootCmd.AddCommand(chainCmd)
	chainCmd.Flags().StringVar(&chainSaved, "saved", "", "Read from saved session (ID, prefix, or 'latest')")
}
