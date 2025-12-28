package cmd

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	domainsPrimary bool
	domainsIgnored bool
	domainsAll     bool
	domainsSaved   string
	domainsLimit   int
)

var domainsCmd = &cobra.Command{
	Use:   "domains",
	Short: "List all domains with statistics",
	Long: `List all unique domains captured in traffic.

Default: Shows domains from LIVE session (real-time).
Use --saved to view domains from archived sessions.

  rep domains              Show active domains from live session
  rep domains --all        Show all domains including ignored
  rep domains --primary    Show only primary domains
  rep domains --ignored    Show only ignored domains
  rep domains --saved latest   Show domains from most recent saved session`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var tempStore *store.Store

		if domainsSaved != "" {
			// Load from saved session
			s, err := store.Get()
			if err != nil {
				return fmt.Errorf("failed to load store: %w", err)
			}

			var session *store.Session
			if domainsSaved == "latest" || domainsSaved == "last" {
				session = s.GetLatestSession()
			} else {
				session = s.GetSession(domainsSaved)
			}

			if session == nil {
				pterm.Warning.Printf("Session not found: %s\n", domainsSaved)
				pterm.Info.Println("Use 'rep sessions' to list available sessions")
				return nil
			}

			tempStore = store.NewTempStore(session.Requests)
			tempStore.PrimaryDomains = s.PrimaryDomains
			tempStore.IgnoredDomains = s.IgnoredDomains
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
			// Load ignore/primary lists from store
			s, err := store.Get()
			if err == nil {
				tempStore.PrimaryDomains = s.PrimaryDomains
				tempStore.IgnoredDomains = s.IgnoredDomains
			}
		}

		domains := tempStore.GetDomains()

		// Filter based on flags
		var filtered []store.DomainInfo
		for _, d := range domains {
			if domainsAll {
				filtered = append(filtered, d)
			} else if domainsPrimary && d.IsPrimary {
				filtered = append(filtered, d)
			} else if domainsIgnored && d.IsIgnored {
				filtered = append(filtered, d)
			} else if !domainsPrimary && !domainsIgnored && !d.IsIgnored {
				filtered = append(filtered, d)
			}
		}

		// Apply limit
		totalCount := len(filtered)
		if domainsLimit > 0 && len(filtered) > domainsLimit {
			filtered = filtered[:domainsLimit]
		}

		if getOutputMode() == "json" {
			out, _ := sonic.MarshalIndent(filtered, "", "  ")
			fmt.Println(string(out))
		} else {
			printDomains(filtered, totalCount, domainsLimit)
		}

		return nil
	},
}

func printDomains(domains []store.DomainInfo, totalCount, limit int) {
	if len(domains) == 0 {
		pterm.Info.Println("No domains match the filter")
		return
	}

	// Create table
	tableData := pterm.TableData{{"Domain", "Requests", "Endpoints", "Methods", "Status"}}

	for _, d := range domains {
		methodStr := ""
		for m, count := range d.Methods {
			if methodStr != "" {
				methodStr += ", "
			}
			methodStr += fmt.Sprintf("%s:%d", m, count)
		}

		status := ""
		if d.IsPrimary {
			status = "PRIMARY"
		} else if d.IsIgnored {
			status = "IGNORED"
		}

		tableData = append(tableData, []string{
			d.Domain,
			fmt.Sprintf("%d", d.RequestCount),
			fmt.Sprintf("%d", len(d.Endpoints)),
			methodStr,
			status,
		})
	}

	pterm.DefaultTable.WithHasHeader().WithData(tableData).Render()

	// Show truncation indicator
	if limit > 0 && len(domains) < totalCount {
		fmt.Printf("\n[Showing %d of %d domains]\n", len(domains), totalCount)
	} else {
		fmt.Printf("\nTotal: %d domains\n", len(domains))
	}
}

func init() {
	rootCmd.AddCommand(domainsCmd)
	domainsCmd.Flags().BoolVar(&domainsPrimary, "primary", false, "Show only primary domains")
	domainsCmd.Flags().BoolVar(&domainsIgnored, "ignored", false, "Show only ignored domains")
	domainsCmd.Flags().BoolVar(&domainsAll, "all", false, "Show all domains including ignored")
	domainsCmd.Flags().StringVar(&domainsSaved, "saved", "", "Read from saved session (ID, prefix, or 'latest')")
	domainsCmd.Flags().IntVarP(&domainsLimit, "limit", "l", 0, "Limit number of domains shown (0=unlimited)")
}
